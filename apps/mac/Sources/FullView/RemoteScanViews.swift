import SwiftUI

/// Remote mode: the steps of the server job of a scan.
struct RemoteJobStrip: View {
    let job: DiskScanJob

    struct Step: Equatable {
        enum Mark { case done, pending, failed }
        let mark: Mark
        let label: String
        let value: String
    }

    var body: some View {
        HStack(spacing: 10) {
            Text("Via server").fontWeight(.bold).foregroundStyle(UsagePalette.infoStrong)
            let steps = Self.steps(job)
            ForEach(Array(steps.enumerated()), id: \.offset) { i, s in
                HStack(spacing: 6) {
                    Text(symbol(s.mark)).foregroundStyle(color(s.mark))
                    Text(s.label).foregroundStyle(s.mark == .pending ? UsagePalette.infoMuted : UsagePalette.infoFG)
                    if !s.value.isEmpty {
                        Text(s.value).font(.system(size: 13, design: .monospaced)).foregroundStyle(UsagePalette.infoMuted)
                    }
                    if i < steps.count - 1 {
                        Text("→").foregroundStyle(UsagePalette.infoArrow)
                    }
                }
            }
            Spacer(minLength: 8)
            (Text("Needs ") + Text("--allow-disk-scan").font(.system(size: 13, design: .monospaced)) + Text(" on the sampler"))
                .foregroundStyle(UsagePalette.infoMuted)
                .lineLimit(1)
        }
        .font(.system(size: 13))
        .lineLimit(1)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .background(UsagePalette.infoBG, in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(UsagePalette.infoBorder))
    }

    private func symbol(_ m: Step.Mark) -> String {
        switch m {
        case .done: return "✓"
        case .pending: return "○"
        case .failed: return "✕"
        }
    }

    private func color(_ m: Step.Mark) -> Color {
        switch m {
        case .done: return UsagePalette.okGreen
        case .pending: return UsagePalette.infoMuted
        case .failed: return UsagePalette.dangerFG
        }
    }

    /// Queued → Sampler picked up → Scanned → Result in. A failed,
    /// cancelled or expired job ends with that state.
    static func steps(_ j: DiskScanJob) -> [Step] {
        var out = [Step(mark: .done, label: "Queued", value: j.createdTs.map(time) ?? "")]
        if let s = j.startedTs {
            out.append(Step(mark: .done, label: "Sampler picked up", value: time(s)))
        } else if !j.state.isFinal {
            out.append(Step(mark: .pending, label: "Sampler picked up", value: ""))
        }
        let ms: Int64? = {
            if let s = j.startedTs, let f = j.finishedTs { return Int64(f.timeIntervalSince(s) * 1000) }
            return j.progress?.elapsedMs
        }()
        let took = ms.map(UsageText.duration) ?? ""
        switch j.state {
        case .queued, .running:
            out.append(Step(mark: .pending, label: "Scanned", value: took))
            out.append(Step(mark: .pending, label: "Result in", value: ""))
        case .done:
            out.append(Step(mark: .done, label: "Scanned", value: took))
            out.append(Step(mark: .done, label: "Result in", value: size(j.resultBytes)))
        case .failed:
            out.append(Step(mark: .failed, label: "Failed", value: j.finishedTs.map(time) ?? ""))
        case .cancelled:
            out.append(Step(mark: .failed, label: "Cancelled", value: j.finishedTs.map(time) ?? ""))
        case .expired:
            out.append(Step(mark: .failed, label: "Expired", value: j.finishedTs.map(time) ?? ""))
        }
        return out
    }

    /// One decimal below 10 MB, as a result is often 1 to 3 MB.
    static func size(_ b: Int64) -> String {
        (1_000_000..<10_000_000).contains(b) ? String(format: "%.1f MB", Double(b) / 1e6) : DiskSize.text(b)
    }

    static func time(_ d: Date) -> String {
        d.formatted(Date.FormatStyle().hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).second(.twoDigits))
    }
}

/// Remote mode: tells why the server does not scan the node, and how to
/// turn scans on.
struct ScanOptInPanel: View {
    /// The `allowDiskScan` flag of the node.
    let nodeAllows: Bool
    /// The message of the server or sampler, if any.
    let message: String?
    let onAllow: (() async throws -> Void)?
    let onScan: () -> Void

    @State private var saving = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(nodeAllows ? "Disk scans are off on this host" : "Disk scans are off for this node")
                .font(.system(size: 18, weight: .bold))
            Text("A disk scan sends the names and sizes of the folders of the host to the server. Thus the server runs a scan only when you allow it in two places. The scan does not change files.")
                .font(.system(size: 13)).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let message, !message.isEmpty {
                Text(message)
                    .font(.system(size: 13)).foregroundStyle(UsagePalette.warnFG)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            step(1, done: nodeAllows, title: "Allow disk scans for this node",
                 text: "This sets “allowDiskScan” on the node in the server. You must be an admin of the server.") {
                if !nodeAllows, let onAllow {
                    Button(saving ? "Saving…" : "Allow disk scans") {
                        saving = true
                        error = nil
                        Task {
                            do { try await onAllow() } catch { self.error = error.localizedDescription }
                            saving = false
                        }
                    }
                    .buttonStyle(UsagePrimaryButtonStyle())
                    .disabled(saving)
                }
            }
            step(2, done: false, title: "Run the sampler with --allow-disk-scan",
                 text: "Add the flag to the command that starts the sampler on the host, then restart the sampler.") {
                Text("towertail-sampler service install --allow-disk-scan …")
                    .font(.system(size: 12, design: .monospaced))
                    .textSelection(.enabled)
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .background(UsagePalette.well, in: RoundedRectangle(cornerRadius: 6))
            }
            if let error {
                Text(error).font(.system(size: 13)).foregroundStyle(UsagePalette.dangerFG)
                    .textSelection(.enabled)
            }
            if nodeAllows {
                Button("Scan again", action: onScan).buttonStyle(UsagePrimaryButtonStyle())
            }
        }
        .padding(4)
    }

    private func step<Accessory: View>(_ n: Int, done: Bool, title: String, text: String,
                                       @ViewBuilder accessory: () -> Accessory) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text(done ? "✓" : "\(n)")
                .font(.system(size: 12, weight: .bold))
                .foregroundStyle(done ? UsagePalette.okGreen : .primary)
                .frame(width: 22, height: 22)
                .background(UsagePalette.well, in: Circle())
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.system(size: 14, weight: .semibold))
                Text(text).font(.system(size: 13)).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                accessory()
            }
        }
    }
}
