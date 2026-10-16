import AppKit
import SwiftUI

/// Right card of the Usage sub-tab: the recommendations and the cleanup
/// script. Towertail never runs the script. The user copies it.
struct CleanupPanel: View {
    @Bindable var model: DiskUsageModel
    /// "this Mac" for the local node, else the hostname.
    let hostLabel: String
    let isLocal: Bool

    @State private var copied = false

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 2) {
                Text("Recommendations").font(.system(size: 15, weight: .bold))
                Text("Select items to build a cleanup script")
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
                if result.recs.isEmpty {
                    Text("The scan found nothing to clean up.")
                        .font(.system(size: 13)).foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(16)
                }
            }
        }
        .frame(maxHeight: .infinity)
    }

    @ViewBuilder
    private func group(_ g: DiskRecGroup, title: String, bg: Color, fg: Color, result: DiskScanResult) -> some View {
        let recs = result.recs.filter { $0.group == g }
        if !recs.isEmpty {
            HStack {
                Text(title)
                    .font(.system(size: 12, weight: .semibold))
                    .padding(.horizontal, 8).padding(.vertical, 2)
                    .background(bg, in: RoundedRectangle(cornerRadius: 5))
                    .foregroundStyle(fg)
                Spacer()
                Text(DiskSize.text(recs.reduce(0) { $0 + $1.bytes }))
                    .font(.system(size: 12, design: .monospaced)).foregroundStyle(.secondary)
            }
            .padding(EdgeInsets(top: 10, leading: 16, bottom: 6, trailing: 16))
            ForEach(recs) { rec in
                row(rec, windows: result.isWindows)
            }
        }
    }

    private func row(_ rec: DiskRec, windows: Bool) -> some View {
        VStack(spacing: 0) {
            Rectangle().fill(UsagePalette.rowLine).frame(height: 1)
            HStack(spacing: 10) {
                Toggle("", isOn: Binding(
                    get: { model.selected.contains(rec.id) },
                    set: { _ in model.toggle(rec.id) }
                ))
                .toggleStyle(.checkbox)
                .labelsHidden()
                .tint(TreemapView.selectedGreen)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(rec.label).font(.system(size: 13))
                        if rec.elevated {
                            Text(windows ? "admin" : "sudo")
                                .font(.system(size: 10, weight: .bold))
                                .padding(.horizontal, 5).padding(.vertical, 1)
                                .background(UsagePalette.sudoBG, in: RoundedRectangle(cornerRadius: 4))
                                .foregroundStyle(UsagePalette.reviewChipFG)
                        }
                    }
                    Text(rec.path)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .onTapGesture { model.toggle(rec.id) }
                Text(DiskSize.text(rec.bytes)).font(.system(size: 13, design: .monospaced))
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
        }
    }

    private func footer(_ result: DiskScanResult) -> some View {
        let script = CleanupScript(result: result, selected: model.selected)
        let text = script.text
        return VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text(DiskSize.text(script.totalBytes)).font(.system(size: 13, weight: .bold, design: .monospaced))
                    + Text(" selected · \(script.chosen.count) \(script.chosen.count == 1 ? "item" : "items")")
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
                    .disabled(script.chosen.isEmpty)
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
