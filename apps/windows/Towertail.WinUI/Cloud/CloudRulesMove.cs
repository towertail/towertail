using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

public enum RulesMovePhase { Loading, Pick, Sending, Failed }

/// <summary>
/// Moves the local rules of this PC to the cloud. It drives the "Move your rules to the cloud"
/// window. Mirrors the Mac <c>CloudRulesMove</c>. Call it from the UI thread.
/// </summary>
public sealed class CloudRulesMove
{
    public const string Title = "Move your rules to the cloud";
    public const string Lead =
        "This PC has its own rules for the org and for your Cloud hosts. After the move, the cloud holds the rules of Cloud hosts. "
        + "This PC reads them from the cloud and changes them there. Local SSH hosts keep their rules on this PC.";
    public const string Foot = "Needs the admin role in the org. The cloud keeps a copy of the old values for 30 days.";
    public const string LoadingText = "Reading the rules of the cloud…";

    private readonly ServerSettings _settings;
    private readonly NodeStore _nodes;
    private readonly CloudPrefs _prefs;
    private readonly Func<CloudRules?> _rules;
    private readonly Func<RemoteClient?> _client;
    private readonly Func<bool> _isAdmin;
    private readonly string _device;
    private RemoteRulesView? _cloud;
    private IReadOnlyList<RulesDiffRow> _rows = [];
    private HashSet<string> _picks = [];

    public CloudRulesMove(
        ServerSettings settings, NodeStore nodes, CloudPrefs prefs,
        Func<CloudRules?> rules, Func<RemoteClient?> client, Func<bool> isAdmin, string device)
    {
        _settings = settings;
        _nodes = nodes;
        _prefs = prefs;
        _rules = rules;
        _client = client;
        _isAdmin = isAdmin;
        _device = device;
    }

    public CloudRulesMove(CloudSession session, ServerSettings settings, NodeStore nodes, CloudPrefs prefs)
        : this(settings, nodes, prefs, () => session.Rules, () => session.Client,
               () => session.Account.Me?.IsAdmin == true, Environment.MachineName) { }

    public bool IsPresented { get; private set; }
    public RulesMovePhase Phase { get; private set; } = RulesMovePhase.Pick;
    public string? Error { get; private set; }
    public string? Toast { get; private set; }
    public IReadOnlyList<RulesDiffRow> Rows => _rows;
    public IReadOnlySet<string> Picks => _picks;

    /// <summary>Raised when the window must open or close, and when a pick, the phase or the toast changes.</summary>
    public event Action? Changed;

    /// <summary>Signed in with the admin role.</summary>
    public bool CanMove => _client() is not null && _isAdmin();

    public string SendingText => $"Moving {PickedCount} {(PickedCount == 1 ? "rule" : "rules")}…";

    public string MoveButtonText => PickedCount == 0 ? "Move rules" : $"Move {PickedCount} {(PickedCount == 1 ? "rule" : "rules")}";

    public int PickedCount => _rows.Count(r => _picks.Contains(r.Id));

    /// <summary>Loads the rules of the cloud and opens the window.</summary>
    public async Task OpenAsync()
    {
        IsPresented = true;
        Phase = RulesMovePhase.Loading;
        Changed?.Invoke();
        Phase = await FetchAsync().ConfigureAwait(true);
        Changed?.Invoke();
    }

    /// <summary>
    /// Opens the window one time after sign-in, when a local rule differs from the cloud.
    /// It stays closed when the load fails.
    /// </summary>
    public async Task OfferOnceAsync()
    {
        if (_prefs.RulesMoveOffered || !CanMove || IsPresented) return;
        if (await FetchAsync().ConfigureAwait(true) != RulesMovePhase.Pick || _rows.Count == 0) return;
        _prefs.RulesMoveOffered = true;
        _prefs.Save();
        Phase = RulesMovePhase.Pick;
        IsPresented = true;
        Changed?.Invoke();
    }

    public void Toggle(string id)
    {
        if (!_picks.Remove(id)) _picks.Add(id);
        Changed?.Invoke();
    }

    public async Task StartAsync()
    {
        if (_client() is not { } client || _cloud is not { } cloud || PickedCount == 0) return;
        if (Phase is not (RulesMovePhase.Pick or RulesMovePhase.Failed)) return;
        Phase = RulesMovePhase.Sending;
        Changed?.Invoke();
        var picked = _rows.Where(r => _picks.Contains(r.Id)).ToList();
        try
        {
            await client.ImportRulesAsync(RulesDiff.ImportBody(picked, cloud, _device)).ConfigureAwait(true);
            if (_rules() is { } rules) await rules.RefreshAsync().ConfigureAwait(true);
            Toast = $"Moved {picked.Count} {(picked.Count == 1 ? "rule" : "rules")} to the cloud. This PC now reads them from the cloud.";
            Close();
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Error = ex is RemoteException r ? r.UserMessage : ex.Message;
            Phase = RulesMovePhase.Failed;
            Changed?.Invoke();
        }
    }

    public void Close()
    {
        IsPresented = false;
        Phase = RulesMovePhase.Pick;
        Changed?.Invoke();
    }

    public void ClearToast()
    {
        Toast = null;
        Changed?.Invoke();
    }

    private async Task<RulesMovePhase> FetchAsync()
    {
        if (_client() is not { } client)
        {
            Error = "Sign in to Towertail Cloud first.";
            return RulesMovePhase.Failed;
        }
        try
        {
            var view = await client.GetRulesAsync().ConfigureAwait(true);
            _cloud = view;
            _rows = RulesDiff.Rows(_settings, _nodes.Nodes, view);
            _picks = _rows.Select(r => r.Id).ToHashSet();
            Error = null;
            return RulesMovePhase.Pick;
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            _rows = [];
            _picks = [];
            Error = ex is RemoteException r ? r.UserMessage : ex.Message;
            return RulesMovePhase.Failed;
        }
    }
}
