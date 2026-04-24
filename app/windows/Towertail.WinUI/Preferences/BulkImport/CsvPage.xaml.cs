using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Windows.Storage;
using Windows.Storage.Pickers;
using WinRT.Interop;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class CsvPage : Page
{
    /// <summary>Maps to the Mac CSVFieldRole enum. "Ignore" is explicit so
    /// noisy columns can be dropped without editing the file.</summary>
    public enum FieldRole { Ignore, Hostname, Ip, SshUser, Kind, Tags }

    private static readonly (FieldRole Role, string Label)[] _roles = new[]
    {
        (FieldRole.Ignore,   "Ignore"),
        (FieldRole.Hostname, "Name / hostname"),
        (FieldRole.Ip,       "IP / host"),
        (FieldRole.SshUser,  "SSH user"),
        (FieldRole.Kind,     "Kind"),
        (FieldRole.Tags,     "Tags"),
    };

    private readonly BulkImportState _state;
    private readonly IntPtr _hwnd;
    private List<List<string>> _raw = new();
    private List<FieldRole> _mapping = new();

    public CsvPage() { InitializeComponent(); _state = new(); _hwnd = IntPtr.Zero; }
    public CsvPage(BulkImportState state, IntPtr hwnd) : this()
    {
        _state = state;
        _hwnd = hwnd;
    }

    private async void OnPick(object sender, RoutedEventArgs e)
    {
        var picker = new FileOpenPicker();
        picker.FileTypeFilter.Add(".csv");
        picker.FileTypeFilter.Add(".tsv");
        picker.FileTypeFilter.Add(".txt");
        if (_hwnd != IntPtr.Zero) InitializeWithWindow.Initialize(picker, _hwnd);

        StorageFile? file = null;
        try { file = await picker.PickSingleFileAsync(); }
        catch (Exception ex) { ShowError($"Couldn't open picker: {ex.Message}"); return; }
        if (file is null) return;

        try
        {
            var text = await FileIO.ReadTextAsync(file);
            var ext = System.IO.Path.GetExtension(file.Name);
            var delim = CsvParser.DelimiterForExtension(ext);
            _raw = CsvParser.Parse(text, delim);
            FilenameText.Text = file.Name;
            FilenameText.Visibility = Visibility.Visible;
            HideError();
            RebuildMapping();
        }
        catch (Exception ex)
        {
            ShowError($"Couldn't read file: {ex.Message}");
            _raw = new();
            _state.Candidates = new();
            _state.NotifyCandidatesChanged();
            RenderEmpty();
        }
    }

    private void OnHeaderChanged(object sender, RoutedEventArgs e) => RebuildMapping();

    private void RebuildMapping()
    {
        var columns = _raw.FirstOrDefault()?.Count ?? 0;
        _mapping = new(columns);
        var hasHeader = HeaderCheck.IsChecked == true;
        if (hasHeader && _raw.Count > 0)
        {
            var header = _raw[0];
            for (int c = 0; c < columns; c++)
                _mapping.Add(GuessRole(c < header.Count ? header[c] : ""));
        }
        else
        {
            // Without a header row fall back to a conservative default.
            for (int c = 0; c < columns; c++)
            {
                _mapping.Add(c switch
                {
                    0 => FieldRole.Hostname,
                    1 => FieldRole.Ip,
                    2 => FieldRole.SshUser,
                    3 => FieldRole.Tags,
                    _ => FieldRole.Ignore,
                });
            }
        }
        RenderMapping();
        RenderPreview();
        ApplyMapping();
    }

    private void RenderEmpty()
    {
        MappingPanel.Children.Clear();
        PreviewPanel.Children.Clear();
        HeaderCheck.Visibility = Visibility.Collapsed;
        MappingScroll.Visibility = Visibility.Collapsed;
        PreviewBorder.Visibility = Visibility.Collapsed;
        EmptyHint.Visibility = Visibility.Visible;
    }

    private void RenderMapping()
    {
        MappingPanel.Children.Clear();
        var columns = _raw.FirstOrDefault()?.Count ?? 0;
        if (columns == 0) { RenderEmpty(); return; }

        HeaderCheck.Visibility = Visibility.Visible;
        MappingScroll.Visibility = Visibility.Visible;
        EmptyHint.Visibility = Visibility.Collapsed;

        var hasHeader = HeaderCheck.IsChecked == true;
        for (int c = 0; c < columns; c++)
        {
            var header = hasHeader && _raw.Count > 0 && c < _raw[0].Count
                ? _raw[0][c]
                : $"Column {c + 1}";

            var stack = new StackPanel { Spacing = 4, Width = 160 };
            stack.Children.Add(new TextBlock
            {
                Text = header,
                Style = (Style)Application.Current.Resources["CaptionTextBlockStyle"],
                Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
                TextTrimming = Microsoft.UI.Xaml.TextTrimming.CharacterEllipsis,
            });

            var combo = new ComboBox { Width = 160 };
            foreach (var r in _roles) combo.Items.Add(r.Label);
            combo.SelectedIndex = IndexOfRole(_mapping[c]);
            int captured = c;
            combo.SelectionChanged += (_, _) =>
            {
                if (combo.SelectedIndex >= 0 && combo.SelectedIndex < _roles.Length)
                {
                    _mapping[captured] = _roles[combo.SelectedIndex].Role;
                    ApplyMapping();
                }
            };
            stack.Children.Add(combo);
            MappingPanel.Children.Add(stack);
        }
    }

    private void RenderPreview()
    {
        PreviewPanel.Children.Clear();
        var hasHeader = HeaderCheck.IsChecked == true;
        var dataRows = hasHeader && _raw.Count > 0 ? _raw.Skip(1).ToList() : new List<List<string>>(_raw);
        if (dataRows.Count == 0) { PreviewBorder.Visibility = Visibility.Collapsed; return; }
        PreviewBorder.Visibility = Visibility.Visible;

        foreach (var r in dataRows.Take(20))
        {
            var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
            foreach (var cell in r)
            {
                row.Children.Add(new TextBlock
                {
                    Text = cell,
                    FontFamily = (Microsoft.UI.Xaml.Media.FontFamily)Application.Current.Resources["MonoFont"],
                    FontSize = 12,
                    Width = 160,
                    TextTrimming = Microsoft.UI.Xaml.TextTrimming.CharacterEllipsis,
                });
            }
            PreviewPanel.Children.Add(row);
        }
        if (dataRows.Count > 20)
        {
            PreviewPanel.Children.Add(new TextBlock
            {
                Text = $"… and {dataRows.Count - 20} more row{(dataRows.Count - 20 == 1 ? "" : "s")}",
                Style = (Style)Application.Current.Resources["CaptionTextBlockStyle"],
                Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
                Margin = new Thickness(0, 4, 0, 0),
            });
        }
    }

    private void ApplyMapping()
    {
        var hasHeader = HeaderCheck.IsChecked == true;
        var dataRows = hasHeader && _raw.Count > 0 ? _raw.Skip(1) : _raw;
        var defaultUser = Environment.UserName;
        var rows = new List<ImportRow>();
        foreach (var cells in dataRows)
        {
            var hostname = "";
            var ip = "";
            var user = "";
            var kind = NodeKind.Ssh;
            var tags = new List<string>();
            for (int c = 0; c < cells.Count; c++)
            {
                if (c >= _mapping.Count) break;
                var v = cells[c].Trim();
                switch (_mapping[c])
                {
                    case FieldRole.Ignore: break;
                    case FieldRole.Hostname: hostname = v; break;
                    case FieldRole.Ip: ip = v; break;
                    case FieldRole.SshUser: user = v; break;
                    case FieldRole.Kind:
                        kind = v.Equals("local", StringComparison.OrdinalIgnoreCase) ? NodeKind.Local : NodeKind.Ssh;
                        break;
                    case FieldRole.Tags:
                        tags = v.Split(new[] { ',', ';', '|' })
                                .Select(s => s.Trim())
                                .Where(s => s.Length > 0)
                                .ToList();
                        break;
                }
            }
            var name = string.IsNullOrEmpty(hostname) ? ip : hostname;
            var host = string.IsNullOrEmpty(ip) ? hostname : ip;
            if (string.IsNullOrEmpty(name) && string.IsNullOrEmpty(host)) continue;
            rows.Add(new ImportRow
            {
                DisplayName = name,
                Kind = kind,
                SshUser = string.IsNullOrEmpty(user) ? defaultUser : user,
                SshHost = host,
                TagsText = string.Join(", ", tags),
            });
        }
        _state.Candidates = rows;
        _state.NotifyCandidatesChanged();
    }

    private static FieldRole GuessRole(string header)
    {
        var h = header.Trim().ToLowerInvariant();
        return h switch
        {
            "host" or "hostname" or "name" or "display_name" or "server" => FieldRole.Hostname,
            "ip" or "address" or "ansible_host" or "ip_address" => FieldRole.Ip,
            "user" or "ssh_user" or "ansible_user" or "login" => FieldRole.SshUser,
            "kind" or "type" => FieldRole.Kind,
            "tags" or "labels" or "groups" => FieldRole.Tags,
            _ => FieldRole.Ignore,
        };
    }

    private static int IndexOfRole(FieldRole r)
    {
        for (int i = 0; i < _roles.Length; i++) if (_roles[i].Role == r) return i;
        return 0;
    }

    private void ShowError(string msg)
    {
        ErrorText.Text = msg;
        ErrorText.Visibility = Visibility.Visible;
    }

    private void HideError()
    {
        ErrorText.Text = "";
        ErrorText.Visibility = Visibility.Collapsed;
    }
}
