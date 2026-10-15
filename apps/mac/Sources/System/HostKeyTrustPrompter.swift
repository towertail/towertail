import Foundation

/// Auto-trust on first connect (TOFU). The accepted fingerprint is still
/// persisted onto the Node via `onTrust`, so a subsequent key change is
/// caught as a `HostKeyMismatch` and refused — but the initial connect
/// goes through without a sheet.
enum HostKeyTrustPrompter {
    /// Sendable adapter for the Citadel `HostKeyPrompt` shape. Always
    /// returns true; the connect path will record the fingerprint and
    /// pin it for future connects.
    static var sharedPromptAdapter: HostKeyPrompt {
        { @Sendable _, _ in true }
    }
}
