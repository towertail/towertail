import Foundation

struct LocalAgentInvoker: AgentInvoker {
    static let relativeBundledPath = "agents/\(LocalAgentInvoker.hostTripleDefault)/towertail-agent"

    static var hostTripleDefault: String {
        #if arch(arm64)
        return "darwin-arm64"
        #else
        return "darwin-amd64"
        #endif
    }

    static func bundledBinaryURL() -> URL? {
        let triple = hostTripleDefault
        if let url = Bundle.main.url(
            forResource: "towertail-agent",
            withExtension: nil,
            subdirectory: "agents/\(triple)"
        ) {
            return url
        }
        if let resourceURL = Bundle.main.resourceURL {
            let candidate = resourceURL
                .appendingPathComponent("agents", isDirectory: true)
                .appendingPathComponent(triple, isDirectory: true)
                .appendingPathComponent("towertail-agent")
            if FileManager.default.fileExists(atPath: candidate.path) {
                return candidate
            }
        }
        return nil
    }

    static func developmentFallbackURL() -> URL? {
        let triple = hostTripleDefault
        let fm = FileManager.default
        var url = URL(fileURLWithPath: fm.currentDirectoryPath)
        for _ in 0..<8 {
            let candidate = url
                .appendingPathComponent("dist/agents/\(triple)/towertail-agent")
            if fm.fileExists(atPath: candidate.path) {
                return candidate
            }
            let parent = url.deletingLastPathComponent()
            if parent.path == url.path { break }
            url = parent
        }
        return nil
    }

    static func resolveBinary() -> URL? {
        bundledBinaryURL() ?? developmentFallbackURL()
    }

    func invokeOnce(node: Node) async throws -> Sample {
        guard let binary = Self.resolveBinary() else {
            throw AgentInvokeError.binaryMissing
        }
        let result = try await ProcessRunner.run(executable: binary, arguments: ["--once"])
        if result.exitCode != 0 {
            let err = String(data: result.stderr, encoding: .utf8) ?? ""
            throw AgentInvokeError.sshFailed(stderr: err, exitCode: result.exitCode)
        }
        guard !result.stdout.isEmpty else {
            throw AgentInvokeError.emptyOutput
        }
        do {
            return try SampleCodec.decoder().decode(Sample.self, from: result.stdout)
        } catch {
            throw AgentInvokeError.decodeFailed(underlying: error)
        }
    }
}
