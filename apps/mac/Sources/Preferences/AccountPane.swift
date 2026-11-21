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
    @Environment(CloudExit.self) private var exit: CloudExit?
    @Environment(NodeStore.self) private var nodeStore
    @State private var email = UserDefaults.standard.string(forKey: CloudAccount.emailKey) ?? ""
    @State private var busy = false
    @State private var attempt = 0
    @State private var confirmSignOut = false
    @State private var leaving = false
    @State private var showFailure = false
    @State private var showDelete = false

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
                LabeledContent("Cloud hosts", value: "\(b.nodes) of \(b.maxNodes ?? b.plan.maxNodes)")
            }
            LabeledContent("SSH hosts", value: "\(nodeStore.nodes.filter { $0.kind == .ssh && $0.cloud == nil }.count) (no limit)")
            HStack {
                Button("Open dashboard") { Task { await account.openDashboard(using: { openURL($0) }) } }
                Button("Sign out…") { confirmSignOut = true }
                    .disabled(exit?.isBusy == true)
                if let exit, exit.isBusy {
                    ProgressView().controlSize(.small)
                    Text(Self.exitText(exit.phase, hosts: exit.linkedHosts.count))
                        .foregroundStyle(.secondary)
                }
            }
            if exit != nil {
                Button("Delete Cloud account…", role: .destructive) { showDelete = true }
                    .buttonStyle(.link)
                    .disabled(exit?.isBusy == true)
            }
        }
        .confirmationDialog("Sign out of Towertail Cloud?", isPresented: $confirmSignOut) {
            Button("Move hosts back and sign out") { signOut() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(Self.signOutMessage(hosts: exit?.linkedHosts.count ?? 0, cloudOnly: exit?.cloudOnlyHosts.count ?? 0))
        }
        .alert("Some hosts did not move back", isPresented: $showFailure) {
            Button("Try again") { signOut() }
            Button("Sign out anyway", role: .destructive) { signOutAnyway() }
            Button("Cancel", role: .cancel) { finishLeaving() }
        } message: {
            Text(Self.failureText(exit?.phase))
        }
        .onChange(of: exit?.phase) { _, phase in
            if leaving, case .failed = phase { showFailure = true }
        }
        .sheet(isPresented: $showDelete) {
            if let exit { DeleteCloudAccountSheet(exit: exit) }
        }
    }

    private func signOut() {
        guard let exit else { return account.signOut() }
        leaving = true
        Task {
            await exit.leave()
            if exit.phase == .done { finishLeaving() }
        }
    }

    private func signOutAnyway() {
        account.signOut()
        finishLeaving()
    }

    private func finishLeaving() {
        leaving = false
        exit?.reset()
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

    static func signOutMessage(hosts: Int, cloudOnly: Int) -> String {
        var lines: [String] = []
        if hosts > 0 {
            lines.append("\(count(hosts)) now \(hosts == 1 ? "reports" : "report") to Cloud. The app removes \(hosts == 1 ? "its sampler" : "their samplers") and deletes \(hosts == 1 ? "it" : "them") from Cloud. This Mac then watches \(hosts == 1 ? "it" : "them") over SSH again.")
            lines.append("Cloud alerts for \(hosts == 1 ? "this host" : "these hosts") stop.")
        } else {
            lines.append("No host on this Mac reports to Cloud.")
        }
        if cloudOnly > 0 {
            let one = cloudOnly == 1
            lines.append("\(count(cloudOnly)) \(one ? "exists" : "exist") only in Cloud. \(one ? "It stays" : "They stay") in Cloud and \(one ? "leaves" : "leave") this Mac.")
        }
        return lines.joined(separator: " ")
    }

    static func exitText(_ phase: CloudExit.Phase, hosts: Int) -> String {
        switch phase {
        case .idle: return ""
        case .moving: return hosts == 1 ? "The app moves 1 host back to SSH." : "The app moves \(hosts) hosts back to SSH."
        case .deleting: return "The app deletes the Cloud account."
        case .done: return "Done."
        case .failed(let message): return message
        }
    }

    private static func failureText(_ phase: CloudExit.Phase?) -> String {
        guard case .failed(let message) = phase else { return "" }
        return message + " If you sign out anyway, these hosts stay in Cloud. Sign in again to link them."
    }

    private static func count(_ hosts: Int) -> String { hosts == 1 ? "1 host" : "\(hosts) hosts" }

    static func planText(me: CloudMe, billing: CloudBilling?) -> String {
        guard let plan = billing?.plan else { return "\(me.org.plan.capitalized) plan" }
        return "\(plan.id.capitalized) plan, \(plan.maxNodes) Cloud hosts"
    }
}
