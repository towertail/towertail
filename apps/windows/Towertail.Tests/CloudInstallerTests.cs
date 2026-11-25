using System.Text;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudInstallerTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private RemoteClient? _client;
    private NodeStore _nodes = null!;
    private ViewLeaseController _lease = null!;
    private readonly List<LiveView> _views = [];
    private readonly Dictionary<Guid, FakeSamplerSession> _sessions = [];
    private readonly string _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
    private readonly string _binary = Path.Combine(Path.GetTempPath(), $"tt-bin-{Guid.NewGuid():N}");
    private Func<Guid, DateTime, string?> _firstSample = (_, _) => "1.0+abc";
    private CloudBilling? _billing;
    private Func<FakeSamplerSession, FakeSamplerSession> _tune = s => s;
    private int _enrollStatus = 200;
    private string _enrollBody = "";

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer { ExpectedToken = "tok" };
        _server.Handlers["POST /v1/sampler/enroll"] = ctx =>
        {
            if (_enrollStatus != 200) return LocalTestHttpServer.ReplyAsync(ctx, _enrollStatus, _enrollBody);
            return LocalTestHttpServer.ReplyAsync(ctx, 200,
                $$"""{"node_id":"{{Guid.NewGuid()}}","sampler_token":"stk-secret","endpoint":"https://cloud.example"}""");
        };
        _server.Handlers["DELETE /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 204);
        _client = new RemoteClient(new Uri(_server.BaseUrl), "tok");
        _nodes = new NodeStore(_settingsPath);
        _lease = new ViewLeaseController(v => { lock (_views) _views.Add(v); return Task.CompletedTask; }, new ManualDelay().Func);
        File.WriteAllBytes(_binary, Encoding.UTF8.GetBytes("binary"));
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _server.DisposeAsync();
        if (_client is not null) await _client.DisposeAsync();
        foreach (var p in new[] { _settingsPath, _binary })
            if (File.Exists(p)) File.Delete(p);
    }

    private CloudInstaller Installer(Func<TimeSpan, CancellationToken, Task>? delay = null, bool signedIn = true) => new(
        () => signedIn ? _client : null,
        node => _sessions[node.Id] = _tune(new FakeSamplerSession()),
        _nodes, _lease, (id, since) => _firstSample(id, since), delay ?? ((_, _) => Task.CompletedTask),
        _ => _binary, () => Task.FromResult(_billing), () => "free");

    private Node AddHost(string name)
    {
        var node = new Node { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = name + ".lan" };
        _nodes.Add(node);
        return node;
    }

    [Fact]
    public async Task InstallsAHostThroughAllSteps()
    {
        var host = AddHost("web");
        var installer = Installer();
        var steps = new List<(InstallStep, InstallState)>();
        installer.StepChanged += (_, s, st) => { lock (steps) steps.Add((s, st)); };

        await installer.RunAsync([host]);

        foreach (var step in Enum.GetValues<InstallStep>())
            installer.StateOf(host.Id, step).Should().Be(InstallState.Done);
        steps.Where(s => s.Item2 == InstallState.Running).Select(s => s.Item1)
            .Should().Equal(InstallStep.Copy, InstallStep.Register, InstallStep.Start, InstallStep.Reboot, InstallStep.Sample);
        installer.LineOf(host.Id).Should().Be(new InstallLine(CloudInstaller.ReportingText, InstallLineKind.Done));
        var link = _nodes.ById(host.Id)!.Cloud!;
        link.Reboot.Should().Be("systemd");
        link.SamplerVersion.Should().Be("1.0+abc");
        _sessions[host.Id].Disposed.Should().BeTrue();
    }

    [Fact]
    public async Task EnrollRequestCarriesNodeAndMachineId()
    {
        var host = AddHost("web");

        await Installer().RunAsync([host]);

        var body = _server.Requests.Single(r => r.Path == "/v1/sampler/enroll").Body;
        body.Should().Contain($"\"node_id\":\"{host.Id}\"").And.Contain("\"machine_id\":\"m-1\"");
    }

    [Fact]
    public async Task TokenNeverAppearsInACommand()
    {
        var host = AddHost("web");

        await Installer().RunAsync([host]);

        _sessions[host.Id].Commands.Should().NotContain(c => c.Contains("stk-secret"));
        _sessions[host.Id].Puts.Should().Contain(p => Encoding.UTF8.GetString(p.Data).Contains("stk-secret"));
    }

    [Fact]
    public async Task WatchesTheHostWhileItWaitsForTheFirstSample()
    {
        var host = AddHost("web");
        var calls = 0;
        _firstSample = (_, _) =>
        {
            if (++calls == 1)
            {
                lock (_views) _views.Should().Contain(new LiveView([host.Id]));
                return null;
            }
            return "1.0";
        };

        await Installer().RunAsync([host]);

        calls.Should().Be(2);
        _lease.Current.Should().Be(LiveView.Empty);
    }

    [Fact]
    public async Task TimesOutAfterNinetySeconds()
    {
        var host = AddHost("web");
        _firstSample = (_, _) => null;
        var waited = TimeSpan.Zero;
        var installer = Installer((span, _) => { waited += span; return Task.CompletedTask; });

        await installer.RunAsync([host]);

        waited.Should().Be(CloudInstaller.SampleWait);
        installer.StateOf(host.Id, InstallStep.Sample).Should().Be(InstallState.Failed);
        installer.LineOf(host.Id).Kind.Should().Be(InstallLineKind.Failed);
        _lease.Current.Should().Be(LiveView.Empty);
    }

    [Fact]
    public async Task RebootFailureDoesNotStopTheInstall()
    {
        var host = AddHost("web");
        _tune = s =>
        {
            s.Handler = c => c.EndsWith("boot enable --mode auto") ? (1, "", "no init") : FakeSamplerSession.Default(c);
            return s;
        };
        var installer = Installer();

        await installer.RunAsync([host]);

        installer.StateOf(host.Id, InstallStep.Reboot).Should().Be(InstallState.Failed);
        installer.LineOf(host.Id).Kind.Should().Be(InstallLineKind.Done);
    }

    [Fact]
    public async Task StartFailureStopsTheHost()
    {
        var host = AddHost("web");
        _tune = s =>
        {
            s.Handler = c => c.Contains(" start ") ? (1, "", "port busy") : FakeSamplerSession.Default(c);
            return s;
        };
        var installer = Installer();

        await installer.RunAsync([host]);

        installer.StateOf(host.Id, InstallStep.Start).Should().Be(InstallState.Failed);
        installer.StateOf(host.Id, InstallStep.Sample).Should().Be(InstallState.Waiting);
        installer.ResultOf(host.Id).Should().Contain("port busy");
    }

    [Fact]
    public async Task UnsupportedHostFailsAtTheFirstStep()
    {
        var host = AddHost("pc");
        _tune = s => { s.Handler = _ => (0, "Windows_NT", ""); return s; };
        var installer = Installer();

        await installer.RunAsync([host]);

        installer.StateOf(host.Id, InstallStep.Copy).Should().Be(InstallState.Failed);
        installer.ResultOf(host.Id).Should().Be("Not supported on this host");
    }

    [Fact]
    public async Task PlanFullKeepsTheHostOnSsh()
    {
        var host = AddHost("web");
        _enrollStatus = 402;
        _enrollBody = "the plan allows 3 hosts";
        var installer = Installer();

        await installer.RunAsync([host]);

        installer.IsPlanFull(host.Id).Should().BeTrue();
        installer.ResultOf(host.Id).Should().Be("Free plan is full (3 of 3)");
        installer.NoteOf(host.Id).Should().Be("Live view over SSH still works");
        _nodes.ById(host.Id)!.Cloud.Should().BeNull();
    }

    [Fact]
    public async Task OnlyTheHostsThatFitInTheRoomInstall()
    {
        var a = AddHost("a");
        var b = AddHost("b");
        _billing = new CloudBilling(new CloudPlan("free", 1), 0, 1);
        var installer = Installer();

        await installer.RunAsync([a, b]);

        installer.LineOf(a.Id).Kind.Should().Be(InstallLineKind.Done);
        installer.LineOf(b.Id).Should().Be(new InstallLine(CloudInstaller.StayedText, InstallLineKind.Stayed));
        installer.Stayed(b.Id).Should().BeTrue();
        _sessions.Should().NotContainKey(b.Id);
    }

    [Fact]
    public async Task SignedOutFailsEachHost()
    {
        var host = AddHost("web");
        var installer = Installer(signedIn: false);

        await installer.RunAsync([host]);

        installer.ResultOf(host.Id).Should().Be("Sign in to Towertail Cloud first.");
        _sessions.Should().BeEmpty();
    }

    [Fact]
    public async Task InstallsAtMostFourHostsAtOnce()
    {
        var hosts = Enumerable.Range(0, 6).Select(i => AddHost($"h{i}")).ToList();
        var gate = new TaskCompletionSource();
        var open = 0;
        var peak = 0;
        _tune = s =>
        {
            Interlocked.Increment(ref open);
            s.OnDispose = () => Interlocked.Decrement(ref open);
            s.Before = async c =>
            {
                lock (this) peak = Math.Max(peak, Volatile.Read(ref open));
                if (c.StartsWith("uname")) await gate.Task;
            };
            return s;
        };
        var run = Installer().RunAsync(hosts);

        var until = DateTime.UtcNow.AddSeconds(5);
        while (Volatile.Read(ref open) < CloudInstaller.MaxParallel && DateTime.UtcNow < until) await Task.Delay(10);
        await Task.Delay(100);
        Volatile.Read(ref open).Should().Be(CloudInstaller.MaxParallel);
        gate.SetResult();
        await run;

        peak.Should().Be(CloudInstaller.MaxParallel);
        hosts.Should().OnlyContain(h => _nodes.ById(h.Id)!.Cloud != null);
    }

    [Fact]
    public async Task LeaveRemovesTheSamplerAndTheCloudHost()
    {
        var host = AddHost("web");
        _nodes.Update(host with { Cloud = new CloudLink(DateTime.UtcNow) });
        var cloudOnly = Node.CloudOnly(Guid.NewGuid(), "db");
        _nodes.Add(cloudOnly);
        var installer = Installer();

        var failures = await installer.LeaveAsync(_nodes.Nodes.ToList());

        failures.Should().BeEmpty();
        _nodes.ById(host.Id)!.Cloud.Should().BeNull();
        _server.Requests.Where(r => r.Method == "DELETE").Select(r => r.Path).Should().Equal($"/v1/nodes/{host.Id}");
        _sessions[host.Id].Commands.Should().Contain("~/.towertail/towertail-sampler boot disable");
        _nodes.ById(cloudOnly.Id).Should().NotBeNull();
    }

    [Fact]
    public async Task LeaveStillDeletesTheCloudHostWhenSshFails()
    {
        var host = AddHost("web");
        _nodes.Update(host with { Cloud = new CloudLink(DateTime.UtcNow) });
        _tune = s => { s.Handler = _ => throw new IOException("down"); return s; };

        var failures = await Installer().LeaveAsync(_nodes.Nodes.ToList());

        failures.Should().BeEmpty();
        _nodes.ById(host.Id)!.Cloud.Should().BeNull();
    }

    [Fact]
    public async Task LeaveReportsAFailedCloudDelete()
    {
        var host = AddHost("web");
        _nodes.Update(host with { Cloud = new CloudLink(DateTime.UtcNow) });
        _server.Handlers["DELETE /v1/nodes/"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 500, "boom");

        var failures = await Installer().LeaveAsync(_nodes.Nodes.ToList());

        failures.Should().ContainKey(host.Id);
        _nodes.ById(host.Id)!.Cloud.Should().NotBeNull();
    }

    [Fact]
    public async Task TurnOffFreesTheSlot()
    {
        var host = AddHost("web");
        _nodes.Update(host with { Cloud = new CloudLink(DateTime.UtcNow) });

        await Installer().TurnOffAsync(_nodes.ById(host.Id)!);

        _nodes.ById(host.Id)!.Cloud.Should().BeNull();
    }

    [Fact]
    public async Task StatusSavesTheJsonOnTheLink()
    {
        var host = AddHost("web");
        _nodes.Update(host with { Cloud = new CloudLink(DateTime.UtcNow) });
        _tune = s => { s.Handler = _ => (0, """{"running":true,"version":"1.0"}""", ""); return s; };

        var status = await Installer().StatusAsync(_nodes.ById(host.Id)!);

        status.Running.Should().BeTrue();
        _nodes.ById(host.Id)!.Cloud!.LastStatus!.Value.GetProperty("running").GetBoolean().Should().BeTrue();
    }

    [Fact]
    public async Task AvailableRoomFollowsTheBilling()
    {
        _billing = new CloudBilling(new CloudPlan("free", 3), 1, 3);
        (await Installer().AvailableRoomAsync()).Should().Be(2);
        _billing = null;
        (await Installer().AvailableRoomAsync()).Should().BeNull();
    }
}
