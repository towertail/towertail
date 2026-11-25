using System.Text.Json;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>Keeps the last rules of the cloud on disk. The app uses them until the first fetch ends.</summary>
public sealed class CloudRulesCache(string path)
{
    public RemoteRulesView? Load()
    {
        try { return File.Exists(path) ? JsonSerializer.Deserialize<RemoteRulesView>(File.ReadAllText(path), RemoteClient.WireJson) : null; }
        catch (Exception ex) when (ex is JsonException or IOException or UnauthorizedAccessException) { return null; }
    }

    public void Save(RemoteRulesView view)
    {
        try
        {
            if (Path.GetDirectoryName(path) is { Length: > 0 } dir) Directory.CreateDirectory(dir);
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, JsonSerializer.Serialize(view, RemoteClient.WireJson));
            File.Move(tmp, path, overwrite: true);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { }
    }

    public void Clear()
    {
        try { File.Delete(path); }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { }
    }
}

/// <summary>
/// The rules of the cloud: the org rules and the rules of each host. A cloud host uses these in
/// place of the local rules. Mirrors the Mac <c>CloudRules</c>. Thread-safe. <see cref="Changed"/>
/// can run on any thread.
/// </summary>
public sealed class CloudRules : ICloudRulesSource
{
    private readonly RemoteClient _client;
    private readonly string _device;
    private readonly CloudRulesCache? _cache;
    private readonly TimeSpan _gap;
    private readonly Logger? _log;
    private readonly object _gate = new();
    private RemoteRulesView? _view;
    private Dictionary<Guid, RemoteRulesHost> _byNode = [];
    private string? _error;
    private CancellationTokenSource? _pending;
    private DateTime _lastFetch = DateTime.MinValue;

    public CloudRules(RemoteClient client, string device, CloudRulesCache? cache = null, TimeSpan? gap = null, Logger? log = null)
    {
        _client = client;
        _device = device;
        _cache = cache;
        _gap = gap ?? TimeSpan.FromSeconds(2);
        _log = log;
        if (cache?.Load() is { } cached) Apply(cached);
    }

    /// <summary>Raised after the rules change.</summary>
    public event Action? Changed;

    public RemoteRulesView? View { get { lock (_gate) return _view; } }
    public RemoteOrgRules? Org => View?.Org;
    public string? Error { get { lock (_gate) return _error; } }

    public RemoteEffectiveRules? Effective(Guid nodeId) { lock (_gate) return _byNode.GetValueOrDefault(nodeId)?.Effective; }

    public RemoteHostRules? Host(Guid nodeId) { lock (_gate) return _byNode.GetValueOrDefault(nodeId)?.Host; }

    public async Task RefreshAsync(CancellationToken ct = default)
    {
        lock (_gate) _lastFetch = DateTime.UtcNow;
        try
        {
            var fresh = await _client.GetRulesAsync(ct).ConfigureAwait(false);
            Apply(fresh);
            _cache?.Save(fresh);
            lock (_gate) _error = null;
            Changed?.Invoke();
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            lock (_gate) _error = ex is RemoteException r ? r.UserMessage : ex.Message;
            _log?.Warning($"cloud: rules fetch failed: {ex.Message}");
        }
    }

    /// <summary>Fetches at once, or after the gap when the last fetch is recent. Calls during the wait share one fetch.</summary>
    public void RefreshSoon()
    {
        CancellationTokenSource cts;
        TimeSpan wait;
        lock (_gate)
        {
            if (_pending is not null) return;
            cts = _pending = new CancellationTokenSource();
            var since = DateTime.UtcNow - _lastFetch;
            wait = since >= _gap ? TimeSpan.Zero : _gap - since;
        }
        _ = Task.Run(async () =>
        {
            try
            {
                if (wait > TimeSpan.Zero) await Task.Delay(wait, cts.Token).ConfigureAwait(false);
                lock (_gate) _pending = null;
                await RefreshAsync(cts.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException) { }
        });
    }

    public void Stop()
    {
        CancellationTokenSource? cts;
        lock (_gate) { cts = _pending; _pending = null; }
        cts?.Cancel();
    }

    /// <summary>Deletes the cache file. Used at sign-out.</summary>
    public void ClearCache() => _cache?.Clear();

    /// <summary>Replaces the rules of a host. The origin names this app. Reads the rules back after.</summary>
    public async Task SaveHostAsync(Guid nodeId, RemoteHostRules rules, CancellationToken ct = default)
    {
        await _client.PutHostRulesAsync(nodeId, rules with { Origin = new RemoteRulesOrigin("windows", _device) }, ct).ConfigureAwait(false);
        await RefreshAsync(ct).ConfigureAwait(false);
    }

    /// <summary>Mutes or unmutes a signal for a host, or for the org when <paramref name="nodeId"/> is null.</summary>
    public async Task SetMuteAsync(AlertSignal signal, Guid? nodeId, bool muted, CancellationToken ct = default)
    {
        await _client.SetMuteAsync(signal, nodeId, muted, ct).ConfigureAwait(false);
        await RefreshAsync(ct).ConfigureAwait(false);
    }

    private void Apply(RemoteRulesView view)
    {
        var byNode = view.Hosts.GroupBy(h => h.NodeId).ToDictionary(g => g.Key, g => g.Last());
        lock (_gate) { _view = view; _byNode = byNode; }
    }
}
