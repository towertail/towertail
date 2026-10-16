import AppKit
import SwiftUI

/// How the scan reaches the host. Changes the texts only.
enum ScanVia: Equatable {
    case local
    case ssh(user: String)
    case server
}

/// Usage sub-tab of DISK: scan a folder, see a treemap, pick cleanup items.
struct DiskUsageView<Leading: View>: View {
    @Bindable var model: DiskUsageModel
    let vm: ServerViewModel
    let via: ScanVia
    /// "~" and the mounts of the host.
    let scopeChoices: [String]
    /// The sub-tab picker, at the start of the toolbar row.
    @ViewBuilder var leading: () -> Leading

    var body: some View {
        VStack(spacing: 14) {
            toolbar
            if showFDABanner { fdaBanner }
            stats
            HStack(alignment: .top, spacing: 16) {
                leftCard
                CleanupPanel(model: model, hostLabel: vm.hostname, isLocal: via == .local)
            }
            .frame(maxHeight: .infinity)
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 6)
    }

    // MARK: Toolbar

    private var toolbar: some View {
        HStack(spacing: 12) {
            leading()
            scopeMenu
            Spacer()
            statusText
            if model.isRunning {
                Button("Cancel scan") { model.cancel() }
                    .buttonStyle(UsageDangerButtonStyle())
            } else {
                Button(model.record == nil ? "Scan" : "Rescan") { model.start() }
                    .buttonStyle(UsagePrimaryButtonStyle())
            }
        }
        .frame(height: 36)
    }

    private var scopeMenu: some View {
        Menu {
            ForEach(scopeChoices, id: \.self) { s in
                Button {
                    model.setScope(s)
                } label: {
                    Label(scopeLabel(s), systemImage: s == model.scope ? "checkmark" : "")
                }
            }
        } label: {
            Text("Scope  ").foregroundColor(.secondary)
                + Text(scopeLabel(model.scope)).font(.system(size: 13, design: .monospaced))
        }
        .menuStyle(.button)
        .fixedSize()
        .disabled(model.isRunning)
        .help("The folder to scan")
    }

    private func scopeLabel(_ s: String) -> String {
        guard s == "~" else { return s }
        if let user = model.result?.user, !user.isEmpty { return "~ (\(user))" }
        if case .ssh(let user) = via, !user.isEmpty { return "~ (\(user))" }
        if via == .local { return "~ (\(NSUserName()))" }
        return "~"
    }

    @ViewBuilder
    private var statusText: some View {
        Group {
            switch model.phase {
            case .running:
                TimelineView(.periodic(from: .now, by: 1)) { ctx in
                    Text("Scanning… \(UsageText.clock(ctx.date.timeIntervalSince(model.startedAt ?? ctx.date)))")
                }
            case .failed:
                Text("Scan failed").foregroundStyle(UsagePalette.dangerFG)
            default:
                if let rec = model.record {
                    Text("Scanned \(UsageText.ago(rec.finishedAt)) · \(DiskSize.count(rec.result.files)) files · \(UsageText.duration(rec.durationMs))")
                } else {
                    Text("Not scanned yet")
                }
            }
        }
        .font(.system(size: 13))
        .foregroundStyle(.secondary)
        .monospacedDigit()
    }

    // MARK: Full Disk Access

    private var showFDABanner: Bool {
        via == .local && model.scope == "~" && model.result != nil && !(model.isRunning && !model.showPrevious)
            && !FullDiskAccess.isGranted
    }

    private var fdaBanner: some View {
        let size = model.result?.noAccess.bytesEst.map { " (\(DiskSize.text($0)))" } ?? ""
        return HStack(spacing: 12) {
            Text("Full Disk Access is off.").fontWeight(.bold)
            Text("Towertail could not read Mail, Messages and Safari data\(size). The map shows it as “No access”.")
                .foregroundStyle(UsagePalette.warnText)
                .lineLimit(2)
            Spacer(minLength: 0)
            Button("Open Privacy Settings") { FullDiskAccess.openSettings() }
        }
        .font(.system(size: 13))
        .foregroundStyle(UsagePalette.warnFG)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .background(UsagePalette.warnBG, in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(UsagePalette.warnBorder))
    }

    // MARK: Stats

    private struct Stat: Identifiable {
        var label: String
        var dot: Color
        var value: String
        var sub: String
        var id: String { label }
    }

    private var statItems: [Stat] {
        let gray = Color(.sRGB, red: 142 / 255, green: 142 / 255, blue: 147 / 255)
        let orange = Color(.sRGB, red: 1, green: 159 / 255, blue: 10 / 255)
        let blue = DiskCategory.cache.color(1)
        if model.isRunning && !model.showPrevious {
            let p = model.progress
            return [
                Stat(label: "Scanned so far", dot: gray, value: p.map { DiskSize.text($0.bytes) } ?? "—",
                     sub: p.map { "\(DiskSize.count($0.files)) files" } ?? "starting"),
                Stat(label: "Selected", dot: TreemapView.selectedGreen, value: "—", sub: "after scan"),
                Stat(label: "No access", dot: orange, value: p.map { "\($0.noAccessDirs)" } ?? "—", sub: "folders so far"),
                diskFree(blue),
            ]
        }
        guard let r = model.result else {
            return [
                Stat(label: "Scanned", dot: gray, value: "—", sub: ""),
                Stat(label: "Selected", dot: TreemapView.selectedGreen, value: "—", sub: ""),
                Stat(label: "No access", dot: orange, value: "—", sub: ""),
                diskFree(blue),
            ]
        }
        let noAccess: Stat = {
            if let est = r.noAccess.bytesEst, est > 0 {
                return Stat(label: "No access", dot: orange, value: DiskSize.text(est), sub: dirsText(r.noAccess))
            }
            let sub = showFDABanner ? "needs Full Disk Access" : (r.noAccess.count == 1 ? "folder" : "folders")
            return Stat(label: "No access", dot: orange, value: "\(r.noAccess.count)", sub: sub)
        }()
        var fourth = diskFree(blue)
        if model.scope == "~", let fs = r.fs, !r.isWindows {
            let rest = max(0, fs.used - r.scannedBytes - (r.noAccess.bytesEst ?? 0))
            fourth = Stat(label: "Not scanned", dot: blue, value: DiskSize.text(rest), sub: "outside ~ · system & apps")
        }
        let n = model.selectedRecs.count
        return [
            Stat(label: "Scanned", dot: gray, value: DiskSize.text(r.scannedBytes), sub: "\(DiskSize.count(r.files)) files"),
            Stat(label: "Selected", dot: TreemapView.selectedGreen, value: DiskSize.text(model.selectedBytes),
                 sub: "\(n) \(n == 1 ? "item" : "items")"),
            noAccess,
            fourth,
        ]
    }

    private func dirsText(_ na: DiskScanNoAccess) -> String {
        let names = na.dirs.prefix(2).joined(separator: ", ")
        let more = Int(na.count) - min(2, na.dirs.count)
        return more > 0 ? "\(names) +\(more)" : names
    }

    private func diskFree(_ dot: Color) -> Stat {
        if let fs = model.result?.fs {
            return Stat(label: "Disk free", dot: dot, value: DiskSize.text(fs.free),
                        sub: "of \(DiskSize.text(fs.total)) · \(fs.mount)")
        }
        let root = model.scope == "~" ? (model.result?.home ?? "/") : model.scope
        let disk = vm.latestDisks
            .filter { root.hasPrefix($0.mount) }
            .max { $0.mount.count < $1.mount.count }
        guard let d = disk else { return Stat(label: "Disk free", dot: dot, value: "—", sub: "") }
        return Stat(label: "Disk free", dot: dot, value: DiskSize.text(max(0, d.total - d.used)),
                    sub: "of \(DiskSize.text(d.total)) · \(d.mount)")
    }

    private var stats: some View {
        HStack(spacing: 12) {
            ForEach(statItems) { s in
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 6) {
                        Circle().fill(s.dot).frame(width: 7, height: 7)
                        Text(s.label)
                    }
                    .font(.system(size: 12)).foregroundStyle(.secondary)
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(s.value).font(.system(size: 26, weight: .bold, design: .monospaced))
                        Text(s.sub).font(.system(size: 13, design: .monospaced)).foregroundStyle(.secondary)
                            .lineLimit(1).minimumScaleFactor(0.75).truncationMode(.middle)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16).padding(.vertical, 12)
                .background(UsagePalette.card, in: RoundedRectangle(cornerRadius: 10))
                .overlay(RoundedRectangle(cornerRadius: 10).stroke(UsagePalette.border))
            }
        }
    }

    // MARK: Left card

    private var leftCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            if model.isRunning && !model.showPrevious {
                ScanningPanel(model: model, host: vm.hostname, via: via, scopeLabel: scopeLabel(model.scope))
            } else if case .failed(let msg) = model.phase {
                message(title: "The scan failed", text: msg, danger: true)
            } else if model.result != nil {
                explorer
            } else {
                message(title: "No scan yet",
                        text: "Scan \(scopeLabel(model.scope)) to see what uses the space. The sampler reads only file sizes, with low priority. The result stays on this Mac.",
                        danger: false)
            }
        }
        .padding(.horizontal, 16).padding(.vertical, 14)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(UsagePalette.card, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(UsagePalette.border))
    }

    private func message(title: String, text: String, danger: Bool) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(.system(size: 18, weight: .bold))
            Text(text)
                .font(.system(size: 13))
                .foregroundStyle(danger ? UsagePalette.dangerFG : .secondary)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                Button(danger ? "Try again" : "Scan \(scopeLabel(model.scope))") { model.start() }
                    .buttonStyle(UsagePrimaryButtonStyle())
                if danger, model.record != nil {
                    Button("Show last result") { model.dismissFailure() }
                }
            }
        }
        .padding(4)
    }

    private var explorer: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Text("Explorer").font(.system(size: 15, weight: .bold))
                Text("Click a folder to open it.").font(.system(size: 13)).foregroundStyle(.secondary)
                Spacer()
                if model.isRunning {
                    Button("Show progress") { model.showPrevious = false }
                        .controlSize(.small)
                }
                breadcrumb
                Text(DiskSize.text(model.mapTotal))
                    .font(.system(size: 13, design: .monospaced)).foregroundStyle(.secondary)
                    .padding(.leading, 8)
            }
            .frame(height: 26)
            TreemapView(nodes: model.mapNodes, selectedIDs: model.selectedNodeIDs) { i in
                let nodes = model.mapNodes
                if nodes.indices.contains(i) { model.open(nodes[i].name) }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            ScrollView(.horizontal, showsIndicators: false) { legend }
        }
    }

    private var breadcrumb: some View {
        HStack(spacing: 6) {
            Button(scopeLabel(model.scope)) { model.goUp(to: 0) }
                .font(.system(size: 12, design: .monospaced))
                .controlSize(.small)
            ForEach(Array(model.focus.enumerated()), id: \.offset) { i, name in
                Text("›").foregroundStyle(.secondary)
                if i == model.focus.count - 1 {
                    Text(name).font(.system(size: 12, design: .monospaced)).lineLimit(1)
                } else {
                    Button(name) { model.goUp(to: i + 1) }
                        .buttonStyle(.link)
                        .font(.system(size: 12, design: .monospaced))
                }
            }
        }
    }

    private var legend: some View {
        let used = usedCategories
        return HStack(spacing: 16) {
            ForEach(DiskCategory.allCases.filter(used.contains), id: \.self) { c in
                HStack(spacing: 6) {
                    swatch(c)
                    Text(c.label)
                }
                .fixedSize()
            }
            HStack(spacing: 6) {
                Canvas { ctx, size in
                    let r = CGRect(origin: .zero, size: size).insetBy(dx: 0.5, dy: 0.5)
                    let shape = Path(roundedRect: r, cornerRadius: 3)
                    TreemapView.stripes(r, shape: shape, angle: 135, a: TreemapView.selectedGreen.opacity(0.6),
                                        b: TreemapView.selectedGreen.opacity(0.15), band: 3, in: &ctx)
                    ctx.stroke(shape, with: .color(TreemapView.selectedGreen), lineWidth: 1)
                }
                .frame(width: 10, height: 10)
                Text("Selected for cleanup")
            }
            .fixedSize()
        }
        .font(.system(size: 12))
        .foregroundStyle(.secondary)
        .padding(.top, 4)
    }

    private func swatch(_ c: DiskCategory) -> some View {
        RoundedRectangle(cornerRadius: 3)
            .fill(c.color(c == .none ? 0.2 : 0.45))
            .overlay(RoundedRectangle(cornerRadius: 3).stroke(c.color(0.6)))
            .frame(width: 10, height: 10)
    }

    private var usedCategories: Set<DiskCategory> {
        var out: Set<DiskCategory> = []
        for n in model.mapNodes {
            out.insert(n.cat)
            for c in n.children { out.insert(c.cat) }
        }
        return out
    }
}

/// Left card while a scan runs.
private struct ScanningPanel: View {
    let model: DiskUsageModel
    let host: String
    let via: ScanVia
    let scopeLabel: String

    var body: some View {
        let p = model.progress
        VStack(alignment: .leading, spacing: 18) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("Scanning \(scopeLabel) on \(via == .local ? "this Mac" : host)")
                    .font(.system(size: 18, weight: .bold))
                Text(subtitle).font(.system(size: 13)).foregroundStyle(.secondary)
            }
            bar(p)
            HStack(alignment: .top, spacing: 12) {
                number(p.map { DiskSize.text($0.bytes) } ?? "—", usedText(p))
                number(p.map { $0.files.formatted(.number) } ?? "—", "files read")
                number(p.map { "\($0.noAccessDirs)" } ?? "—", "folders with no access")
                TimelineView(.periodic(from: .now, by: 1)) { ctx in
                    number(UsageText.clock(ctx.date.timeIntervalSince(model.startedAt ?? ctx.date)), "elapsed")
                }
            }
            if let cur = p?.current, !cur.isEmpty {
                (Text("Now reading ") + Text(cur).font(.system(size: 13, design: .monospaced)).foregroundColor(.primary))
                    .font(.system(size: 13)).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
            }
            if let top = p?.top, !top.isEmpty {
                largest(top)
            }
            Spacer(minLength: 0)
            Divider()
            Text(footnote).font(.system(size: 12)).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 4).padding(.vertical, 8)
    }

    private var subtitle: String {
        switch via {
        case .local: return "low priority · read only"
        case .ssh(let user): return "over SSH as \(user) · nice 10 · ionice idle"
        case .server: return "through the server"
        }
    }

    private var footnote: String {
        switch via {
        case .local:
            return "The sampler runs on this Mac and reads only file sizes. Cancel stops the walk. The app keeps the last 3 results in its local database."
        case .ssh:
            return "The sampler streams progress lines over the SSH channel. Cancel closes the channel and stops the walk. The app keeps the last result in its local database. The host keeps nothing."
        case .server:
            return "The server sends the job to the sampler on the host. Cancel asks the server to stop the job. The app keeps the last result in its local database."
        }
    }

    private func usedText(_ p: DiskScanProgress?) -> String {
        guard let p, p.usedBytes > 0 else { return "read" }
        let pct = Int((Double(p.bytes) / Double(p.usedBytes) * 100).rounded())
        return "of ~\(DiskSize.text(p.usedBytes)) used (\(min(pct, 100))%)"
    }

    private func bar(_ p: DiskScanProgress?) -> some View {
        GeometryReader { geo in
            let frac: Double = {
                guard let p, p.usedBytes > 0 else { return 1 }
                return min(0.99, Double(p.bytes) / Double(p.usedBytes))
            }()
            ZStack(alignment: .leading) {
                Capsule().fill(UsagePalette.track)
                Canvas { ctx, size in
                    let r = CGRect(origin: .zero, size: size)
                    let shape = Path(r)
                    TreemapView.stripes(r, shape: shape, angle: 135, a: TreemapView.selectedGreen,
                                        b: Color(.sRGB, red: 47 / 255, green: 174 / 255, blue: 79 / 255),
                                        band: 7, in: &ctx)
                }
                .frame(width: geo.size.width * frac)
                .opacity(p?.usedBytes ?? 0 > 0 ? 1 : 0.45)
                .clipShape(Capsule())
            }
        }
        .frame(height: 10)
    }

    private func number(_ value: String, _ caption: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(value).font(.system(size: 20, weight: .bold, design: .monospaced)).foregroundStyle(.primary)
            Text(caption).font(.system(size: 13)).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func largest(_ top: [DiskScanTop]) -> some View {
        let maxB = Double(top.map(\.b).max() ?? 1)
        return VStack(alignment: .leading, spacing: 10) {
            Text("Largest so far").font(.system(size: 13, weight: .semibold)).foregroundStyle(.secondary)
            ForEach(Array(top.prefix(5).enumerated()), id: \.offset) { _, t in
                HStack(spacing: 12) {
                    Text(t.path).font(.system(size: 13, design: .monospaced))
                        .lineLimit(1).truncationMode(.middle)
                        .frame(width: 220, alignment: .leading)
                    GeometryReader { geo in
                        ZStack(alignment: .leading) {
                            Capsule().fill(UsagePalette.track)
                            Capsule().fill(DiskCategory.cache.color(1))
                                .frame(width: geo.size.width * Double(t.b) / max(maxB, 1))
                        }
                    }
                    .frame(height: 8)
                    Text(DiskSize.text(t.b)).font(.system(size: 13, design: .monospaced))
                        .frame(width: 90, alignment: .trailing)
                }
            }
        }
        .padding(.top, 6)
    }
}

/// Texts of the Usage sub-tab.
enum UsageText {
    static func ago(_ d: Date, now: Date = Date()) -> String {
        let s = Int(max(0, now.timeIntervalSince(d)))
        if s < 60 { return "just now" }
        if s < 3600 { return "\(s / 60) min ago" }
        if s < 86_400 { return "\(s / 3600) h ago" }
        let days = s / 86_400
        return days == 1 ? "1 day ago" : "\(days) days ago"
    }

    /// "41 s", "1 m 12 s".
    static func duration(_ ms: Int64) -> String {
        let s = Int((Double(ms) / 1000).rounded())
        return s < 60 ? "\(s) s" : "\(s / 60) m \(s % 60) s"
    }

    /// "0:37", "12:05".
    static func clock(_ t: TimeInterval) -> String {
        let s = Int(max(0, t))
        return String(format: "%d:%02d", s / 60, s % 60)
    }
}

/// Colors of the Usage sub-tab. Dark values follow the approved mockup.
enum UsagePalette {
    static func dyn(light: NSColor, dark: NSColor) -> Color {
        Color(nsColor: NSColor(name: nil) { a in
            a.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light
        })
    }

    static func hex(_ v: UInt32, _ alpha: CGFloat = 1) -> NSColor {
        NSColor(srgbRed: CGFloat((v >> 16) & 0xFF) / 255, green: CGFloat((v >> 8) & 0xFF) / 255,
                blue: CGFloat(v & 0xFF) / 255, alpha: alpha)
    }

    static let card = dyn(light: hex(0xF6F6F8), dark: hex(0x242426))
    static let border = dyn(light: hex(0xDCDCE0), dark: hex(0x38383A))
    static let well = dyn(light: hex(0xEDEDF0), dark: hex(0x1F1F21))
    static let footer = dyn(light: hex(0xEFEFF2), dark: hex(0x212123))
    static let script = dyn(light: hex(0xFFFFFF), dark: hex(0x161618))
    static let scriptText = dyn(light: hex(0x2C2C2E), dark: hex(0xD6D6D8))
    static let rowLine = dyn(light: hex(0xE4E4E8), dark: hex(0x2F2F31))
    static let track = dyn(light: hex(0xDEDEE2), dark: hex(0x38383A))
    static let buttonBorder = dyn(light: hex(0xC7C7CC), dark: hex(0x48484A))
    static let safeChipBG = dyn(light: hex(0xD5F2DD), dark: hex(0x173A22))
    static let safeChipFG = dyn(light: hex(0x1E7A3A), dark: hex(0x7EE29A))
    static let reviewChipBG = dyn(light: hex(0xFBE6CC), dark: hex(0x3D2A12))
    static let reviewChipFG = dyn(light: hex(0x9A5A12), dark: hex(0xFFB877))
    static let sudoBG = dyn(light: hex(0xF8DCC0), dark: hex(0x4A2A12))
    static let warnBG = dyn(light: hex(0xFFF3CF), dark: hex(0x372D12))
    static let warnBorder = dyn(light: hex(0xE8C860), dark: hex(0x6B5414))
    static let warnFG = dyn(light: hex(0x6B5100), dark: hex(0xF5D58A))
    static let warnText = dyn(light: hex(0x7A5E0A), dark: hex(0xE9CF8F))
    static let infoBG = dyn(light: hex(0xE3EEFA), dark: hex(0x17283A))
    static let infoBorder = dyn(light: hex(0xA9C7E8), dark: hex(0x2C4B6B))
    static let infoFG = dyn(light: hex(0x1F4E7A), dark: hex(0xB9D4F1))
    static let infoStrong = dyn(light: hex(0x123A60), dark: hex(0xD7E7F8))
    static let infoMuted = dyn(light: hex(0x4A6F95), dark: hex(0x8FB2D6))
    static let dangerBG = dyn(light: hex(0xFDE3E1), dark: hex(0x3A1D1D))
    static let dangerBorder = dyn(light: hex(0xE8A6A1), dark: hex(0x6E2B2B))
    static let dangerFG = dyn(light: hex(0xB3261E), dark: hex(0xFF9B94))
    static let primaryBG = dyn(light: hex(0x1C1C1E), dark: hex(0xF2F2F4))
    static let primaryFG = dyn(light: hex(0xFFFFFF), dark: hex(0x111113))
}

/// White (dark mode) button: Rescan, Copy script.
struct UsagePrimaryButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 13, weight: .semibold))
            .padding(.horizontal, 16)
            .frame(height: 30)
            .foregroundStyle(UsagePalette.primaryFG)
            .background(UsagePalette.primaryBG, in: RoundedRectangle(cornerRadius: 7))
            .opacity(enabled ? (configuration.isPressed ? 0.8 : 1) : 0.4)
    }
}

/// Red button: Cancel scan.
struct UsageDangerButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 13, weight: .semibold))
            .padding(.horizontal, 16)
            .frame(height: 30)
            .foregroundStyle(UsagePalette.dangerFG)
            .background(UsagePalette.dangerBG, in: RoundedRectangle(cornerRadius: 7))
            .overlay(RoundedRectangle(cornerRadius: 7).stroke(UsagePalette.dangerBorder))
            .opacity(configuration.isPressed ? 0.8 : 1)
    }
}
