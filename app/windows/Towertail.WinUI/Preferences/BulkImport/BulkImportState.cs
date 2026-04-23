using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences.BulkImport;

public enum BulkImportSource { Tailscale, Paste }

public sealed class BulkImportState
{
    public BulkImportSource Source { get; set; } = BulkImportSource.Tailscale;
    public List<Node> Candidates { get; set; } = new();
}
