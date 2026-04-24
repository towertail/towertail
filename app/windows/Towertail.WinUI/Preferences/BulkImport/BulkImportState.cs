namespace Towertail.WinUI.Preferences.BulkImport;

public enum BulkImportSource { Tailscale, Csv, Paste }

public sealed class BulkImportState
{
    public BulkImportSource Source { get; set; } = BulkImportSource.Tailscale;
    public List<ImportRow> Candidates { get; set; } = new();

    /// <summary>Fires after <see cref="Candidates"/> is replaced so the wizard
    /// footer can re-evaluate whether Next is enabled.</summary>
    public event Action? CandidatesChanged;
    public void NotifyCandidatesChanged() => CandidatesChanged?.Invoke();
}
