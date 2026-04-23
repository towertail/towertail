using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Towertail.WinUI.Preferences;

public sealed partial class LogsPane : UserControl
{
    private readonly string _logDir;
    public LogsPane()
    {
        InitializeComponent();
        _logDir = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "Towertail", "logs");
        PathText.Text = _logDir;
        TryLoadLatest();
    }

    private void TryLoadLatest()
    {
        if (!Directory.Exists(_logDir)) { Viewer.Text = "(no logs yet)"; return; }
        var latest = new DirectoryInfo(_logDir).GetFiles("*.log").OrderByDescending(f => f.LastWriteTimeUtc).FirstOrDefault();
        if (latest is null) { Viewer.Text = "(no logs yet)"; return; }
        try { Viewer.Text = File.ReadAllText(latest.FullName); }
        catch (Exception ex) { Viewer.Text = $"(error reading {latest.FullName}: {ex.Message})"; }
    }

    private void OnOpenFolder(object sender, RoutedEventArgs e)
    {
        try { global::System.Diagnostics.Process.Start("explorer.exe", _logDir); } catch { }
    }
}
