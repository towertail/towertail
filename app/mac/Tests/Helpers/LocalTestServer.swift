import CommonCrypto
import Foundation
import Network

/// A minimal, real HTTP/1.1 + WebSocket server used by RemoteBackend tests.
///
/// This is **not** a mock — it speaks the actual protocols over a TCP
/// socket bound to `127.0.0.1:0`. Tests use it to exercise the full
/// `URLSession` + `URLSessionWebSocketTask` code path in `RemoteClient`
/// without spinning up Docker. Use `IntegrationHarness` for end-to-end
/// tests against the real `towertail-server` binary.
///
/// Supports only the endpoints the Mac client hits:
///   - Bearer token header required on every request (401 if missing)
///   - REST: GET/POST/PUT/DELETE with JSON bodies
///   - WS at /v1/stream: upgrades and emits scripted text frames
///
/// Handlers are registered with closures so tests can script responses
/// and assert on captured requests.
@available(macOS 14.0, *)
final class LocalTestServer: @unchecked Sendable {
    struct CapturedRequest: Sendable {
        let method: String
        let path: String
        let headers: [String: String]
        let body: Data
    }

    typealias Handler = @Sendable (CapturedRequest) -> Response

    struct Response: Sendable {
        var status: Int
        var body: Data
        var contentType: String = "application/json"

        static func json(_ status: Int, _ data: Data) -> Response {
            Response(status: status, body: data)
        }

        static func empty(_ status: Int) -> Response {
            Response(status: status, body: Data())
        }
    }

    private let queue = DispatchQueue(label: "towertail.test-server")
    private let listener: NWListener
    private let listenerReady = DispatchSemaphore(value: 0)
    private let expectedToken: String

    /// Handlers keyed by "METHOD PATH". Path matching is prefix-aware: a
    /// registered "GET /v1/nodes/" will match "/v1/nodes/<uuid>".
    private var handlers: [String: Handler] = [:]

    /// Captured REST requests in receipt order.
    private(set) var captured: [CapturedRequest] = []
    private let capturedLock = NSLock()

    /// Active WS connections — tests push frames via `sendToAllWebSockets`.
    private var wsConnections: [NWConnection] = []
    private let wsLock = NSLock()

    var port: UInt16 { listener.port?.rawValue ?? 0 }

    var baseURL: URL {
        URL(string: "http://127.0.0.1:\(port)")!
    }

    init(token: String) throws {
        self.expectedToken = token
        let params = NWParameters.tcp
        params.allowLocalEndpointReuse = true
        params.requiredInterfaceType = .loopback
        self.listener = try NWListener(using: params, on: .init(rawValue: 0)!)
    }

    // MARK: - Lifecycle

    func start() throws {
        listener.stateUpdateHandler = { [weak self] state in
            switch state {
            case .ready:
                self?.listenerReady.signal()
            case .failed:
                self?.listenerReady.signal()
            default:
                break
            }
        }
        listener.newConnectionHandler = { [weak self] conn in
            self?.handle(connection: conn)
        }
        listener.start(queue: queue)
        if listenerReady.wait(timeout: .now() + 5) == .timedOut {
            throw NSError(domain: "LocalTestServer", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: "listener not ready within 5s"])
        }
    }

    func stop() {
        listener.cancel()
        wsLock.lock()
        for c in wsConnections { c.cancel() }
        wsConnections.removeAll()
        wsLock.unlock()
    }

    // MARK: - Handler registration

    func on(_ method: String, _ path: String, _ handler: @escaping Handler) {
        handlers["\(method) \(path)"] = handler
    }

    // MARK: - Connection dispatch

    private func handle(connection: NWConnection) {
        connection.start(queue: queue)
        receiveRequestLine(connection: connection, buffer: Data())
    }

    /// Read the request until we have headers + body (Content-Length).
    private func receiveRequestLine(connection: NWConnection, buffer: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, isEOF, error in
            guard let self else { return }
            if error != nil {
                connection.cancel()
                return
            }
            var buf = buffer
            if let data = data { buf.append(data) }
            // Find end of headers.
            guard let headersEnd = Self.indexOf(sequence: Data("\r\n\r\n".utf8), in: buf) else {
                if isEOF {
                    connection.cancel()
                    return
                }
                self.receiveRequestLine(connection: connection, buffer: buf)
                return
            }
            let headerData = buf.subdata(in: 0..<headersEnd)
            let restStart = headersEnd + 4
            guard let headerString = String(data: headerData, encoding: .utf8) else {
                connection.cancel()
                return
            }
            let lines = headerString.split(separator: "\r\n", omittingEmptySubsequences: false).map(String.init)
            guard let requestLine = lines.first else { connection.cancel(); return }
            let parts = requestLine.split(separator: " ").map(String.init)
            guard parts.count >= 2 else { connection.cancel(); return }
            let method = parts[0]
            let path = parts[1]
            var headers: [String: String] = [:]
            for line in lines.dropFirst() {
                if let colon = line.firstIndex(of: ":") {
                    let k = line[..<colon].trimmingCharacters(in: .whitespaces)
                    let v = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
                    headers[k.lowercased()] = v
                }
            }

            // WebSocket upgrade?
            if method == "GET" && path.hasPrefix("/v1/stream") {
                self.completeWebSocketUpgrade(connection: connection, headers: headers)
                return
            }

            let contentLength = Int(headers["content-length"] ?? "0") ?? 0
            let need = restStart + contentLength
            if buf.count < need {
                self.receiveUntilBody(connection: connection, buffer: buf, need: need,
                                      method: method, path: path, headers: headers, bodyStart: restStart)
                return
            }
            let body = buf.subdata(in: restStart..<need)
            self.dispatchREST(connection: connection, method: method, path: path, headers: headers, body: body)
        }
    }

    private func receiveUntilBody(connection: NWConnection, buffer: Data, need: Int,
                                  method: String, path: String, headers: [String: String], bodyStart: Int) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, isEOF, error in
            guard let self else { return }
            if error != nil { connection.cancel(); return }
            var buf = buffer
            if let data = data { buf.append(data) }
            if buf.count < need {
                if isEOF { connection.cancel(); return }
                self.receiveUntilBody(connection: connection, buffer: buf, need: need,
                                      method: method, path: path, headers: headers, bodyStart: bodyStart)
                return
            }
            let body = buf.subdata(in: bodyStart..<need)
            self.dispatchREST(connection: connection, method: method, path: path, headers: headers, body: body)
        }
    }

    private func dispatchREST(connection: NWConnection, method: String, path: String,
                              headers: [String: String], body: Data) {
        // Bearer auth required.
        let expected = "Bearer \(expectedToken)"
        guard headers["authorization"] == expected else {
            writeHTTP(connection: connection, status: 401, body: Data("{\"error\":{\"code\":\"unauthorized\",\"message\":\"missing or invalid bearer\"}}".utf8),
                      contentType: "application/json", closeAfter: true)
            return
        }

        let cap = CapturedRequest(method: method, path: path, headers: headers, body: body)
        capturedLock.lock()
        captured.append(cap)
        capturedLock.unlock()

        // Exact match first, then prefix match (for /v1/nodes/<id>-style paths).
        var handler = handlers["\(method) \(path)"]
        if handler == nil {
            for (key, h) in handlers where key.hasPrefix("\(method) ") {
                let registered = String(key.dropFirst(method.count + 1))
                if registered.hasSuffix("/") && path.hasPrefix(registered) {
                    handler = h
                    break
                }
            }
        }
        let response = handler?(cap) ?? Response(status: 404, body: Data("{\"error\":\"not found\"}".utf8))
        writeHTTP(connection: connection, status: response.status, body: response.body,
                  contentType: response.contentType, closeAfter: true)
    }

    private func writeHTTP(connection: NWConnection, status: Int, body: Data, contentType: String, closeAfter: Bool) {
        let reason = Self.reasonPhrase(status)
        var head = "HTTP/1.1 \(status) \(reason)\r\n"
        head += "Content-Type: \(contentType)\r\n"
        head += "Content-Length: \(body.count)\r\n"
        if closeAfter { head += "Connection: close\r\n" }
        head += "\r\n"
        var out = Data(head.utf8)
        out.append(body)
        connection.send(content: out, completion: .contentProcessed { _ in
            if closeAfter { connection.cancel() }
        })
    }

    // MARK: - WebSocket

    private func completeWebSocketUpgrade(connection: NWConnection, headers: [String: String]) {
        let expected = "Bearer \(expectedToken)"
        guard headers["authorization"] == expected else {
            writeHTTP(connection: connection, status: 401, body: Data(),
                      contentType: "text/plain", closeAfter: true)
            return
        }
        guard let key = headers["sec-websocket-key"] else {
            writeHTTP(connection: connection, status: 400, body: Data(),
                      contentType: "text/plain", closeAfter: true)
            return
        }
        let accept = Self.wsAccept(key: key)
        var head = "HTTP/1.1 101 Switching Protocols\r\n"
        head += "Upgrade: websocket\r\n"
        head += "Connection: Upgrade\r\n"
        head += "Sec-WebSocket-Accept: \(accept)\r\n\r\n"
        connection.send(content: Data(head.utf8), completion: .contentProcessed { [weak self] err in
            guard err == nil, let self else { connection.cancel(); return }
            self.wsLock.lock()
            self.wsConnections.append(connection)
            self.wsLock.unlock()
            // Start reading frames so we don't block the peer; we ignore
            // incoming frames in the tests we run today.
            self.drainWSFrames(connection: connection)
        })
    }

    private func drainWSFrames(connection: NWConnection) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] _, _, isEOF, _ in
            guard let self else { return }
            if isEOF {
                self.wsLock.lock()
                self.wsConnections.removeAll { $0 === connection }
                self.wsLock.unlock()
                connection.cancel()
                return
            }
            self.drainWSFrames(connection: connection)
        }
    }

    var hasWebSocketClient: Bool {
        wsLock.lock()
        defer { wsLock.unlock() }
        return !wsConnections.isEmpty
    }

    /// Broadcasts a text frame to every connected WebSocket client.
    func sendToAllWebSockets(text: String) {
        let frame = Self.encodeTextFrame(text)
        wsLock.lock()
        let conns = wsConnections
        wsLock.unlock()
        for c in conns {
            c.send(content: frame, completion: .contentProcessed { _ in })
        }
    }

    // MARK: - Helpers

    private static func indexOf(sequence: Data, in haystack: Data) -> Int? {
        guard haystack.count >= sequence.count else { return nil }
        for i in 0...(haystack.count - sequence.count) {
            if haystack.subdata(in: i..<(i + sequence.count)) == sequence { return i }
        }
        return nil
    }

    private static func reasonPhrase(_ status: Int) -> String {
        switch status {
        case 200: return "OK"
        case 201: return "Created"
        case 204: return "No Content"
        case 400: return "Bad Request"
        case 401: return "Unauthorized"
        case 404: return "Not Found"
        case 500: return "Internal Server Error"
        default: return "Status"
        }
    }

    /// Sec-WebSocket-Accept = base64(sha1(key + magic)).
    private static func wsAccept(key: String) -> String {
        let magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
        let combined = Data((key + magic).utf8)
        return sha1Base64(combined)
    }

    private static func sha1Base64(_ data: Data) -> String {
        var hash = [UInt8](repeating: 0, count: 20)
        data.withUnsafeBytes { raw in
            _ = CC_SHA1(raw.baseAddress, CC_LONG(data.count), &hash)
        }
        return Data(hash).base64EncodedString()
    }

    /// Encodes a single unmasked server-to-client text frame.
    private static func encodeTextFrame(_ text: String) -> Data {
        let payload = Data(text.utf8)
        var frame = Data()
        frame.append(0x81) // FIN=1, opcode=0x1 (text)
        if payload.count < 126 {
            frame.append(UInt8(payload.count))
        } else if payload.count <= UInt16.max {
            frame.append(126)
            var n = UInt16(payload.count).bigEndian
            withUnsafeBytes(of: &n) { frame.append(contentsOf: $0) }
        } else {
            frame.append(127)
            var n = UInt64(payload.count).bigEndian
            withUnsafeBytes(of: &n) { frame.append(contentsOf: $0) }
        }
        frame.append(payload)
        return frame
    }
}

