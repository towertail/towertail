using Towertail.WinUI.Backend;

namespace Towertail.WinUI.State;

/// <summary>
/// Adds a process name to an ignore list, saves it through the backend and pushes the new
/// list to each host at once. Mirrors the Mac <c>LongProcIgnore</c>.
/// </summary>
public sealed class LongProcIgnore(ServerSettings settings, NodeStore nodes, ServerStore servers, IBackend backend)
{
    /// <summary>Ignores <paramref name="name"/> on one host, or on all hosts when <paramref name="hostId"/> is null.</summary>
    public async Task IgnoreAsync(string name, Guid? hostId)
    {
        var lower = name.ToLowerInvariant();
        if (hostId is { } id)
        {
            if (nodes.ById(id) is not { } node || node.IgnoredProcs.Any(n => n.ToLowerInvariant() == lower)) return;
            await backend.UpdateNodeAsync(node with { IgnoredProcs = node.IgnoredProcs.Append(name).ToList() });
        }
        else
        {
            if (settings.LongProc.Ignore.Any(n => n.ToLowerInvariant() == lower)) return;
            settings.LongProc = settings.LongProc with { Ignore = settings.LongProc.Ignore.Append(name).ToList() };
            await backend.UpdateServerSettingsAsync();
        }
        foreach (var vm in servers.Servers)
            if (nodes.ById(vm.Node.Id) is { } n) vm.ApplyLongProc(n, settings.LongProc);
    }
}
