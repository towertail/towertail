import SwiftUI

/// One row for each host with the install steps and the result.
struct CloudInstallTable: View {
    let hosts: [Node]
    let installer: CloudInstaller?
    var failures: [UUID: String] = [:]
    /// The text when there is no host.
    var empty = ""

    var body: some View {
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
            if hosts.isEmpty {
                Text(empty)
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    VStack(spacing: 0) {
                        ForEach(hosts) { row($0) }
                    }
                }
            }
        }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
        .frame(maxHeight: .infinity)
    }

    private func row(_ node: Node) -> some View {
        let rows = installer?.rows[node.id] ?? [:]
        let result = installer?.results[node.id] ?? failures[node.id]
        let note = installer?.notes[node.id]
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
