import SwiftUI

struct WelcomeInstallView: View {
    let flow: WelcomeFlow

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 16) {
                WelcomeHeading(
                    title: "Turn on 24/7 monitoring",
                    subtitle: "Towertail copies the sampler to each server and starts it."
                )
                table
                noSudo
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 16)
            WelcomeFooter(
                step: .install,
                secondary: flow.hasPlanFull
                    ? .init(title: "Upgrade for \(flow.upgradeName)", run: flow.upgrade)
                    : nil,
                primary: .init(title: "Done", run: flow.finish)
            )
        }
        .task { await flow.install() }
    }

    private var table: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text("Server").frame(width: 130, alignment: .leading)
                ForEach(InstallStep.allCases, id: \.rawValue) { step in
                    Text(step.title).frame(width: 62, alignment: .leading)
                }
                Text("Result").frame(maxWidth: .infinity, alignment: .leading)
            }
            .font(Typography.rowColumn)
            .foregroundStyle(.secondary)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            Divider()
            if flow.added.isEmpty {
                Text("No servers added. Add servers in Preferences, then turn on 24/7 monitoring.")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    VStack(spacing: 0) {
                        ForEach(flow.added) { row($0) }
                    }
                }
            }
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func row(_ node: Node) -> some View {
        let rows = flow.installer?.rows[node.id] ?? [:]
        let result = flow.installer?.results[node.id] ?? flow.addFailures[node.id]
        let note = flow.installer?.notes[node.id]
        return HStack(spacing: 8) {
            Text(node.displayName).font(Typography.rowName).lineLimit(1).frame(width: 130, alignment: .leading)
            ForEach(InstallStep.allCases, id: \.rawValue) { step in
                StepMark(state: rows[step] ?? .waiting).frame(width: 62, alignment: .leading)
            }
            VStack(alignment: .leading, spacing: 1) {
                if let result {
                    Text(result)
                        .foregroundStyle(failed(rows) ? ThresholdTint.critical.color : Color.primary)
                }
                if let note { Text(note).foregroundStyle(.secondary) }
            }
            .font(.system(size: 11))
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
    }

    private func failed(_ rows: [InstallStep: InstallStepState]) -> Bool {
        rows.values.contains { if case .failed = $0 { return true }; return false }
    }

    private var noSudo: some View {
        HStack(spacing: 10) {
            Image(systemName: "lock.open").foregroundStyle(.secondary)
            Text("The sampler runs as your user in the background. It needs no sudo and no root.")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10).stroke(Color.secondary.opacity(0.3)))
    }
}

/// The four states of one install step.
private struct StepMark: View {
    let state: InstallStepState

    var body: some View {
        switch state {
        case .waiting:
            Image(systemName: "circle").foregroundStyle(.tertiary).accessibilityLabel("Waiting")
        case .running:
            ProgressView().controlSize(.mini).accessibilityLabel("Running")
        case .done:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(ThresholdTint.nominal.color).accessibilityLabel("Done")
        case .failed:
            Image(systemName: "xmark.circle.fill").foregroundStyle(ThresholdTint.critical.color).accessibilityLabel("Failed")
        }
    }
}
