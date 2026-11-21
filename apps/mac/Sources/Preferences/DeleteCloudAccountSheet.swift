import SwiftUI

/// Shows what an account delete does to each org, then asks for the email.
struct DeleteCloudAccountSheet: View {
    let exit: CloudExit
    @Environment(\.dismiss) private var dismiss
    @State private var typed = ""
    @State private var started = false

    private var deletion: CloudAccountDeletion? { exit.deletion }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Delete Cloud account").font(.title3.weight(.semibold))
            if let deletion {
                Text("The delete removes your Cloud account and its history. The app moves your hosts back to SSH first.")
                    .foregroundStyle(.secondary)
                orgList(deletion)
                Text("Cloud blocks the delete when you are the only owner of a shared org, or when an org has an active subscription.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                confirmField(deletion)
            } else if case .failed(let message) = exit.phase {
                Text(message).foregroundStyle(ThresholdTint.critical.color)
            } else {
                ProgressView().controlSize(.small)
            }
            if started, case .failed(let message) = exit.phase, deletion != nil {
                Text(message).foregroundStyle(ThresholdTint.critical.color)
            }
            buttons
        }
        .padding(20)
        .frame(width: 460)
        .task {
            exit.reset()
            await exit.loadDeletion()
        }
        .onChange(of: exit.phase) { _, phase in
            if phase == .done { dismiss() }
        }
    }

    private func orgList(_ deletion: CloudAccountDeletion) -> some View {
        VStack(spacing: 0) {
            ForEach(deletion.orgs) { org in
                if org.id != deletion.orgs.first?.id { Divider() }
                HStack(alignment: .top, spacing: 10) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(org.name).font(.system(size: 13, weight: .medium))
                        Text(Self.detail(org))
                            .font(.system(size: 11))
                            .foregroundStyle(org.fate == "blocked" ? ThresholdTint.critical.color : Color.secondary)
                    }
                    Spacer()
                    Text(Self.badge(org))
                        .font(.system(size: 11, weight: .medium))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Capsule().fill(Self.badgeColor(org).opacity(0.15)))
                        .foregroundStyle(Self.badgeColor(org))
                }
                .padding(.vertical, 8)
                .padding(.horizontal, 12)
            }
        }
        .background(RoundedRectangle(cornerRadius: 8).fill(Color("Card/Background")))
    }

    private func confirmField(_ deletion: CloudAccountDeletion) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Type \(deletion.email) to confirm").font(.callout)
            TextField("Email", text: $typed)
                .textFieldStyle(.roundedBorder)
                .textContentType(.emailAddress)
                .disabled(exit.isBusy)
        }
    }

    private var buttons: some View {
        HStack {
            if exit.isBusy {
                ProgressView().controlSize(.small)
                Text(exit.phase == .moving ? "The app moves the hosts back to SSH." : "The app deletes the account.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button("Cancel", role: .cancel) { dismiss() }
                .keyboardShortcut(.cancelAction)
                .disabled(exit.isBusy)
            Button("Delete account", role: .destructive) {
                started = true
                Task { await exit.deleteAccount(email: typed.trimmingCharacters(in: .whitespaces)) }
            }
            .buttonStyle(.borderedProminent)
            .tint(ThresholdTint.critical.color)
            .disabled(!Self.canDelete(deletion, typed: typed, busy: exit.isBusy))
        }
    }

    static func canDelete(_ deletion: CloudAccountDeletion?, typed: String, busy: Bool) -> Bool {
        guard let deletion, !deletion.blocked, !busy else { return false }
        return typed.trimmingCharacters(in: .whitespaces).caseInsensitiveCompare(deletion.email) == .orderedSame
    }

    static func detail(_ org: CloudOrgDeletion) -> String {
        switch org.fate {
        case "delete": return org.nodes == 1 ? "Deleted with 1 host" : "Deleted with \(org.nodes) hosts"
        case "leave": return "You leave. The other members stay."
        default: return org.reason ?? "Cloud blocks the delete."
        }
    }

    static func badge(_ org: CloudOrgDeletion) -> String {
        switch org.fate {
        case "delete": return "Deleted"
        case "leave": return "You leave"
        default: return "Blocked"
        }
    }

    private static func badgeColor(_ org: CloudOrgDeletion) -> Color {
        org.fate == "leave" ? Color.secondary : ThresholdTint.critical.color
    }
}
