import SwiftUI

struct WelcomeSignInView: View {
    let flow: WelcomeFlow
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @State private var busy = false
    @State private var attempt = 0

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 22) {
                WelcomeHeading(
                    title: "Enter your code",
                    subtitle: "We sent a 6-digit code to \(cloud?.account.pendingEmail ?? flow.email)."
                )
                CodeEntryView { code in
                    Task { await verify(code) }
                }
                .id(attempt)
                .disabled(busy)
                if case .failed(let message) = cloud?.account.status {
                    Text(message).font(.system(size: 12)).foregroundStyle(ThresholdTint.critical.color)
                }
                HStack(spacing: 16) {
                    resend
                    Button("Use a different email", action: flow.back).buttonStyle(.link)
                }
                .font(.system(size: 12))
                Spacer()
            }
            .padding(32)
            WelcomeFooter(step: .signIn, secondary: .init(title: "Back", run: flow.back))
        }
    }

    private var resend: some View {
        TimelineView(.periodic(from: .now, by: 1)) { _ in
            let wait = cloud?.account.resendWait ?? 0
            Button(wait > 0 ? "Send a new code (\(AccountSection.clock(wait)))" : "Send a new code") {
                Task { await flow.sendCode() }
            }
            .disabled(wait > 0 || busy)
            .buttonStyle(.link)
        }
    }

    private func verify(_ code: String) async {
        busy = true
        await flow.verify(code: code)
        busy = false
        if case .failed = cloud?.account.status { attempt += 1 }
    }
}
