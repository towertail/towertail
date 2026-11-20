import Foundation
@testable import Towertail

/// Records commands and `put` calls. Rules give the output of a command
/// that contains a text. The newest rule wins.
final class FakeSamplerSession: SamplerSession, @unchecked Sendable {
    struct Put: Equatable {
        let path: String
        let data: Data
        let mode: Int
    }

    private let lock = NSLock()
    private var rules: [(String, Result<String, Error>)] = []
    private var _commands: [String] = []
    private var _puts: [Put] = []
    private var _closed = false

    var commands: [String] { lock.withLock { _commands } }
    var puts: [Put] { lock.withLock { _puts } }
    var closed: Bool { lock.withLock { _closed } }

    init() {
        reply("echo $HOME", "/home/u\n")
        reply("uname -sm", "Linux x86_64\n")
        reply("sampler start", "started, pid 7\n")
        reply("boot enable", "{\"reboot\":\"linger\"}\n")
    }

    func reply(_ fragment: String, _ output: String) {
        lock.withLock { rules.insert((fragment, .success(output)), at: 0) }
    }

    func fail(_ fragment: String, _ error: Error) {
        lock.withLock { rules.insert((fragment, .failure(error)), at: 0) }
    }

    func run(_ command: String) async throws -> Data {
        let rule: Result<String, Error>? = lock.withLock {
            _commands.append(command)
            return rules.first { command.contains($0.0) }?.1
        }
        switch rule {
        case .success(let text): return Data(text.utf8)
        case .failure(let error): throw error
        case nil: return Data()
        }
    }

    func put(_ data: Data, path: String, mode: Int) async throws {
        lock.withLock { _puts.append(Put(path: path, data: data, mode: mode)) }
    }

    func close() async {
        lock.withLock { _closed = true }
    }
}

/// A clock whose sleep ends at once and moves its time forward.
final class ImmediateClock: Clock, @unchecked Sendable {
    struct Instant: InstantProtocol {
        var offset: Duration
        func advanced(by duration: Duration) -> Instant { Instant(offset: offset + duration) }
        func duration(to other: Instant) -> Duration { other.offset - offset }
        static func < (a: Instant, b: Instant) -> Bool { a.offset < b.offset }
    }

    private let lock = NSLock()
    private var elapsed = Duration.zero

    var now: Instant { lock.withLock { Instant(offset: elapsed) } }
    var minimumResolution: Duration { .zero }

    func sleep(until deadline: Instant, tolerance: Duration?) async throws {
        lock.withLock { elapsed = max(elapsed, deadline.offset) }
        await Task.yield()
    }
}
