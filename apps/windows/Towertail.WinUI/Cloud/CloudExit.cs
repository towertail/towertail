using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

public enum ExitPhase { Idle, Moving, Deleting, Done, Failed }

/// <summary>
/// Leaves the cloud: moves the hosts back to SSH, then signs out or deletes the cloud account.
/// Mirrors the Mac <c>CloudExit</c>. Call it from the UI thread.
/// </summary>
public sealed class CloudExit(CloudSession session, CloudInstaller installer, NodeStore nodes)
{
    public ExitPhase Phase { get; private set; }

    /// <summary>The text of a failure. Empty otherwise.</summary>
    public string Error { get; private set; } = "";

    /// <summary>The error text of each host that could not move back.</summary>
    public IReadOnlyDictionary<Guid, string> Failures { get; private set; } = new Dictionary<Guid, string>();

    public CloudDeletion? Deletion { get; private set; }

    public event Action? Changed;

    /// <summary>SSH hosts that report to the cloud.</summary>
    public IReadOnlyList<Node> LinkedHosts => nodes.Nodes.Where(n => n.Kind == NodeKind.Ssh && n.Cloud is not null).ToList();

    /// <summary>Hosts that only the cloud knows. They leave this PC when the user signs out.</summary>
    public IReadOnlyList<Node> CloudOnlyHosts => nodes.Nodes.Where(n => n.Kind == NodeKind.Cloud).ToList();

    public bool IsBusy => Phase is ExitPhase.Moving or ExitPhase.Deleting;

    public void Reset()
    {
        Set(ExitPhase.Idle);
        Failures = new Dictionary<Guid, string>();
    }

    /// <summary>Moves the hosts back, then signs out. A failure keeps the sign-in.</summary>
    public async Task LeaveAsync()
    {
        if (!await MoveHostsAsync()) return;
        await session.SignOutAsync();
        Set(ExitPhase.Done);
    }

    /// <summary>Signs out and leaves the hosts that did not move back in the cloud.</summary>
    public async Task SignOutAnywayAsync()
    {
        await session.SignOutAsync();
        Reset();
    }

    public async Task LoadDeletionAsync()
    {
        try
        {
            Deletion = await session.Account.DeletionAsync();
            if (Deletion is null) Fail("This Cloud server cannot delete accounts yet.");
        }
        catch (Exception ex) when (ex is not OperationCanceledException) { Fail(ex.Message); }
    }

    /// <summary>Moves the hosts back, then deletes the account. The samplers need the token to leave, so the hosts move first.</summary>
    public async Task DeleteAccountAsync(string email)
    {
        Set(ExitPhase.Moving);
        await LoadDeletionAsync();
        if (Phase == ExitPhase.Failed || Deletion is not { } deletion) return;
        if (deletion.Blocked)
        {
            var reasons = deletion.Orgs.Where(o => o.Reason is not null).Select(o => $"{o.Name}: {o.Reason}").ToList();
            Fail(reasons.Count == 0 ? "Cloud blocks the delete." : string.Join(' ', reasons));
            return;
        }
        if (!await MoveHostsAsync()) return;
        Set(ExitPhase.Deleting);
        try
        {
            await session.Account.DeleteAccountAsync(email);
            Set(ExitPhase.Done);
        }
        catch (Exception ex) when (ex is not OperationCanceledException) { Fail(ex.Message); }
    }

    private async Task<bool> MoveHostsAsync()
    {
        Set(ExitPhase.Moving);
        var hosts = LinkedHosts;
        Failures = await installer.LeaveAsync(hosts);
        if (Failures.Count == 0) return true;
        var names = hosts.Where(h => Failures.ContainsKey(h.Id)).Select(h => h.DisplayName).Order().ToList();
        Fail($"These hosts did not move back: {string.Join(", ", names)}.");
        return false;
    }

    private void Fail(string message)
    {
        Error = message;
        Set(ExitPhase.Failed);
    }

    private void Set(ExitPhase phase)
    {
        Phase = phase;
        if (phase != ExitPhase.Failed) Error = "";
        Changed?.Invoke();
    }
}
