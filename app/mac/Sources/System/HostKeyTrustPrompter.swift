import Foundation
import AppKit

/// Serializes first-connect host-key trust prompts. Multiple collector
/// workers can race to present a sheet at the same time after a bulk import;
/// this actor funnels them through a single NSAlert.
actor HostKeyTrustPrompter {
    static let shared = HostKeyTrustPrompter()

    /// Prompt the user about trusting `fingerprint` for `host`. Returns true
    /// iff they clicked Trust. Serialized inside the actor so concurrent
    /// callers see one sheet at a time rather than a stack.
    func prompt(host: String, fingerprint: String) async -> Bool {
        await MainActor.run {
            let alert = NSAlert()
            alert.alertStyle = .informational
            alert.messageText = "Trust host \(host)?"
            alert.informativeText =
                "First time connecting. The server presented key fingerprint:\n\n\(fingerprint)\n\nTrust this key?"
            alert.addButton(withTitle: "Trust")
            alert.addButton(withTitle: "Cancel")
            return alert.runModal() == .alertFirstButtonReturn
        }
    }

    /// Sendable adapter for the Citadel `HostKeyPrompt` shape. Curry with
    /// `Self.shared` to get a closure suitable for handing into
    /// `SSHConnectionFactory.connect`.
    static var sharedPromptAdapter: HostKeyPrompt {
        { @Sendable host, fp in
            await HostKeyTrustPrompter.shared.prompt(host: host, fingerprint: fp)
        }
    }
}
