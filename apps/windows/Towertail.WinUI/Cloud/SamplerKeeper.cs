using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>
/// Restarts a stopped sampler on a cloud host that the cloud marks as alerted, and runs the
/// update that the cloud orders. It replaces the SSH push for hosts with a cloud link.
/// </summary>
public sealed class SamplerKeeper(
    NodeStore nodes, SamplerManifest? manifest, Func<Node, ISamplerSession> connect,
    Func<string, string?>? binary = null, Func<DateTime>? now = null, Func<Action, Task>? marshal = null, Logger? log = null)
{
    public static readonly TimeSpan Revisit = TimeSpan.FromSeconds(600);

    private readonly Func<string, string?> _binary = binary ?? CloudInstaller.BundledBinary;
    private readonly Func<DateTime> _now = now ?? (() => DateTime.UtcNow);
    private readonly Dictionary<Guid, DateTime> _lastVisit = [];
    private readonly object _gate = new();

    /// <summary>One try for each host in each 10 minutes.</summary>
    public async Task HandleAsync(IReadOnlySet<Guid> alerted)
    {
        foreach (var id in alerted)
        {
            if (nodes.ById(id) is not { Cloud: not null, Kind: NodeKind.Ssh } node
                || string.IsNullOrEmpty(node.SshUser) || string.IsNullOrEmpty(node.SshHost)) continue;
            lock (_gate)
            {
                if (_lastVisit.TryGetValue(id, out var last) && _now() - last < Revisit) continue;
                _lastVisit[id] = _now();
            }
            await VisitAsync(node).ConfigureAwait(false);
        }
    }

    private async Task VisitAsync(Node node)
    {
        try
        {
            await using var session = connect(node);
            var lifecycle = new SamplerLifecycle(session);
            var status = await lifecycle.StatusAsync().ConfigureAwait(false);
            if (!status.Running)
            {
                await lifecycle.RestartAsync().ConfigureAwait(false);
                log?.Info($"cloud: restarted the sampler on {node.DisplayName}");
                status = await TryStatusAsync(lifecycle) ?? status;
            }
            else if (!string.IsNullOrEmpty(status.LastError))
            {
                log?.Warning($"cloud: sampler on {node.DisplayName} cannot reach the cloud: {status.LastError}");
            }
            await SaveAsync(node.Id, status).ConfigureAwait(false);
            await ApplyUpdateAsync(status, lifecycle).ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            log?.Warning($"cloud: keeper failed on {node.DisplayName}: {ex.Message}");
        }
    }

    private static async Task<SamplerStatus?> TryStatusAsync(SamplerLifecycle lifecycle)
    {
        try { return await lifecycle.StatusAsync().ConfigureAwait(false); }
        catch (SamplerLifecycleException) { return null; }
    }

    /// <summary>Runs the update when the cloud asks for the version that the app bundles.</summary>
    private async Task ApplyUpdateAsync(SamplerStatus status, SamplerLifecycle lifecycle)
    {
        if (string.IsNullOrEmpty(status.Update) || manifest is null) return;
        var have = manifest.ExpectedSamplerField;
        if (!(status.Update == have || have.StartsWith(status.Update + "+", StringComparison.Ordinal)) || status.Version == have) return;
        var triple = await lifecycle.DetectAsync().ConfigureAwait(false);
        if (_binary(triple) is not { } file) return;
        await lifecycle.UpdateAsync(file).ConfigureAwait(false);
    }

    private Task SaveAsync(Guid id, SamplerStatus status)
    {
        void Save()
        {
            if (nodes.ById(id) is not { Cloud: { } link } node) return;
            nodes.Update(node with { Cloud = link with { LastStatus = System.Text.Json.JsonSerializer.SerializeToElement(status) } });
        }
        return marshal is null ? RunNow(Save) : marshal(Save);
    }

    private static Task RunNow(Action action)
    {
        action();
        return Task.CompletedTask;
    }
}
