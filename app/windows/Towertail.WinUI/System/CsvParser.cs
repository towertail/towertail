namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Minimal RFC-4180-style CSV/TSV parser. Handles quoted fields (including
/// the "" escape), embedded delimiters, and CR/LF/CRLF line endings.
/// Trailing empty rows are dropped so a file with a stray newline at EOF
/// doesn't surface a phantom row.
/// </summary>
public static class CsvParser
{
    public static List<List<string>> Parse(string text, char delimiter = ',')
    {
        var rows = new List<List<string>>();
        var field = new System.Text.StringBuilder();
        var row = new List<string>();
        bool inQuotes = false;

        int i = 0;
        int n = text.Length;
        while (i < n)
        {
            char c = text[i];
            if (inQuotes)
            {
                if (c == '"')
                {
                    if (i + 1 < n && text[i + 1] == '"')
                    {
                        field.Append('"');
                        i += 2;
                        continue;
                    }
                    inQuotes = false;
                    i++;
                    continue;
                }
                field.Append(c);
                i++;
                continue;
            }
            if (c == '"') { inQuotes = true; i++; continue; }
            if (c == delimiter)
            {
                row.Add(field.ToString());
                field.Clear();
                i++;
                continue;
            }
            if (c == '\n' || c == '\r')
            {
                row.Add(field.ToString());
                field.Clear();
                rows.Add(row);
                row = new List<string>();
                if (c == '\r' && i + 1 < n && text[i + 1] == '\n') i += 2;
                else i++;
                continue;
            }
            field.Append(c);
            i++;
        }
        if (field.Length > 0 || row.Count > 0)
        {
            row.Add(field.ToString());
            rows.Add(row);
        }
        rows.RemoveAll(r => r.Count == 1 && string.IsNullOrEmpty(r[0]));
        return rows;
    }

    /// <summary>
    /// Chooses a delimiter from the file extension. Tab for .tsv, comma for
    /// everything else.
    /// </summary>
    public static char DelimiterForExtension(string? extension)
    {
        if (extension is null) return ',';
        return extension.TrimStart('.').Equals("tsv", StringComparison.OrdinalIgnoreCase) ? '\t' : ',';
    }
}
