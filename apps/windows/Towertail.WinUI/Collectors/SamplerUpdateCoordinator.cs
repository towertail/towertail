using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Collectors;

/// <summary>Uploads the bundled sampler to a node.</summary>
public interface ISamplerPusher
{
    /// <summary>Returns the triple of the uploaded binary.</summary>
    Task<string> PushAsync(Node node, CancellationToken ct = default);
}

public sealed class SshSamplerPusher : ISamplerPusher
{
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;

    public SshSamplerPusher(HostKeyPrompt? hostKeyPrompt = null, Action<Guid, string>? onTrust = null)
    {
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
    }

    public Task<string> PushAsync(Node node, CancellationToken ct = default)
        => new SshBootstrap(node, _hostKeyPrompt, _onTrust).PushUpdateAsync(ct);
}

/// <summary>
/// Pushes the bundled sampler to an SSH node when the <c>host.sampler</c> field of a
/// good sample differs from <see cref="SamplerManifest.ExpectedSamplerField"/>.
/// Same rule as SamplerUpdateCoordinator.swift.
/// </summary>
public sealed class SamplerUpdateCoordinator
{
    public static readonly TimeSpan CooldownSuccess = TimeSpan.FromSeconds(15);
    public static readonly TimeSpan CooldownFailure = TimeSpan.FromSeconds(300);

    private readonly SamplerManifest? _manifest;
    private readonly ISamplerPusher _pusher;
    private readonly Logger? _logger;
    private readonly Func<DateTime> _now;
    private readonly object _lock = new();
    private readonly HashSet<Guid> _updating = new();
    private readonly Dictionary<Guid, DateTime> _lastAttempt = new();
    private readonly Dictionary<Guid, string> _lastError = new();

    public SamplerUpdateCoordinator(
        SamplerManifest? manifest,
        ISamplerPusher pusher,
        Logger? logger = null,
        Func<DateTime>? now = null)
    {
        _manifest = manifest;
        _pusher = pusher;
        _logger = logger;
        _now = now ?? (() => DateTime.UtcNow);
    }

    public bool IsUpdating(Guid id)
    {
        lock (_lock) return _updating.Contains(id);
    }

    public string? LastError(Guid id)
    {
        lock (_lock) return _lastError.GetValueOrDefault(id);
    }

    /// <summary>
    /// Call after each good sample. Returns the push task, or a completed task
    /// when no push starts. Callers do not need to await it.
    /// </summary>
    public Task MaybeUpdateAsync(Node node, string reportedSampler, bool enabled)
    {
        if (!enabled || node.Kind != NodeKind.Ssh || _manifest is null) return Task.CompletedTask;
        if (string.IsNullOrEmpty(reportedSampler)) return Task.CompletedTask;
        if (string.IsNullOrEmpty(node.SshUser) || string.IsNullOrEmpty(node.SshHost)) return Task.CompletedTask;

        var expected = _manifest.ExpectedSamplerField;
        lock (_lock)
        {
            if (reportedSampler == expected)
            {
                _lastError.Remove(node.Id);
                return Task.CompletedTask;
            }
            if (_updating.Contains(node.Id)) return Task.CompletedTask;
            if (_lastAttempt.TryGetValue(node.Id, out var last))
            {
                var cooldown = _lastError.ContainsKey(node.Id) ? CooldownFailure : CooldownSuccess;
                if (_now() - last < cooldown) return Task.CompletedTask;
            }
            _updating.Add(node.Id);
            _lastAttempt[node.Id] = _now();
        }

        _logger?.Info($"auto-update[{node.DisplayName}]: push starting from={reportedSampler} to={expected}");
        return PushAsync(node, expected);
    }

    /// <summary>
    /// Before a disk scan: push the bundled sampler now when the host has another version,
    /// so that the host knows <c>scan</c>. No cooldown, because the user asked for the scan.
    /// Throws when the push fails.
    /// </summary>
    public async Task EnsureCurrentAsync(Node node, string reportedSampler, CancellationToken ct = default)
    {
        if (node.Kind != NodeKind.Ssh || _manifest is null) return;
        var expected = _manifest.ExpectedSamplerField;
        if (reportedSampler == expected) return;

        var waited = false;
        while (true)
        {
            lock (_lock)
            {
                // A push that was in progress and did not fail made the host current.
                if (waited && !_updating.Contains(node.Id) && !_lastError.ContainsKey(node.Id)) return;
                if (!_updating.Contains(node.Id))
                {
                    _updating.Add(node.Id);
                    _lastAttempt[node.Id] = _now();
                    break;
                }
            }
            waited = true;
            await Task.Delay(500, ct).ConfigureAwait(false);
        }

        _logger?.Info($"scan-update[{node.DisplayName}]: push starting from={reportedSampler} to={expected}");
        try
        {
            await _pusher.PushAsync(node, ct).ConfigureAwait(false);
            lock (_lock) _lastError.Remove(node.Id);
        }
        catch (Exception ex)
        {
            lock (_lock) _lastError[node.Id] = ex.Message;
            _logger?.Warning($"scan-update[{node.DisplayName}]: push failed: {ex.Message}");
            throw;
        }
        finally
        {
            lock (_lock) _updating.Remove(node.Id);
        }
    }

    private async Task PushAsync(Node node, string expected)
    {
        try
        {
            var triple = await _pusher.PushAsync(node).ConfigureAwait(false);
            lock (_lock) _lastError.Remove(node.Id);
            _logger?.Info($"auto-update[{node.DisplayName}]: push succeeded triple={triple} to={expected}");
        }
        catch (Exception ex)
        {
            lock (_lock) _lastError[node.Id] = ex.Message;
            _logger?.Warning($"auto-update[{node.DisplayName}]: push failed: {ex.Message}");
        }
        finally
        {
            lock (_lock) _updating.Remove(node.Id);
        }
    }
}
