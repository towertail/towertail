import Foundation

protocol AgentInvoker: Sendable {
    func invokeOnce(node: Node) async throws -> Sample
}

enum AgentInvokeError: LocalizedError {
    case binaryMissing
    case sshFailed(stderr: String, exitCode: Int32)
    case decodeFailed(underlying: Error)
    case timeout
    case emptyOutput
    case misconfigured(String)

    var errorDescription: String? {
        switch self {
        case .binaryMissing:
            return "Bundled agent binary not found."
        case .sshFailed(let stderr, let code):
            let trimmed = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            return "ssh exit \(code): \(trimmed.isEmpty ? "(no stderr)" : trimmed)"
        case .decodeFailed(let err):
            return "decode failed: \(err.localizedDescription)"
        case .timeout:
            return "agent timed out"
        case .emptyOutput:
            return "agent produced no output"
        case .misconfigured(let reason):
            return reason
        }
    }
}

@Sendable
func makeInvoker(for node: Node) -> AgentInvoker {
    node.kind == .local ? LocalAgentInvoker() : SSHAgentInvoker()
}

enum ProcessRunner {
    static let defaultTimeout: TimeInterval = 10

    static func run(
        executable: URL,
        arguments: [String],
        timeout: TimeInterval = defaultTimeout
    ) async throws -> (stdout: Data, stderr: Data, exitCode: Int32) {
        try await withThrowingTaskGroup(of: (stdout: Data, stderr: Data, exitCode: Int32)?.self) { group in
            group.addTask {
                try Self.spawnAndWait(executable: executable, arguments: arguments)
            }
            group.addTask {
                try await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
                return nil
            }
            defer { group.cancelAll() }
            guard let result = try await group.next() else {
                throw AgentInvokeError.timeout
            }
            if let result { return result }
            throw AgentInvokeError.timeout
        }
    }

    private static func spawnAndWait(
        executable: URL,
        arguments: [String]
    ) throws -> (stdout: Data, stderr: Data, exitCode: Int32) {
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments

        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        process.standardOutput = stdoutPipe
        process.standardError = stderrPipe
        process.standardInput = FileHandle.nullDevice

        try process.run()
        let stdoutData = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
        let stderrData = stderrPipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        return (stdoutData, stderrData, process.terminationStatus)
    }
}
