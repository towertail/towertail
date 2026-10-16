import Foundation

/// The cleanup script that the user copies. Towertail never runs it.
/// bash/zsh for Linux and macOS, PowerShell for Windows (from `result.os`).
/// The first command is a hostname guard. The selected recommendations
/// follow in list order, then the items that the user added.
struct CleanupScript {
    /// A folder or file that the user added, with its size in the tree.
    struct Item: Equatable {
        var path: String
        var bytes: Int64
    }

    let result: DiskScanResult
    let selected: Set<String>
    let now: Date
    /// Absolute paths. Paths that are not in the tree are left out.
    let mine: [Item]

    init(result: DiskScanResult, selected: Set<String>, mine: [String] = [], now: Date = Date()) {
        self.result = result
        self.selected = selected
        self.now = now
        self.mine = mine.compactMap { p in
            result.names(of: p).flatMap { result.tree.node(at: $0[...]) }.map { Item(path: p, bytes: $0.bytes) }
        }
    }

    var isPowerShell: Bool { result.isWindows }

    var chosen: [DiskRec] { result.recs.filter { selected.contains($0.id) } }

    var count: Int { chosen.count + mine.count }

    var totalBytes: Int64 { chosen.reduce(0) { $0 + $1.bytes } + mine.reduce(0) { $0 + $1.bytes } }

    var shellLabel: String {
        switch result.os {
        case "windows": return "PowerShell · Windows"
        case "darwin": return "bash/zsh · macOS"
        default: return "bash · Linux"
        }
    }

    var text: String {
        let recs = chosen
        let n = count
        let host = result.hostname
        var lines: [String] = []
        lines.append("# Towertail cleanup for \(host) · \(Self.stamp(now))")
        lines.append("# Root: \(result.root) · scanned \(Self.stamp(result.startedAt ?? now))")
        lines.append("# \(n) \(n == 1 ? "item" : "items") · \(DiskSize.text(totalBytes)). Read each line before you run it.")
        guard n > 0 else {
            lines.append("# Select items above to build the script.")
            return lines.joined(separator: "\n") + "\n"
        }
        let elevated = recs.contains { $0.elevated } || mine.contains { !isBelowHome($0.path) }
        if isPowerShell {
            if elevated { lines.append("# Lines marked admin need an elevated PowerShell.") }
            lines.append("& {")
            lines.append("  if ([System.Net.Dns]::GetHostName() -ne \(Self.psQuote(host))) { Write-Host \(Self.psQuote("wrong host: this script is for \(host)")); return }")
            for rec in recs {
                lines.append("")
                lines.append("  # \(rec.label) · \(DiskSize.text(rec.bytes))")
                for c in result.commands(for: rec) { lines.append("  " + c) }
            }
            for item in mine {
                lines.append("")
                lines.append("  # \(itemComment(item))")
                lines.append("  Remove-Item -LiteralPath \(Self.psQuote(item.path)) -Recurse -Force")
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
            for item in mine {
                lines.append("")
                lines.append("# \(itemComment(item))")
                lines.append("rm -rf -- \(shPath(item.path))")
            }
        }
        return lines.joined(separator: "\n") + "\n"
    }

    private func isBelowHome(_ path: String) -> Bool {
        !result.home.isEmpty && path.hasPrefix(result.home + result.separator)
    }

    private func itemComment(_ item: Item) -> String {
        var c = "Added by you · \(DiskSize.text(item.bytes))"
        if !isBelowHome(item.path) { c += isPowerShell ? " · admin" : " · may need sudo" }
        return c
    }

    /// `"$HOME"/'rest'` below home, so that the line is easy to read.
    private func shPath(_ path: String) -> String {
        guard isBelowHome(path) else { return Self.shQuote(path) }
        return "\"$HOME\"/" + Self.shQuote(String(path.dropFirst(result.home.count + 1)))
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
