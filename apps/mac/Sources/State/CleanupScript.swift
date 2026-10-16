import Foundation

/// The cleanup script that the user copies. Towertail never runs it.
/// bash/zsh for Linux and macOS, PowerShell for Windows (from `result.os`).
/// The first command is a hostname guard. The selected recommendations
/// follow in list order.
struct CleanupScript {
    let result: DiskScanResult
    let selected: Set<String>
    let now: Date

    init(result: DiskScanResult, selected: Set<String>, now: Date = Date()) {
        self.result = result
        self.selected = selected
        self.now = now
    }

    var isPowerShell: Bool { result.isWindows }

    var chosen: [DiskRec] { result.recs.filter { selected.contains($0.id) } }

    var totalBytes: Int64 { chosen.reduce(0) { $0 + $1.bytes } }

    var shellLabel: String {
        switch result.os {
        case "windows": return "PowerShell · Windows"
        case "darwin": return "bash/zsh · macOS"
        default: return "bash · Linux"
        }
    }

    var text: String {
        let recs = chosen
        let host = result.hostname
        var lines: [String] = []
        lines.append("# Towertail cleanup for \(host) · \(Self.stamp(now))")
        lines.append("# Root: \(result.root) · scanned \(Self.stamp(result.startedAt ?? now))")
        lines.append("# \(recs.count) \(recs.count == 1 ? "item" : "items") · \(DiskSize.text(totalBytes)). Read each line before you run it.")
        guard !recs.isEmpty else {
            lines.append("# Select items above to build the script.")
            return lines.joined(separator: "\n") + "\n"
        }
        let elevated = recs.contains { $0.elevated }
        if isPowerShell {
            if elevated { lines.append("# Lines marked admin need an elevated PowerShell.") }
            lines.append("& {")
            lines.append("  if ([System.Net.Dns]::GetHostName() -ne \(Self.psQuote(host))) { Write-Host \(Self.psQuote("wrong host: this script is for \(host)")); return }")
            for rec in recs {
                lines.append("")
                lines.append("  # \(rec.label) · \(DiskSize.text(rec.bytes))")
                for c in result.commands(for: rec) { lines.append("  " + c) }
            }
            lines.append("}")
        } else {
            if elevated { lines.append("# sudo lines ask for your password.") }
            lines.append("set -u")
            lines.append("[ \"$(hostname)\" = \(Self.shQuote(host)) ] || { echo \(Self.shQuote("wrong host: this script is for \(host)")); exit 1; }")
            for rec in recs {
                lines.append("")
                lines.append("# \(rec.label) · \(DiskSize.text(rec.bytes))")
                lines.append(contentsOf: result.commands(for: rec))
            }
        }
        return lines.joined(separator: "\n") + "\n"
    }

    static func shQuote(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    static func psQuote(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "''") + "'"
    }

    private static func stamp(_ d: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd HH:mm"
        return f.string(from: d)
    }
}
