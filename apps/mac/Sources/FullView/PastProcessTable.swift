import SwiftUI

/// Top processes of a past range of a Cloud host. Cloud keeps no PID or
/// per-sample snapshot, so the rows are aggregates of name and command.
struct PastProcessTable: View {
    let host: UUID
    let range: ClosedRange<Date>
    let loader: any CloudHistoryLoader

    @State private var rows: [RemoteTopProcess] = []
    @State private var loading = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text("Processes").font(.headline)
                Text("Top processes in this range")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(range.lowerBound.formatted(date: .omitted, time: .shortened)
                     + " – " + range.upperBound.formatted(date: .omitted, time: .shortened))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            if let error {
                note(error)
            } else if loading && rows.isEmpty {
                ProgressView().frame(maxWidth: .infinity, minHeight: 80)
            } else if rows.isEmpty {
                note("No process data in this range.")
            } else {
                Table(rows) {
                    TableColumn("Name") { r in Text(r.name).lineLimit(1).textSelection(.enabled) }
                        .width(min: 100, ideal: 160)
                    TableColumn("Command") { r in Text(r.cmd).lineLimit(1).textSelection(.enabled) }
                        .width(min: 120, ideal: 260)
                    TableColumn("CPU avg %") { r in
                        Text(String(format: "%.1f", r.cpuPctAvg)).monospacedDigit()
                    }
                    .width(min: 70, ideal: 80)
                    TableColumn("RSS max") { r in
                        Text(ProcessTable.formatBytes(Int64(clamping: r.rssMax))).monospacedDigit()
                    }
                    .width(min: 70, ideal: 90)
                }
                .frame(minHeight: 160)
            }
        }
        .padding(.horizontal, 16)
        .task(id: range) { await load() }
    }

    private func note(_ text: String) -> some View {
        Text(text)
            .font(.callout)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, minHeight: 80)
    }

    private func load() async {
        // A moving hover changes the range often. Wait before the request.
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        loading = true
        error = nil
        do {
            let result = try await loader.pastProcesses(host: host, from: range.lowerBound, to: range.upperBound)
            guard !Task.isCancelled else { return }
            rows = result
        } catch {
            guard !Task.isCancelled else { return }
            rows = []
            self.error = CloudHistoryError.message(error)
        }
        loading = false
    }
}

@MainActor
enum CloudHistoryError {
    /// The server text of an error envelope, else the error text.
    static func message(_ error: Error) -> String {
        if case RemoteClient.RemoteError.http(_, let body) = error,
           let text = RemoteClient.errorMessage(Data(body.utf8)) {
            return text
        }
        return error.localizedDescription
    }
}
