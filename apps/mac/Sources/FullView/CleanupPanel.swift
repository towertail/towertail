import AppKit
import SwiftUI

/// Right card of the Usage sub-tab: the cleanup list (recommendations and
/// the items of the user) and the cleanup script. Towertail never runs
/// the script. The user copies it.
struct CleanupPanel: View {
    @Bindable var model: DiskUsageModel
    /// "this Mac" for the local node, else the hostname.
    let hostLabel: String
    let isLocal: Bool

    @State private var copied = false
    @State private var showHidden = false

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 2) {
                Text("Cleanup list").font(.system(size: 15, weight: .bold))
                Text("Rules suggest items. Add your own from the map.")
                    .font(.system(size: 12)).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(EdgeInsets(top: 14, leading: 16, bottom: 10, trailing: 16))
            Divider()

            if model.isRunning && !model.showPrevious {
                waiting
            } else if let result = model.result {
                list(result)
                Divider()
                footer(result)
            } else {
                Text(model.phase == .idle ? "Recommendations show after the first scan." : "No recommendations.")
                    .font(.system(size: 13)).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(16)
            }
        }
        .frame(width: 420)
        .frame(maxHeight: .infinity)
        .background(UsagePalette.card, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(UsagePalette.border))
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private var waiting: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Recommendations show after the scan.")
            if let rec = model.record {
                HStack(spacing: 10) {
                    Text("Previous scan · \(UsageText.ago(rec.finishedAt)) · \(DiskSize.text(rec.result.scannedBytes))")
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button("Show") { model.showPrevious = true }
                }
                .padding(12)
                .background(UsagePalette.well, in: RoundedRectangle(cornerRadius: 9))
                .overlay(RoundedRectangle(cornerRadius: 9).stroke(UsagePalette.border))
            }
        }
        .font(.system(size: 13))
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(EdgeInsets(top: 20, leading: 16, bottom: 20, trailing: 16))
    }

    private func list(_ result: DiskScanResult) -> some View {
        ScrollView {
            VStack(spacing: 0) {
                group(.safe, title: "Safe to remove", bg: UsagePalette.safeChipBG, fg: UsagePalette.safeChipFG, result: result)
                group(.review, title: "Review first", bg: UsagePalette.reviewChipBG, fg: UsagePalette.reviewChipFG, result: result)
                mine
                let hidden = model.hiddenRecs.count
                if hidden > 0 {
                    VStack(spacing: 0) {
                        Rectangle().fill(UsagePalette.rowLine).frame(height: 1)
                        HStack {
                            Text("\(hidden) hidden \(hidden == 1 ? "suggestion" : "suggestions")")
                            Spacer()
                            Button(showHidden ? "Hide again" : "Show") { showHidden.toggle() }
                                .buttonStyle(.link)
                        }
                        .font(.system(size: 12)).foregroundStyle(.secondary)
                        .padding(.horizontal, 16).padding(.vertical, 10)
                    }
                }
            }
        }
        .frame(maxHeight: .infinity)
    }

    private static func chip(_ title: String, bg: Color, fg: Color) -> some View {
        Text(title)
            .font(.system(size: 12, weight: .semibold))
            .padding(.horizontal, 8).padding(.vertical, 2)
            .background(bg, in: RoundedRectangle(cornerRadius: 5))
            .foregroundStyle(fg)
    }

    private static func groupHead(_ title: String, bg: Color, fg: Color, bytes: Int64) -> some View {
        HStack {
            chip(title, bg: bg, fg: fg)
            Spacer()
            Text(DiskSize.text(bytes))
                .font(.system(size: 12, design: .monospaced)).foregroundStyle(.secondary)
        }
        .padding(EdgeInsets(top: 10, leading: 16, bottom: 6, trailing: 16))
    }

    @ViewBuilder
    private func group(_ g: DiskRecGroup, title: String, bg: Color, fg: Color, result: DiskScanResult) -> some View {
        let recs = result.recs.filter { $0.group == g && (showHidden || !model.isHidden($0)) }
        if !recs.isEmpty {
            Self.groupHead(title, bg: bg, fg: fg,
                           bytes: recs.filter { !model.isHidden($0) }.reduce(0) { $0 + $1.bytes })
            ForEach(recs) { rec in
                row(rec, windows: result.isWindows)
            }
        }
    }

    @ViewBuilder
    private var mine: some View {
        let items = model.mineItems
        Self.groupHead("Added by you", bg: UsagePalette.infoBG, fg: UsagePalette.infoFG,
                       bytes: items.reduce(0) { $0 + $1.node.bytes })
        if items.isEmpty {
            Text("Nothing yet. Use the box on a tile, or right-click it, to add a folder or file.")
                .font(.system(size: 12)).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(EdgeInsets(top: 4, leading: 16, bottom: 12, trailing: 16))
        }
        ForEach(items) { item in
            Self.row(label: item.label, path: item.displayPath, bytes: item.node.bytes,
                     on: Binding(get: { item.on }, set: { model.setMine(item.path, on: $0) })) {
                Button { model.removeMine(item.path) } label: { Text("×") }
                    .buttonStyle(RowXButtonStyle())
                    .help("Remove from the list")
                    .accessibilityLabel("Remove \(item.label)")
            }
        }
    }

    private func row(_ rec: DiskRec, windows: Bool) -> some View {
        let hidden = model.isHidden(rec)
        return Self.row(label: rec.label, path: rec.path, bytes: rec.bytes,
                        on: Binding(get: { model.selected.contains(rec.id) }, set: { _ in model.toggle(rec.id) }),
                        badge: rec.elevated ? (windows ? "admin" : "sudo") : nil, hidden: hidden) {
            if hidden {
                Button("Show") { model.unhide(rec) }.buttonStyle(.link).font(.system(size: 12))
            } else {
                Button { model.hide(rec) } label: { Text("×") }
                    .buttonStyle(RowXButtonStyle())
                    .help("Hide this suggestion")
                    .accessibilityLabel("Hide \(rec.label)")
            }
        }
    }

    private static func row<Trailing: View>(
        label: String, path: String, bytes: Int64, on: Binding<Bool>, badge: String? = nil, hidden: Bool = false,
        @ViewBuilder trailing: () -> Trailing
    ) -> some View {
        VStack(spacing: 0) {
            Rectangle().fill(UsagePalette.rowLine).frame(height: 1)
            HStack(spacing: 10) {
                Toggle("", isOn: on)
                    .toggleStyle(.checkbox)
                    .labelsHidden()
                    .tint(TreemapView.selectedGreen)
                    .disabled(hidden)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(label).font(.system(size: 13)).strikethrough(hidden)
                        if let badge {
                            Text(badge)
                                .font(.system(size: 10, weight: .bold))
                                .padding(.horizontal, 5).padding(.vertical, 1)
                                .background(UsagePalette.sudoBG, in: RoundedRectangle(cornerRadius: 4))
                                .foregroundStyle(UsagePalette.reviewChipFG)
                        }
                    }
                    Text(path)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .onTapGesture { if !hidden { on.wrappedValue.toggle() } }
                Text(DiskSize.text(bytes)).font(.system(size: 13, design: .monospaced))
                trailing()
            }
            .padding(EdgeInsets(top: 8, leading: 16, bottom: 8, trailing: 10))
            .opacity(hidden ? 0.55 : 1)
        }
    }

    private func footer(_ result: DiskScanResult) -> some View {
        let script = model.script() ?? CleanupScript(result: result, selected: [])
        let text = script.text
        return VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text(DiskSize.text(script.totalBytes)).font(.system(size: 13, weight: .bold, design: .monospaced))
                    + Text(" selected · \(script.count) \(script.count == 1 ? "item" : "items")")
                    .font(.system(size: 13)).foregroundColor(.secondary)
                Spacer()
                Text(script.shellLabel)
                    .font(.system(size: 11)).foregroundStyle(.secondary)
                    .padding(.horizontal, 8).padding(.vertical, 2)
                    .overlay(RoundedRectangle(cornerRadius: 5).stroke(UsagePalette.buttonBorder))
            }
            ScrollView([.vertical, .horizontal]) {
                Text(text)
                    .font(.system(size: 11, design: .monospaced))
                    .lineSpacing(3)
                    .foregroundStyle(UsagePalette.scriptText)
                    .textSelection(.enabled)
                    .fixedSize()
                    .frame(maxWidth: .infinity, alignment: .topLeading)
                    .padding(10)
            }
            .frame(height: 150)
            .background(UsagePalette.script, in: RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(UsagePalette.border))
            HStack(spacing: 8) {
                Button("Select safe") { model.selectSafe() }
                Button("Clear") { model.clearSelection() }
                Spacer()
                if model.isStale, let rec = model.record {
                    Text("Scanned \(UsageText.ago(rec.finishedAt)). Rescan before cleanup?")
                        .font(.system(size: 11)).foregroundStyle(UsagePalette.reviewChipFG)
                        .lineLimit(2).multilineTextAlignment(.trailing)
                }
                Button(copied ? "Copied ✓" : "Copy script") { copy(text) }
                    .buttonStyle(UsagePrimaryButtonStyle())
                    .disabled(script.count == 0)
            }
            Text(isLocal
                 ? "Paste into Terminal on this Mac. Towertail never runs the script."
                 : "Paste into an SSH session on \(hostLabel). Towertail never runs the script.")
                .font(.system(size: 11)).foregroundStyle(.secondary)
        }
        .padding(EdgeInsets(top: 12, leading: 16, bottom: 12, trailing: 16))
        .background(UsagePalette.footer)
    }

    private func copy(_ text: String) {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(text, forType: .string)
        copied = true
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            copied = false
        }
    }
}

/// The × button of a row in the cleanup list.
struct RowXButtonStyle: ButtonStyle {
    @State private var hover = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 15))
            .frame(width: 22, height: 22)
            .foregroundStyle(hover ? Color.primary : Color.secondary)
            .background(hover || configuration.isPressed ? UsagePalette.well : .clear, in: RoundedRectangle(cornerRadius: 5))
            .contentShape(Rectangle())
            .onHover { hover = $0 }
    }
}
