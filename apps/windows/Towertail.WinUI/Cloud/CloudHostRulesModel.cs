using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

/// <summary>
/// The rules of one Cloud host as the host rules view shows them. The cloud holds them.
/// Each edit goes to the cloud at once, one request after the other. Call it from the UI thread.
/// </summary>
public sealed class CloudHostRulesModel
{
    private readonly CloudRules _rules;
    private readonly Guid _nodeId;
    private readonly Func<CloudMe?> _me;
    private readonly Func<string?> _hostOs;
    private RemoteHostRules? _draft;
    private int _inflight;
    private Task _chain = Task.CompletedTask;

    public CloudHostRulesModel(CloudRules rules, Guid nodeId, Func<CloudMe?> me, Func<string?> hostOs)
    {
        _rules = rules;
        _nodeId = nodeId;
        _me = me;
        _hostOs = hostOs;
    }

    public event Action? Changed;

    public string? Problem { get; private set; }
    public bool CanEdit => _me()?.IsAdmin == true;
    public bool CanMute => _me()?.CanMute == true;
    public RemoteHostRules Host => _draft ?? _rules.Host(_nodeId) ?? new RemoteHostRules();
    public RemoteOrgRules Org => _rules.Org ?? new RemoteOrgRules();
    public bool IsMacHost => string.Equals(_hostOs(), "darwin", StringComparison.OrdinalIgnoreCase);

    /// <summary>Takes the rules of the cloud, unless a save is on its way.</summary>
    public void Reload()
    {
        if (_inflight == 0) _draft = _rules.Host(_nodeId);
        Changed?.Invoke();
    }

    public ThresholdPair OrgPair(ThresholdMetric m)
        => m == ThresholdMetric.MemPressure && IsMacHost ? Org.Thresholds.HealthOrDefault.MacMemPressure : Org.Thresholds.Get(m);

    public bool IsMuted(ThresholdMetric m) => Org.Muted.Contains(m.Signal()) || Host.Muted.Contains(m.Signal());

    public string OriginLine
    {
        get
        {
            if ((Host.Origin ?? Org.Origin) is not { } origin) return "Set on the web.";
            var from = origin.Client == "web" ? "the web" : origin.Source;
            return origin.At is { } at ? $"Set from {from} on {at.ToLocalTime():MMM d, yyyy}." : $"Set from {from}.";
        }
    }

    /// <summary>Applies a change to the draft and sends all host rules.</summary>
    public void Edit(Func<RemoteHostRules, RemoteHostRules> change)
    {
        if (!CanEdit) return;
        var next = change(Host);
        _draft = next;
        _inflight++;
        var previous = _chain;
        _chain = SaveAsync(previous, next);
        Changed?.Invoke();
    }

    public Task SetMuteAsync(AlertSignal signal, bool muted) => RunAsync(() => _rules.SetMuteAsync(signal, _nodeId, muted));

    /// <summary>Waits for the saves that are on their way.</summary>
    public Task IdleAsync() => _chain;

    private async Task SaveAsync(Task previous, RemoteHostRules next)
    {
        await previous.ConfigureAwait(true);
        try
        {
            await _rules.SaveHostAsync(_nodeId, next).ConfigureAwait(true);
            Problem = null;
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Problem = ex is RemoteException r ? r.UserMessage : ex.Message;
            _draft = _rules.Host(_nodeId);
        }
        _inflight--;
        Reload();
    }

    private async Task RunAsync(Func<Task> send)
    {
        try
        {
            await send().ConfigureAwait(true);
            Problem = null;
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Problem = ex is RemoteException r ? r.UserMessage : ex.Message;
        }
        Changed?.Invoke();
    }
}
