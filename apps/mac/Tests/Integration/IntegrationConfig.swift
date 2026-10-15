import Foundation

/// Shared configuration for integration tests that talk to a real
/// `towertail-server` booted via docker-compose.test.yaml. Tests skip
/// gracefully when the flag file isn't present so `xcodebuild test`
/// without the compose stack still passes.
///
/// Activation: the `scripts/tests.sh --swift-integration` path writes
/// `/tmp/towertail-integration-test.env` before running xcodebuild.
/// We use a file on disk (not env vars) because xcodebuild doesn't
/// reliably forward arbitrary env vars to the macOS test host — the
/// `-only-testing` run was skipping on env alone.
enum IntegrationConfig {
    private static let flagFile = "/tmp/towertail-integration-test.env"

    private static var loaded: [String: String] = {
        var out: [String: String] = [:]
        guard let text = try? String(contentsOfFile: flagFile, encoding: .utf8) else {
            return out
        }
        for line in text.split(separator: "\n") {
            if let eq = line.firstIndex(of: "=") {
                let k = String(line[..<eq])
                let v = String(line[line.index(after: eq)...])
                out[k] = v
            }
        }
        return out
    }()

    static var enabled: Bool {
        // Env var OR flag file — env var stays handy for `xcodebuild
        // test` invoked directly from shell.
        ProcessInfo.processInfo.environment["TOWERTAIL_INTEGRATION"] == "1"
            || loaded["TOWERTAIL_INTEGRATION"] == "1"
    }

    static var endpoint: URL {
        let s = ProcessInfo.processInfo.environment["TOWERTAIL_ENDPOINT"]
            ?? loaded["TOWERTAIL_ENDPOINT"]
            ?? "http://127.0.0.1:18080"
        return URL(string: s)!
    }

    /// Admin token installed via TT_AUTH__BOOTSTRAP_TOKEN in the compose
    /// file. Tests use this to enroll samplers and create nodes.
    static var adminToken: String {
        ProcessInfo.processInfo.environment["TOWERTAIL_ADMIN_TOKEN"]
            ?? loaded["TOWERTAIL_ADMIN_TOKEN"]
            ?? "tt-test-admin-token-0000000000000000"
    }
}
