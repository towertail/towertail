import SwiftUI

/// Host name, health flag and facts line at the top of the workbench.
struct WorkbenchHeader: View {
    let vm: ServerViewModel
    let score: HealthScore
    @Binding var hostId: UUID
    @Environment(ServerStore.self) private var store

    var body: some View {
        HStack(spacing: 12) {
            Circle().fill(dotColor).frame(width: 9, height: 9)
            hostPicker
            flag
            Text(facts)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(WB.faint)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 8)
            if vm.liveSource == .cloud {
                Text("cloud host · rules from the cloud")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(WB.faint)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .background(WB.sidebar)
    }

    private var hostPicker: some View {
        Menu {
            ForEach(store.serverVMs) { other in
                Button {
                    if other.id != hostId { hostId = other.id }
                } label: {
                    Label(other.hostname, systemImage: other.id == hostId ? "checkmark" : "")
                }
            }
        } label: {
            HStack(spacing: 4) {
                Text(vm.hostname).font(.title3.bold())
                Image(systemName: "chevron.down").font(.caption).foregroundStyle(.secondary)
            }
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help("Switch server")
    }

    private var flag: some View {
        let tint = score.tint
        let color = tint == .nominal ? WB.cpu : tint.color
        var text = "HEALTH \(Int(score.score.rounded()))"
        if tint != .nominal, let worst = score.worst { text += " · " + worst.label.uppercased() }
        return Text(text)
            .font(.system(size: 11, weight: .semibold, design: .monospaced))
            .tracking(0.6)
            .foregroundStyle(color)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .overlay(RoundedRectangle(cornerRadius: 4).stroke(color, lineWidth: 1))
            .lineLimit(1)
            .fixedSize()
    }

    private var dotColor: Color {
        switch vm.state {
        case .online: return WB.cpu
        case .warn: return ThresholdTint.warn.color
        case .critical: return ThresholdTint.critical.color
        case .unknown, .offline, .suspended: return WB.faint
        }
    }

    /// `os/arch · kernel · cores · memory · up · sampler`.
    var facts: String {
        var parts: [String] = []
        if !vm.osArch.isEmpty { parts.append(vm.osArch) }
        if let k = vm.kernel { parts.append("kernel \(k)") }
        if let c = vm.cpuCores { parts.append("\(c) cores") }
        if let m = vm.memTotalBytes {
            parts.append(ByteCountFormatter.string(fromByteCount: m, countStyle: .memory))
        }
        if let u = vm.uptimeS { parts.append("up \(ShortDuration.long(TimeInterval(u)))") }
        if !vm.samplerVersion.isEmpty { parts.append("sampler \(vm.samplerVersion)") }
        return parts.joined(separator: " · ")
    }
}
