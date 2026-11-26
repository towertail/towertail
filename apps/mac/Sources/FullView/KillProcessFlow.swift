import SwiftUI

enum KillResult: Equatable {
    case success(pid: Int32, name: String)
    case failure(message: String)
}

/// Dispatches SIGKILL against a PID either locally via /bin/kill or
/// through the same SSH path the sampler uses. Shared module-level helper
/// rather than a method on ProcessTable so the table stays focused on
/// display and this logic can be tested / reused.
enum ProcessKiller {
    enum Outcome {
        case success
        case failure(message: String)
    }

    static func kill(pid: Int32, node: Node) async -> Outcome {
        switch node.kind {
        case .local:
            return await run("/bin/kill", ["-9", String(pid)], label: "kill")
        case .ssh:
            guard let user = node.sshUser, !user.isEmpty,
                  let host = node.sshHost, !host.isEmpty else {
                return .failure(message: "SSH node missing user or host")
            }
            return await run("/usr/bin/ssh", [
                "-o", "BatchMode=yes",
                "-o", "ConnectTimeout=5",
                "-o", "StrictHostKeyChecking=accept-new",
                "\(user)@\(host)",
                "kill -9 \(pid)"
            ], label: "ssh")
        case .cloud:
            return .failure(message: "The app cannot stop processes on a host that reports to Cloud.")
        }
    }

    /// Shell preview. It shows the exact command, with the remote prefix.
    static func preview(pid: Int32, node: Node?) -> String {
        guard let node else { return "kill -9 \(pid)" }
        switch node.kind {
        case .local:
            return "kill -9 \(pid)"
        case .ssh:
            return "ssh \(node.sshUser ?? "?")@\(node.sshHost ?? "?") 'kill -9 \(pid)'"
        case .cloud:
            return "Not available for a host that reports to Cloud."
        }
    }

    private static func run(_ executable: String, _ arguments: [String], label: String) async -> Outcome {
        do {
            let result = try await ProcessRunner.run(
                executable: URL(fileURLWithPath: executable),
                arguments: arguments
            )
            if result.exitCode == 0 { return .success }
            let err = String(data: result.stderr, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return .failure(message: err.isEmpty
                ? "\(label) exited \(result.exitCode)"
                : "\(label) exited \(result.exitCode): \(err)")
        } catch {
            return .failure(message: SamplerInvokeError.shortDescription(for: error))
        }
    }
}

/// The process that the kill flow asks about.
struct KillTarget: Equatable, Sendable {
    let pid: Int32
    let name: String
}

/// Dialog and result alert of a kill. Shared by every view that kills a process.
private struct ProcessKillFlow: ViewModifier {
    let node: Node?
    @Binding var target: KillTarget?
    /// The alert needs an OK, so the user sees stderr.
    @State private var result: KillResult?
    @State private var inFlight = false

    func body(content: Content) -> some View {
        content
            .confirmationDialog(
                target.map { "Kill \($0.name)?" } ?? "Kill process",
                isPresented: Binding(get: { target != nil }, set: { if !$0 { target = nil } }),
                titleVisibility: .visible,
                presenting: target
            ) { t in
                Button(role: .destructive) {
                    run(t)
                } label: {
                    Text(verbatim: "Kill \(t.pid)")
                }
                .disabled(inFlight || node == nil)
                Button("Cancel", role: .cancel) { }
            } message: { t in
                Text("This will run:\n\n\(ProcessKiller.preview(pid: t.pid, node: node))\n\nThe process will be terminated immediately with SIGKILL.")
            }
            .alert(
                alertTitle,
                isPresented: Binding(get: { result != nil }, set: { if !$0 { result = nil } }),
                presenting: result
            ) { _ in
                Button("OK", role: .cancel) { result = nil }
            } message: { r in
                switch r {
                case .success(let pid, let name):
                    Text(verbatim: "Sent SIGKILL to \(name) (PID \(pid)).")
                case .failure(let message):
                    Text(message)
                }
            }
    }

    private var alertTitle: String {
        switch result {
        case .success: return "Process killed"
        case .failure: return "Kill failed"
        case nil: return ""
        }
    }

    private func run(_ t: KillTarget) {
        guard let node else { return }
        inFlight = true
        Task {
            let outcome = await ProcessKiller.kill(pid: t.pid, node: node)
            await MainActor.run {
                inFlight = false
                target = nil
                switch outcome {
                case .success: result = .success(pid: t.pid, name: t.name)
                case .failure(let message): result = .failure(message: message)
                }
            }
        }
    }
}

extension View {
    func processKillFlow(node: Node?, target: Binding<KillTarget?>) -> some View {
        modifier(ProcessKillFlow(node: node, target: target))
    }
}
