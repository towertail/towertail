using System.Text.Json;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

public enum InstallStep { Copy, Register, Start, Reboot, Sample }

public enum InstallState { Waiting, Running, Done, Failed }

public enum InstallLineKind { Waiting, Running, Done, Failed, Stayed }

/// <summary>The one line that the move window shows for a host.</summary>
public sealed record InstallLine(string Text, InstallLineKind Kind);

/// <summary>
/// Moves hosts from SSH polling to a sampler that reports to the cloud. It opens SSH only to
/// install, remove or check the sampler. Mirrors the Mac <c>CloudInstaller</c>. Call it from the
/// UI thread: the node updates run where the call continues.
/// </summary>
public sealed class CloudInstaller
{
    public const string StayedText = "Stays on this PC over SSH";
    public const string ReportingText = "Reports to Cloud";
    public static readonly TimeSpan SampleWait = TimeSpan.FromSeconds(90);
    public const int MaxParallel = 4;

    private readonly Func<RemoteClient?> _client;
    private readonly Func<Node, ISamplerSession> _connect;
    private readonly NodeStore _nodes;
    private readonly ViewLeaseController _lease;
    private readonly Func<Guid, DateTime, string?> _firstSample;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly Func<string, string?> _binary;
    private readonly Func<Task<CloudBilling?>> _billing;
    private readonly Func<string?> _planName;
    private readonly Func<DateTime> _now;
    private readonly object _gate = new();
    private readonly Dictionary<Guid, Dictionary<InstallStep, InstallState>> _rows = [];
    private readonly Dictionary<Guid, InstallStep> _active = [];
    private readonly Dictionary<Guid, string> _results = [];
    private readonly Dictionary<Guid, string> _notes = [];
    private readonly HashSet<Guid> _planFull = [];
    private readonly HashSet<Guid> _stayed = [];

    /// <param name="firstSample">The sampler version of the first summary that came after a date. Null when there is none.</param>
    /// <param name="billing">Reads the plan again. Null when the cloud has no billing.</param>
    public CloudInstaller(
        Func<RemoteClient?> client, Func<Node, ISamplerSession> connect, NodeStore nodes, ViewLeaseController lease,
        Func<Guid, DateTime, string?> firstSample, Func<TimeSpan, CancellationToken, Task>? delay = null,
        Func<string, string?>? binary = null, Func<Task<CloudBilling?>>? billing = null,
        Func<string?>? planName = null, Func<DateTime>? now = null)
    {
        _client = client;
        _connect = connect;
        _nodes = nodes;
        _lease = lease;
        _firstSample = firstSample;
        _delay = delay ?? Task.Delay;
        _binary = binary ?? BundledBinary;
        _billing = billing ?? (() => Task.FromResult<CloudBilling?>(null));
        _planName = planName ?? (() => null);
        _now = now ?? (() => DateTime.UtcNow);
    }

    /// <summary>Raised for each step change. It can run on any thread.</summary>
    public event Action<Guid, InstallStep, InstallState>? StepChanged;

    /// <summary>Raised for each change of a row, a result or a note. It can run on any thread.</summary>
    public event Action? Changed;

    /// <summary>The path of the sampler that the app bundles for a triple. Null when the app has none.</summary>
    public static string? BundledBinary(string triple)
    {
        var path = Path.Combine(AppContext.BaseDirectory, "Assets", "samplers", triple, "towertail-sampler");
        return File.Exists(path) ? path : null;
    }

    public InstallState StateOf(Guid host, InstallStep step)
    {
        lock (_gate) return _rows.TryGetValue(host, out var row) && row.TryGetValue(step, out var s) ? s : InstallState.Waiting;
    }

    public string? ResultOf(Guid host) { lock (_gate) return _results.GetValueOrDefault(host); }

    public string? NoteOf(Guid host) { lock (_gate) return _notes.GetValueOrDefault(host); }

    public bool IsPlanFull(Guid host) { lock (_gate) return _planFull.Contains(host); }

    public bool Stayed(Guid host) { lock (_gate) return _stayed.Contains(host); }

    /// <summary>The line of a host in the move window.</summary>
    public InstallLine LineOf(Guid host)
    {
        lock (_gate)
        {
            if (_stayed.Contains(host)) return new InstallLine(StayedText, InstallLineKind.Stayed);
            if (_results.TryGetValue(host, out var result))
                return new InstallLine(result, result == ReportingText ? InstallLineKind.Done : InstallLineKind.Failed);
            if (_active.TryGetValue(host, out var step) && _rows.TryGetValue(host, out var row) && row.GetValueOrDefault(step) == InstallState.Running)
                return new InstallLine(RunningText(step), InstallLineKind.Running);
            return new InstallLine("Waits for its turn…", InstallLineKind.Waiting);
        }
    }

    private static string RunningText(InstallStep step) => step switch
    {
        InstallStep.Copy => "Copies the sampler…",
        InstallStep.Register => "Registers the host…",
        InstallStep.Start => "Starts the service…",
        InstallStep.Reboot => "Sets the start at boot…",
        _ => "Waits for the first sample…",
    };

    /// <summary>The number of hosts that the plan can still take. Null means no cap.</summary>
    public async Task<int?> AvailableRoomAsync() => (await _billing().ConfigureAwait(false))?.Room;

    /// <summary>The plan name for the notes, for example "Free plan". "Plan" when it is not known.</summary>
    public string PlanLabel => _planName() is { Length: > 0 } name ? char.ToUpperInvariant(name[0]) + name[1..] + " plan" : "Plan";

    /// <summary>Installs on the first hosts that fit in the plan. The other hosts stay on SSH.</summary>
    public async Task RunAsync(IReadOnlyList<Node> hosts, CancellationToken ct = default)
    {
        var take = await AvailableRoomAsync().ConfigureAwait(true) is { } room ? hosts.Take(room).ToList() : hosts.ToList();
        foreach (var node in hosts.Skip(take.Count)) KeepOnSsh(node.Id);
        lock (_gate)
        {
            foreach (var node in take)
            {
                _rows[node.Id] = [];
                _results.Remove(node.Id);
                _notes.Remove(node.Id);
                _planFull.Remove(node.Id);
                _stayed.Remove(node.Id);
            }
        }
        Changed?.Invoke();
        await InParallelAsync(take, async node => { await InstallAsync(node, ct); return 0; });
    }

    /// <summary>
    /// Removes the samplers of linked SSH hosts and the hosts in the cloud. It gives the failures
    /// by host id. Cloud-only hosts stay untouched.
    /// </summary>
    public async Task<IReadOnlyDictionary<Guid, string>> LeaveAsync(IReadOnlyList<Node> hosts, CancellationToken ct = default)
    {
        var linked = hosts.Where(n => n.Kind == NodeKind.Ssh && n.Cloud is not null).ToList();
        var outcomes = await InParallelAsync(linked, async node =>
        {
            try { await RemoveAsync(node, ct); return (node.Id, (string?)null); }
            catch (Exception ex) when (ex is not OperationCanceledException) { return (node.Id, ex.Message); }
        });
        return outcomes.Where(o => o.Item2 is not null).ToDictionary(o => o.Item1, o => o.Item2!);
    }

    /// <summary>Stops 24/7 monitoring. The host leaves the cloud and its slot is free.</summary>
    public Task TurnOffAsync(Node node, CancellationToken ct = default) => RemoveAsync(node, ct);

    /// <summary>Reads the sampler status over SSH and saves it on the host link.</summary>
    public async Task<SamplerStatus> StatusAsync(Node node, CancellationToken ct = default)
    {
        var status = await WithLifecycleAsync(node, l => l.StatusAsync(ct));
        UpdateCloud(node.Id, link => link with { LastStatus = JsonSerializer.SerializeToElement(status) });
        return status;
    }

    /// <summary>Starts a stopped sampler with its saved config.</summary>
    public Task<string> RestartAsync(Node node, CancellationToken ct = default)
        => WithLifecycleAsync(node, l => l.RestartAsync(ct));

    /// <summary>Removes the sampler from the host (when SSH answers) and the host from the cloud.</summary>
    public async Task RemoveAsync(Node node, CancellationToken ct = default)
    {
        var client = _client() ?? throw SamplerLifecycleException.NotSignedIn();
        if (node.Kind == NodeKind.Ssh)
        {
            try
            {
                await using var session = _connect(node);
                await new SamplerLifecycle(session).RemoveAsync(ct);
            }
            catch (Exception ex) when (ex is not OperationCanceledException) { /* the host can be down: the cloud delete still runs */ }
        }
        await client.DeleteNodeAsync(node.Id, ct);
        UpdateNode(node.Id, n => n with { Cloud = null });
    }

    private async Task<T> WithLifecycleAsync<T>(Node node, Func<SamplerLifecycle, Task<T>> body)
    {
        await using var session = _connect(node);
        return await body(new SamplerLifecycle(session));
    }

    /// <summary>Runs <paramref name="work"/> on the hosts, <see cref="MaxParallel"/> at a time.</summary>
    private static async Task<List<T>> InParallelAsync<T>(IReadOnlyList<Node> hosts, Func<Node, Task<T>> work)
    {
        using var slots = new SemaphoreSlim(MaxParallel);
        var tasks = hosts.Select(async node =>
        {
            await slots.WaitAsync();
            try { return await work(node); }
            finally { slots.Release(); }
        }).ToList();
        return [.. await Task.WhenAll(tasks)];
    }

    private void KeepOnSsh(Guid id)
    {
        lock (_gate)
        {
            _rows.Remove(id);
            _notes.Remove(id);
            _planFull.Remove(id);
            _results[id] = StayedText;
            _stayed.Add(id);
        }
    }

    // One host.

    private async Task InstallAsync(Node node, CancellationToken ct)
    {
        var id = node.Id;
        if (_client() is not { } client)
        {
            Fail(id, InstallStep.Copy, SamplerLifecycleException.NotSignedIn());
            return;
        }
        Set(id, InstallStep.Copy, InstallState.Running);
        await using var session = _connect(node);
        var lifecycle = new SamplerLifecycle(session, client);

        try
        {
            var triple = await lifecycle.DetectAsync(ct);
            var file = _binary(triple) ?? throw new SamplerLifecycleException(LifecycleFailure.UnsupportedHost, $"No bundled sampler for {triple}");
            await lifecycle.InstallAsync(file, ct);
            Set(id, InstallStep.Copy, InstallState.Done);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Fail(id, InstallStep.Copy, ex);
            return;
        }

        Set(id, InstallStep.Register, InstallState.Running);
        EnrollResponse enrollment;
        try
        {
            enrollment = await lifecycle.EnrollAsync(node, ct);
            Set(id, InstallStep.Register, InstallState.Done);
        }
        catch (SamplerLifecycleException ex) when (ex.Kind == LifecycleFailure.PlanFull)
        {
            var text = PlanFullText(ex.PlanMax);
            Set(id, InstallStep.Register, InstallState.Failed);
            lock (_gate)
            {
                _results[id] = text;
                _notes[id] = "Live view over SSH still works";
                _planFull.Add(id);
            }
            Changed?.Invoke();
            return;
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Fail(id, InstallStep.Register, ex);
            return;
        }

        Set(id, InstallStep.Start, InstallState.Running);
        var since = _now();
        try
        {
            await lifecycle.StartAsync(
                Uri.TryCreate(enrollment.Endpoint, UriKind.Absolute, out var endpoint) ? endpoint : client.BaseUri,
                enrollment.SamplerToken, ct);
            Set(id, InstallStep.Start, InstallState.Done);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            Fail(id, InstallStep.Start, ex);
            return;
        }
        // The feed drops samples of a host with no link, so the link comes now.
        UpdateCloud(id, link => link);

        Set(id, InstallStep.Reboot, InstallState.Running);
        try
        {
            var mode = await lifecycle.EnableBootAsync(ct);
            Set(id, InstallStep.Reboot, InstallState.Done);
            UpdateCloud(id, link => link with { Reboot = mode });
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            // The sampler runs until the next reboot, so the install goes on.
            Set(id, InstallStep.Reboot, InstallState.Failed);
        }

        Set(id, InstallStep.Sample, InstallState.Running);
        // The cloud sends data only for leased hosts, and the popover can be closed.
        _lease.SetWatched(id, true);
        try
        {
            var waited = TimeSpan.Zero;
            var step = TimeSpan.FromSeconds(1);
            while (true)
            {
                if (_firstSample(id, since) is { } version)
                {
                    Set(id, InstallStep.Sample, InstallState.Done);
                    lock (_gate) _results[id] = ReportingText;
                    UpdateCloud(id, link => link with { SamplerVersion = version.Length > 0 ? version : link.SamplerVersion });
                    Changed?.Invoke();
                    return;
                }
                if (waited >= SampleWait) break;
                await _delay(step, ct);
                waited += step;
            }
            Set(id, InstallStep.Sample, InstallState.Failed);
            lock (_gate) _results[id] = "No sample yet. Check the sampler log.";
            Changed?.Invoke();
        }
        finally { _lease.SetWatched(id, false); }
    }

    private string PlanFullText(int? max)
        => max is { } m ? $"{PlanLabel} is full ({m} of {m})" : $"{PlanLabel} is full";

    private void Set(Guid id, InstallStep step, InstallState state)
    {
        lock (_gate)
        {
            if (!_rows.TryGetValue(id, out var row)) _rows[id] = row = [];
            row[step] = state;
            if (state == InstallState.Running) _active[id] = step;
        }
        StepChanged?.Invoke(id, step, state);
        Changed?.Invoke();
    }

    private void Fail(Guid id, InstallStep step, Exception error)
    {
        lock (_gate) _results[id] = error.Message;
        Set(id, step, InstallState.Failed);
    }

    private void UpdateCloud(Guid id, Func<CloudLink, CloudLink> edit)
        => UpdateNode(id, n => n with { Cloud = edit(n.Cloud ?? new CloudLink(_now())) });

    private void UpdateNode(Guid id, Func<Node, Node> edit)
    {
        if (_nodes.ById(id) is { } node) _nodes.Update(edit(node));
    }
}
