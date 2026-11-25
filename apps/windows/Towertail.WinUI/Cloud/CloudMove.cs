using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

public enum MovePhase { Pick, Install }

/// <summary>
/// Moves SSH hosts that exist already to the cloud. It drives the "Move hosts to Cloud" window.
/// Mirrors the Mac <c>CloudMove</c>. Call it from the UI thread.
/// </summary>
public sealed class CloudMove
{
    private readonly NodeStore _nodes;
    private readonly CloudPrefs _prefs;
    private readonly Func<bool> _signedIn;
    private readonly Func<CloudBilling?> _billing;
    private List<Node> _hosts = [];

    public CloudMove(NodeStore nodes, CloudInstaller installer, CloudPrefs prefs, Func<bool> signedIn, Func<CloudBilling?> billing)
    {
        _nodes = nodes;
        Installer = installer;
        _prefs = prefs;
        _signedIn = signedIn;
        _billing = billing;
    }

    public CloudMove(CloudSession session, CloudInstaller installer, CloudPrefs prefs)
        : this(session.Nodes, installer, prefs, () => session.Account.IsSignedIn, () => session.Account.Billing) { }

    public CloudInstaller Installer { get; }

    public bool IsPresented { get; private set; }
    public MovePhase Phase { get; private set; } = MovePhase.Pick;
    public CloudPicks Picks { get; private set; } = new();

    /// <summary>The SSH hosts with no cloud link when the window opened.</summary>
    public IReadOnlyList<Node> Hosts => _hosts;

    /// <summary>Raised when the window must open or close, and when a pick or the phase changes.</summary>
    public event Action? Changed;

    public IReadOnlyList<Node> SshHosts => _nodes.Nodes.Where(n => n.Kind == NodeKind.Ssh && n.Cloud is null).ToList();

    public bool CanMove => _signedIn() && SshHosts.Count > 0;

    public bool IsFull => _billing()?.Room == 0;

    public IReadOnlyList<Node> Picked => _hosts.Where(n => Picks.Ids.Contains(n.Id)).ToList();

    public int NotPicked => _hosts.Count - Picked.Count;

    public string Note => CloudPicks.Note(Picks.Room, Installer.PlanLabel, (_billing()?.Nodes ?? 0) > 0);

    public async Task OpenAsync()
    {
        var list = SshHosts;
        var room = await Installer.AvailableRoomAsync();
        _hosts = [.. list];
        Picks = new CloudPicks(list, room);
        Phase = MovePhase.Pick;
        IsPresented = true;
        Changed?.Invoke();
    }

    /// <summary>Opens the window one time after the first sign-in, when SSH hosts exist and the plan has room.</summary>
    public async Task OfferOnceAsync()
    {
        if (_prefs.MoveOffered || !CanMove) return;
        if (await Installer.AvailableRoomAsync() == 0) return;
        _prefs.MoveOffered = true;
        _prefs.Save();
        await OpenAsync();
    }

    public void Toggle(Guid id)
    {
        Picks.Toggle(id);
        Changed?.Invoke();
    }

    public void Clear()
    {
        Picks.Clear();
        Changed?.Invoke();
    }

    public async Task StartAsync()
    {
        var list = Picked;
        if (Phase != MovePhase.Pick || list.Count == 0) return;
        Phase = MovePhase.Install;
        Changed?.Invoke();
        await Installer.RunAsync(list);
        await Installer.AvailableRoomAsync();
    }

    public void Close()
    {
        if (!IsPresented) return;
        IsPresented = false;
        Changed?.Invoke();
    }
}
