import SwiftUI
import AppKit

/// "Need help setting up SSH keys?" sheet — copy-pasteable commands only, the
/// app never executes anything. Detects whether `~/.ssh/id_ed25519` already
/// exists and skips the generation step when it does. Steps use `ssh-copy-id`
/// on macOS for the pubkey install.
struct SshKeySetupSheet: View {
    let user: String
    let host: String
    let onDone: () -> Void

    private var hasEd25519: Bool {
        let home = FileManager.default.homeDirectoryForCurrentUser
        return FileManager.default.fileExists(
            atPath: home.appendingPathComponent(".ssh/id_ed25519").path
        )
    }

    private var userAtHost: String {
        let u = user.trimmingCharacters(in: .whitespaces)
        let h = host.trimmingCharacters(in: .whitespaces)
        if u.isEmpty || h.isEmpty { return "USER@HOST" }
        return "\(u)@\(h)"
    }

    private var steps: [(label: String, command: String)] {
        var out: [(String, String)] = []
        if !hasEd25519 {
            out.append((
                "Generate a new ed25519 key (press Enter to accept defaults).",
                "ssh-keygen -t ed25519"
            ))
        }
        out.append((
            "Copy the public key to the remote host's authorized_keys.",
            "ssh-copy-id -i ~/.ssh/id_ed25519.pub \(userAtHost)"
        ))
        out.append((
            "Verify: this should log in without a password prompt.",
            "ssh \(userAtHost) \"echo ok\""
        ))
        return out
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("SSH key setup")
                .font(.headline)

            Text(hasEd25519
                 ? "You already have an ed25519 key. Use the command below to install it on the remote host."
                 : "Towertail couldn't find an ed25519 key. Run these commands in Terminal — Towertail will pick up the key automatically next time it connects.")
                .fixedSize(horizontal: false, vertical: true)

            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(Array(steps.enumerated()), id: \.offset) { idx, step in
                        stepView(number: idx + 1, label: step.label, command: step.command)
                    }
                    if hasEd25519 {
                        Text("If that host still rejects the key, verify the user has write access to ~/.ssh and that sshd_config allows pubkey auth.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .padding(.vertical, 4)
            }
            .frame(minHeight: 240)

            HStack {
                Spacer()
                Button("Done", action: onDone)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 560)
    }

    @ViewBuilder
    private func stepView(number: Int, label: String, command: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Step \(number). \(label)")
                .font(.subheadline.weight(.semibold))
            HStack(spacing: 8) {
                Text(command)
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(
                        RoundedRectangle(cornerRadius: 5)
                            .fill(Color(nsColor: .textBackgroundColor))
                    )
                    .overlay(
                        RoundedRectangle(cornerRadius: 5)
                            .stroke(Color.secondary.opacity(0.25), lineWidth: 1)
                    )
                Button("Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(command, forType: .string)
                }
            }
        }
    }
}
