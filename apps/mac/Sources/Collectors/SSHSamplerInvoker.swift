import Foundation
import NIO
import Citadel

/// One open SSH connection that can run a command. The seam lets tests
/// replace Citadel with a fake that hangs.
protocol SamplerSession: Sendable {
    func run(_ command: String) async throws -> Data
    func close() async
}

/// Invokes `towertail-sampler --once` on a remote host over SSH via Citadel.
/// No shell-out — connect, exec, collect stdout, decode.
struct SSHSamplerInvoker: SamplerInvoker {
    static let remoteSamplerPath = "~/.towertail/towertail-sampler"
    /// Deadline for connect plus `--once`. Without it a hung host blocks
    /// the pacer forever and the card keeps its last state.
    static let timeout: TimeInterval = 20

    typealias Connect = @Sendable (Node) async throws -> any SamplerSession

    private let connect: Connect
    private let timeout: TimeInterval

    /// `hostKeyPrompt` is nil in unit tests: with no pinned fingerprint the
    /// connect then refuses. `onTrust` writes an accepted fingerprint back
    /// onto the Node.
    init(
        hostKeyPrompt: HostKeyPrompt? = nil,
        onTrust: (@Sendable (UUID, String) -> Void)? = nil,
        timeout: TimeInterval = SSHSamplerInvoker.timeout
    ) {
        self.timeout = timeout
        self.connect = { node in
            let nodeId = node.id
            let client = try await SSHConnectionFactory.connect(
                node: node,
                hostKeyPrompt: hostKeyPrompt,
                onTrust: onTrust.map { cb in { (fp: String) in cb(nodeId, fp) } }
            )
            return CitadelSession(client: client)
        }
    }

    init(timeout: TimeInterval, connect: @escaping Connect) {
        self.timeout = timeout
        self.connect = connect
    }

    func invokeOnce(node: Node) async throws -> Sample {
        guard node.kind == .ssh else {
            throw SamplerInvokeError.misconfigured("SSHSamplerInvoker called for non-ssh node")
        }
        guard node.sshUser?.isEmpty == false, node.sshHost?.isEmpty == false else {
            throw SamplerInvokeError.misconfigured("SSH node missing user or host")
        }

        let slot = SessionSlot()
        let connect = self.connect
        let command = "\(Self.remoteSamplerPath) --once"
        // Unstructured on purpose: a hung connect must not block the
        // caller. It finishes in the background and the slot closes it.
        let work = Task.detached { () throws -> Sample in
            let session = try await connect(node)
            guard await slot.store(session) else {
                await session.close()
                throw SamplerInvokeError.timeout
            }
            let result: Result<Sample, Error>
            do {
                let data = try await session.run(command)
                guard !data.isEmpty else { throw SamplerInvokeError.emptyOutput }
                result = .success(try SampleCodec.decode(data))
            } catch {
                result = .failure(Self.map(error))
            }
            if let s = await slot.take() { await s.close() }
            return try result.get()
        }

        let timeout = self.timeout
        let race = OneShot<Sample>()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { cont in
                race.install(cont)
                Task.detached {
                    do { race.resume(.success(try await work.value)) }
                    catch {
                        let expired = await slot.isExpired
                        race.resume(.failure(expired ? SamplerInvokeError.timeout : error))
                    }
                }
                Task.detached {
                    try? await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
                    guard race.resume(.failure(SamplerInvokeError.timeout)) else { return }
                    // Closing the client makes a hung `run` throw.
                    if let s = await slot.expire() { await s.close() }
                }
            }
        } onCancel: {
            work.cancel()
            race.resume(.failure(CancellationError()))
        }
    }

    /// Errors of the command; connect errors pass through unchanged.
    private static func map(_ error: Error) -> Error {
        switch error {
        case is SamplerInvokeError: return error
        case let e as DecodingError: return SamplerInvokeError.decodeFailed(underlying: e)
        default: return SamplerInvokeError.sshFailed(stderr: SSHErrorRenderer.describe(error), exitCode: -1)
        }
    }
}

/// Holds the session between the work task and the deadline. Whoever
/// takes it closes it, so the client closes exactly one time.
private actor SessionSlot {
    private var session: (any SamplerSession)?
    private(set) var isExpired = false

    /// False when the deadline already expired: the caller closes.
    func store(_ s: any SamplerSession) -> Bool {
        if isExpired { return false }
        session = s
        return true
    }

    func take() -> (any SamplerSession)? {
        defer { session = nil }
        return session
    }

    func expire() -> (any SamplerSession)? {
        isExpired = true
        return take()
    }
}

/// Resumes a continuation one time; later results are dropped.
private final class OneShot<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var cont: CheckedContinuation<T, Error>?
    private var pending: Result<T, Error>?
    private var done = false

    func install(_ c: CheckedContinuation<T, Error>) {
        lock.lock()
        if let pending {
            lock.unlock()
            c.resume(with: pending)
            return
        }
        cont = c
        lock.unlock()
    }

    @discardableResult
    func resume(_ r: Result<T, Error>) -> Bool {
        lock.lock()
        guard !done else { lock.unlock(); return false }
        done = true
        let c = cont
        cont = nil
        if c == nil { pending = r }
        lock.unlock()
        c?.resume(with: r)
        return true
    }
}

private struct CitadelSession: SamplerSession {
    let client: SSHClient

    func run(_ command: String) async throws -> Data {
        Data(buffer: try await client.executeCommand(command))
    }

    /// Detached so a canceled pacer still tears the channel down.
    func close() async {
        let client = self.client
        await Task.detached { try? await client.close() }.value
    }
}
