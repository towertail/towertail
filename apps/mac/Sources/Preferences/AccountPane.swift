import SwiftUI

struct AccountPane: View {
    @Environment(CloudSession.self) private var cloud: CloudSession?

    var body: some View {
        Form {
            if let account = cloud?.account {
                AccountSection(account: account)
            } else {
                Text("Cloud is not available in this mode.")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }
}

struct AccountSection: View {
    let account: CloudAccount
    @Environment(\.openURL) private var openURL
    @State private var email = UserDefaults.standard.string(forKey: CloudAccount.emailKey) ?? ""
    @State private var busy = false
    @State private var attempt = 0

    var body: some View {
        if case .signedIn(let me) = account.status {
            signedIn(me)
        } else {
            signedOut
        }
    }

    private func signedIn(_ me: CloudMe) -> some View {
        Section("Towertail Cloud") {
            LabeledContent("Email", value: me.user.email)
            LabeledContent("Organization", value: me.org.name)
            LabeledContent("Plan", value: Self.planText(me: me, billing: account.billing))
            if let b = account.billing {
                LabeledContent("Hosts", value: "\(b.nodes) of \(b.plan.maxNodes)")
            }
            HStack {
                Button("Open dashboard") { openURL(account.dashboardURL) }
                Button("Sign out") { account.signOut() }
            }
        }
    }

    @ViewBuilder
    private var signedOut: some View {
        Section("Sign in to Towertail Cloud") {
            Text("Cloud keeps your hosts under watch while this Mac is asleep.")
                .foregroundStyle(.secondary)
            HStack {
                TextField("Email", text: $email)
                    .textContentType(.emailAddress)
                    .onSubmit(sendCode)
                Button("Email me a code", action: sendCode)
                    .disabled(busy || email.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            if let pending = account.pendingEmail {
                Text("We sent a 6-digit code to \(pending).")
                    .foregroundStyle(.secondary)
                CodeEntryView { code in
                    Task { await verify(code) }
                }
                .id(attempt)
                .disabled(busy)
                resend
            }
            if case .failed(let message) = account.status {
                Text(message).foregroundStyle(ThresholdTint.critical.color)
            }
        }
    }

    private var resend: some View {
        TimelineView(.periodic(from: .now, by: 1)) { _ in
            let wait = account.resendWait
            Button(wait > 0 ? "Send a new code (\(Self.clock(wait)))" : "Send a new code") {
                sendCode()
            }
            .disabled(wait > 0 || busy)
            .buttonStyle(.link)
        }
    }

    private func sendCode() {
        guard !busy else { return }
        busy = true
        Task {
            await account.sendCode(email: email)
            busy = false
        }
    }

    private func verify(_ code: String) async {
        busy = true
        await account.verify(code: code)
        busy = false
        if case .failed = account.status { attempt += 1 }
    }

    static func clock(_ seconds: Int) -> String {
        String(format: "%d:%02d", seconds / 60, seconds % 60)
    }

    static func planText(me: CloudMe, billing: CloudBilling?) -> String {
        guard let plan = billing?.plan else { return "\(me.org.plan.capitalized) plan" }
        return "\(plan.id.capitalized) plan, \(plan.maxNodes) hosts"
    }
}
