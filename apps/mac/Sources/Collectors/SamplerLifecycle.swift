import Foundation

/// Makes the Cloud node and the sampler token. `RemoteClient` is the real one.
protocol Enroller: Sendable {
    func enroll(nodeID: UUID, displayName: String, machineID: String?) async throws -> Enrollment
}

/// The reply of `POST /v1/sampler/enroll`.
struct Enrollment: Decodable, Sendable, Equatable {
    let nodeId: UUID
    let samplerToken: String
    let endpoint: String

    enum CodingKeys: String, CodingKey {
        case nodeId = "node_id"
        case samplerToken = "sampler_token"
        case endpoint
    }
}

/// The JSON of `towertail-sampler status`.
struct SamplerStatus: Codable, Equatable, Sendable {
    let running: Bool
    let pid: Int?
    let version: String?
    let endpoint: String?
    let lastPush: Date?
    let lastStatus: Int?
    let lastError: String?
    let reboot: String?
    let update: String?
    let interval: String?
    let pausedUntil: Date?
    let gone: String?

    enum CodingKeys: String, CodingKey {
        case running, pid, version, endpoint
        case lastPush = "last_push"
        case lastStatus = "last_status"
        case lastError = "last_error"
        case reboot, update, interval
        case pausedUntil = "paused_until"
        case gone
    }
}

/// The columns of the install table.
enum InstallStep: String, CaseIterable, Sendable {
    case copy, register, start, reboot, sample

    var title: String {
        switch self {
        case .copy: return "Copy"
        case .register: return "Register"
        case .start: return "Start"
        case .reboot: return "Reboot"
        case .sample: return "Sample"
        }
    }
}

enum InstallStepState: Equatable, Sendable {
    case waiting
    case running
    case done(String?)
    case failed(String)
}

enum LifecycleError: LocalizedError, Equatable {
    case planFull(max: Int?)
    case notSignedIn
    case startFailed(String)
    case statusUnreadable(String)
    case noBootMethod
    case unsupportedHost(String)

    var errorDescription: String? {
        switch self {
        case .planFull(let max):
            return max.map { "The plan is full (\($0) of \($0))." } ?? "The plan is full."
        case .notSignedIn:
            return "Sign in to Towertail Cloud first."
        case .startFailed(let detail):
            return detail.isEmpty ? "The sampler did not start." : "The sampler did not start: \(detail)"
        case .statusUnreadable:
            return "The sampler status is not readable."
        case .noBootMethod:
            return "No way to start the sampler after a reboot."
        case .unsupportedHost:
            return "Not supported on this host"
        }
    }
}

/// The remote commands that install and run a sampler. It needs only a
/// session, so tests need no SSH.
struct SamplerLifecycle: Sendable {
    /// Copies the binary and returns its remote path. It wraps `SSHBootstrap.copyBinary`.
    typealias Upload = @Sendable (URL) async throws -> String

    let session: any SamplerSession
    let enroller: (any Enroller)?
    let upload: Upload

    private var bin: String { SSHBootstrap.remoteSamplerPath }
    private static let tokenInPath = ".towertail/sampler.token.in"

    init(session: any SamplerSession, enroller: (any Enroller)?, upload: @escaping Upload) {
        self.session = session
        self.enroller = enroller
        self.upload = upload
    }

    /// The sampler triple of the host, for example `linux-arm64`.
    func triple() async throws -> String {
        let out: Data
        do {
            out = try await session.run("uname -sm")
        } catch is RemoteCommandFailed {
            throw LifecycleError.unsupportedHost("no uname")
        }
        let text = String(decoding: out, as: UTF8.self)
        do {
            return try SSHBootstrap.triple(fromUname: text)
        } catch {
            throw LifecycleError.unsupportedHost(text.trimmingCharacters(in: .whitespacesAndNewlines))
        }
    }

    /// The copy step. A sampler that runs is stopped, so that the new binary replaces it.
    func install(binary: URL) async throws -> String {
        _ = try? await session.run("if [ -x \(bin) ]; then \(bin) stop; fi")
        return try await upload(binary)
    }

    func enroll(node: Node) async throws -> Enrollment {
        guard let enroller else { throw LifecycleError.notSignedIn }
        let machine = await machineID()
        do {
            return try await enroller.enroll(nodeID: node.id, displayName: node.displayName, machineID: machine)
        } catch RemoteClient.RemoteError.http(402, let body) {
            throw LifecycleError.planFull(max: Self.planMax(in: body))
        }
    }

    /// The machine id that the sampler reports in `--once`. Nil when it has none.
    func machineID() async -> String? {
        guard let data = try? await session.run("\(bin) --once"),
              let sample = try? SampleCodec.decode(data),
              let id = sample.host.machineID, !id.isEmpty
        else { return nil }
        return id
    }

    /// Writes the token as a file, so that it is never in argv.
    func start(enrollment: Enrollment) async throws -> String {
        let home = try await SamplerLifecycle.home(session)
        _ = try await session.run("mkdir -p ~/.towertail")
        try await session.put(
            Data((enrollment.samplerToken + "\n").utf8),
            path: "\(home)/\(Self.tokenInPath)",
            mode: 0o600
        )
        let endpoint = DiskScanRequest.shellQuote(enrollment.endpoint)
        let file = "~/\(Self.tokenInPath)"
        return try await startCommand(
            "\(bin) start --endpoint \(endpoint) --token-file - --boot none < \(file); rc=$?; rm -f \(file); exit $rc"
        )
    }

    /// Starts the sampler with the saved config.
    func restart() async throws -> String {
        try await startCommand("\(bin) start")
    }

    /// Sets the reboot entry and returns the mode that works.
    func enableReboot() async throws -> String {
        struct Reply: Decodable { let reboot: String }
        let out: Data
        do {
            out = try await session.run("\(bin) boot enable --mode auto")
        } catch is RemoteCommandFailed {
            throw LifecycleError.noBootMethod
        }
        guard let reply = try? JSONDecoder().decode(Reply.self, from: out) else {
            throw LifecycleError.noBootMethod
        }
        return reply.reboot
    }

    func status() async throws -> SamplerStatus {
        let out = try await session.run("\(bin) status")
        do {
            return try SampleCodec.decoder().decode(SamplerStatus.self, from: out)
        } catch {
            throw LifecycleError.statusUnreadable(String(decoding: out, as: UTF8.self))
        }
    }

    func update(binary: URL) async throws {
        _ = try? await session.run("\(bin) stop")
        _ = try await upload(binary)
        _ = try await restart()
    }

    func remove() async throws {
        _ = try? await session.run("\(bin) stop")
        do {
            _ = try await session.run("\(bin) boot disable")
        } catch let e as RemoteCommandFailed where e.exitCode == 127 {
            // No sampler on the host: no reboot entry to remove.
        }
        _ = try await session.run("rm -f ~/.towertail/sampler.token ~/.towertail/sampler.json")
    }

    private func startCommand(_ command: String) async throws -> String {
        let out: Data
        do {
            out = try await session.run(command)
        } catch let e as RemoteCommandFailed {
            throw LifecycleError.startFailed(e.stderr)
        }
        return String(decoding: out, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// SFTP needs the absolute home. It does not expand `~`.
    private static func home(_ session: any SamplerSession) async throws -> String {
        let out = try await session.run("echo $HOME")
        let home = String(decoding: out, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !home.isEmpty else { throw LifecycleError.startFailed("The home folder is empty.") }
        return home
    }

    /// The limit in "the plan allows 3 hosts".
    private static func planMax(in body: String) -> Int? {
        guard let range = body.range(of: #"\d+"#, options: .regularExpression) else { return nil }
        return Int(body[range])
    }
}
