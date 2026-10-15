import Foundation

/// Minimal RFC-4180-style CSV/TSV parser. Handles quoted fields (including
/// the `""` escape), embedded delimiters, and CR/LF/CRLF line endings.
/// Returns rows as `[[String]]`; trailing empty rows are dropped so a file
/// with a stray newline at EOF doesn't surface a phantom row.
enum CSVParser {
    static func parse(_ text: String, delimiter: Character = ",") -> [[String]] {
        var rows: [[String]] = []
        var field = ""
        var row: [String] = []
        var inQuotes = false

        // Peek-and-consume iterator; we need single-char lookahead for the
        // `""` escape and the `\r\n` sequence.
        var it = text.unicodeScalars.makeIterator()
        var buf: [Unicode.Scalar] = []
        while let s = it.next() { buf.append(s) }

        var i = 0
        while i < buf.count {
            let c = Character(buf[i])
            if inQuotes {
                if c == "\"" {
                    if i + 1 < buf.count && buf[i + 1] == "\"" {
                        field.append("\"")
                        i += 2
                        continue
                    }
                    inQuotes = false
                    i += 1
                    continue
                }
                field.append(c)
                i += 1
                continue
            }
            if c == "\"" {
                inQuotes = true
                i += 1
                continue
            }
            if c == delimiter {
                row.append(field)
                field = ""
                i += 1
                continue
            }
            if c == "\n" || c == "\r" {
                row.append(field)
                field = ""
                rows.append(row)
                row = []
                // Eat \r\n as one line terminator.
                if c == "\r", i + 1 < buf.count, buf[i + 1] == "\n" {
                    i += 2
                } else {
                    i += 1
                }
                continue
            }
            field.append(c)
            i += 1
        }
        // Flush trailing field if the file didn't end with a newline.
        if !field.isEmpty || !row.isEmpty {
            row.append(field)
            rows.append(row)
        }
        // Drop purely-empty rows (one empty cell only) — these come from
        // trailing newlines and shouldn't look like real rows.
        return rows.filter { !($0.count == 1 && $0[0].isEmpty) }
    }

    /// Chooses a delimiter from the file extension. Tabs for `.tsv`,
    /// commas for everything else. This matches what most spreadsheet apps
    /// write and keeps the UI from having to ask the user.
    static func delimiter(forExtension ext: String) -> Character {
        ext.lowercased() == "tsv" ? "\t" : ","
    }
}
