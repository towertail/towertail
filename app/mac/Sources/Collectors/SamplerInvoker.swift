import Foundation

protocol SamplerInvoker: Sendable {
    func invokeOnce(node: Node) async throws -> Sample
}

enum SamplerInvokeError: LocalizedError {
    case binaryMissing
    case sshFailed(stderr: String, exitCode: Int32)
    case decodeFailed(underlying: Error)
    case timeout
    case emptyOutput
    case misconfigured(String)

    var errorDescription: String? {
        switch self {
        case .binaryMissing:
            return "Bundled sampler binary not found."
        case .sshFailed(let stderr, let code):
            let trimmed = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            return "ssh exit \(code): \(trimmed.isEmpty ? "(no stderr)" : trimmed)"
        case .decodeFailed(let err):
            return "decode failed: \(err.localizedDescription)"
        case .timeout:
            return "sampler timed out"
        case .emptyOutput:
            return "sampler produced no output"
        case .misconfigured(let reason):
            return reason
        }
    }

    /// Short one-line summary for logs and UI tooltips. Works on any
    /// Error (not just this enum) so callers don't have to cast before
    /// emitting a diagnostic string.
    static func shortDescription(for error: Error) -> String {
        if let e = error as? SamplerInvokeError {
            return e.errorDescription ?? "\(e)"
        }
        return error.localizedDescription
    }
}

@Sendable
func makeInvoker(for node: Node) -> SamplerInvoker {
    switch node.kind {
    case .local:
        return LocalSamplerInvoker()
    case .ssh:
        return SSHSamplerInvoker(
            hostKeyPrompt: HostKeyTrustPrompter.sharedPromptAdapter,
            onTrust: { id, fp in
                Task { @MainActor in HostKeyTrustPersister.persist(nodeId: id, fingerprint: fp) }
            }
        )
    }
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
                throw SamplerInvokeError.timeout
            }
            if let result { return result }
            throw SamplerInvokeError.timeout
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
