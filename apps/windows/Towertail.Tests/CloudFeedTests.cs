using System.Net;
using System.Text.Json;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;
using static Towertail.WinUI.Backend.StreamMessage;

namespace Towertail.Tests;

public sealed class CloudFeedTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;
    private string _settingsPath = "";
    private string _historyPath = "";
    private string _prefsPath = "";
    private HistoryStore _history = null!;
    private NodeStore _nodes = null!;
    private ServerStore _servers = null!;
    private CloudPrefs _prefs = null!;
    private ReleasedHosts _released = null!;
    private RemoteClient _client = null!;
    private ManualDelay _delay = null!;
    private ViewLeaseController _lease = null!;
    private CloudFeed _feed = null!;
    private int _unauthorized;
    private List<RemoteNode> _remote = new();
    private string _alerts = """{"alerts":[]}""";

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _server.Handlers["GET /v1/stream"] = _server.AcceptWebSocketAsync;
        _server.Handlers["GET /v1/nodes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, JsonSerializer.Serialize(_remote));
        _server.Handlers["GET /v1/alerts"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, _alerts);
        _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _historyPath = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        _prefsPath = Path.Combine(Path.GetTempPath(), $"tt-c-{Guid.NewGuid():N}.json");
        _history = new HistoryStore(_historyPath);
        _nodes = new NodeStore(_settingsPath);
        _servers = new ServerStore(_nodes, new ServerSettings(_settingsPath), _history);
        _prefs = new CloudPrefs(_prefsPath);
        _released = new ReleasedHosts(_prefs);
        _client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
        _delay = new ManualDelay();
        _lease = new ViewLeaseController(v => _feed.SendAsync(v), _delay.Func);
        _feed = new CloudFeed(_client, _nodes, _servers, _released, _lease, () => _unauthorized++, null, null);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _feed.StopAsync();
        await _client.DisposeAsync();
        await _server.DisposeAsync();
        await _history.DisposeAsync();
        foreach (var p in new[] { _settingsPath, _historyPath, _prefsPath })
            if (File.Exists(p)) File.Delete(p);
    }

    private static RemoteNode Remote(Guid id, string name, bool enabled = true)
        => new(id, name, "ssh", "u", "h", null, enabled, false);

    private Node AddSsh(string name = "web")
    {
        var node = new Node { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
        _nodes.Add(node);
        return node;
    }

    private Node AddCloud(string name = "cloud")
    {
        var node = Node.CloudOnly(Guid.NewGuid(), name);
        _nodes.Add(node);
        return node;
    }

    private static async Task WaitUntil(Func<bool> condition)
    {
        var until = DateTime.UtcNow.AddSeconds(5);
        while (!condition())
        {
            if (DateTime.UtcNow > until) throw new TimeoutException();
            await Task.Delay(10);
        }
    }

    private async Task StartAndConnectAsync()
    {
        var connected = false;
        _feed.Connected += () => connected = true;
        _feed.Start();
        await WaitUntil(() => connected && _feed.IsConnected);
    }

    private static string Summary(Guid id, double cpu)
        => "{\"type\":\"summary\",\"nodeId\":\"" + id + "\",\"summary\":{\"ts\":\"2026-05-01T10:00:00Z\",\"cpu\":" + cpu + ",\"mem\":0.5,\"disk\":0.7,\"rx\":1,\"tx\":2,\"load\":0.5}}";

    private static Sample MakeSample(Node n) => new(
        1, DateTime.UtcNow,
        new HostInfo(n.DisplayName, "linux", "amd64", "6", 0, "s", null),
        new CpuInfo(33, 0, 0, 0, 8),
        new MemInfo(4_000_000_000, 16_000_000_000),
        new MemInfo(0, 0),
        Array.Empty<DiskSample>(),
        null, null, null, null, Array.Empty<string>());

    [Fact]
    public async Task SyncAddsCloudOnlyNodes()
    {
        var id = Guid.NewGuid();
        _remote = [Remote(id, "db")];

        await _feed.SyncNodesAsync();

        var node = _nodes.ById(id)!;
        node.Kind.Should().Be(NodeKind.Cloud);
        node.DisplayName.Should().Be("db");
        node.IsCloud.Should().BeTrue();
        _servers.Find(id).Should().NotBeNull();
    }

    [Fact]
    public async Task SyncRelinksReleasedHost()
    {
        var ssh = AddSsh();
        _released.Add(ssh.Id);
        _remote = [Remote(ssh.Id, "web")];

        await _feed.SyncNodesAsync();

        _nodes.ById(ssh.Id)!.Cloud.Should().NotBeNull();
        _nodes.ById(ssh.Id)!.Kind.Should().Be(NodeKind.Ssh);
        _released.Contains(ssh.Id).Should().BeFalse();
    }

    [Fact]
    public async Task SyncDoesNotLinkUnreleasedSshHost()
    {
        var ssh = AddSsh();
        _remote = [Remote(ssh.Id, "web")];

        await _feed.SyncNodesAsync();

        _nodes.ById(ssh.Id)!.Cloud.Should().BeNull();
    }

    [Fact]
    public async Task SyncDoesNotRelinkDisabledHost()
    {
        var ssh = AddSsh();
        _released.Add(ssh.Id);
        _remote = [Remote(ssh.Id, "web", enabled: false)];

        await _feed.SyncNodesAsync();

        _nodes.ById(ssh.Id)!.Cloud.Should().BeNull();
    }

    [Fact]
    public async Task ReleasedSetShrinks()
    {
        var back = AddSsh("back");
        var gone = AddSsh("gone");
        _released.AddRange([back.Id, gone.Id]);
        _remote = [Remote(back.Id, "back")];

        await _feed.SyncNodesAsync();

        _released.Ids.Should().BeEmpty();
    }

    [Fact]
    public async Task FailedListKeepsReleasedSet()
    {
        var ssh = AddSsh();
        _released.Add(ssh.Id);
        _server.Handlers["GET /v1/nodes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 500);

        await _feed.SyncNodesAsync();

        _released.Ids.Should().BeEquivalentTo([ssh.Id]);
        _unauthorized.Should().Be(0);
    }

    [Fact]
    public async Task SyncDropsRemovedLink()
    {
        var linked = new Node { DisplayName = "web", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", Cloud = new CloudLink(DateTime.UtcNow) };
        _nodes.Add(linked);
        var only = AddCloud();
        _remote = [];

        await _feed.SyncNodesAsync();

        _nodes.ById(linked.Id)!.Cloud.Should().BeNull();
        _nodes.ById(only.Id).Should().BeNull();
    }

    [Fact]
    public async Task UnauthorizedSyncSignsOut()
    {
        _server.Handlers["GET /v1/nodes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 401);

        await _feed.SyncNodesAsync();

        _unauthorized.Should().Be(1);
    }

    [Fact]
    public void ReachabilityEventSetsAlerted()
    {
        var cloud = AddCloud();

        _feed.Handle(new EventFrame(new StreamEvent(cloud.Id, "reachability", "critical", "reachability_changed")));
        _feed.Alerted.Should().BeEquivalentTo([cloud.Id]);

        _feed.Handle(new EventFrame(new StreamEvent(cloud.Id, "reachability", "ok", "reachability_changed")));
        _feed.Alerted.Should().BeEmpty();
    }

    [Fact]
    public void ReachabilityEventMarksOffline()
    {
        var cloud = AddCloud();
        var vm = _servers.Find(cloud.Id)!;

        _feed.Handle(new EventFrame(new StreamEvent(cloud.Id, "reachability", "critical", "reachability_changed")));
        vm.OfflineReason.Should().NotBeNull();

        _feed.Handle(new EventFrame(new StreamEvent(cloud.Id, "reachability", "ok", "reachability_changed")));
        vm.OfflineReason.Should().BeNull();
    }

    [Fact]
    public void OtherEventsReachSubscribersOnly()
    {
        var cloud = AddCloud();
        var seen = new List<StreamEvent>();
        _feed.EventReceived += seen.Add;

        _feed.Handle(new EventFrame(new StreamEvent(cloud.Id, "cpu", "warn", "alert_opened")));

        seen.Should().ContainSingle();
        _feed.Alerted.Should().BeEmpty();
    }

    [Fact]
    public async Task OpenAlertsSeedAlerted()
    {
        var a = AddCloud("a");
        var b = AddCloud("b");
        _alerts = $$"""{"alerts":[{"node_id":"{{a.Id}}","metric":"reachability","tint":"critical"},{"node_id":"{{b.Id}}","metric":"cpu","tint":"critical"}]}""";

        await StartAndConnectAsync();
        _remote = [Remote(a.Id, "a"), Remote(b.Id, "b")];

        await WaitUntil(() => _feed.Alerted.Count == 1);
        _feed.Alerted.Should().BeEquivalentTo([a.Id]);
    }

    [Fact]
    public async Task EventsBeforeTheAlertListWin()
    {
        var a = AddCloud("a");
        _alerts = $$"""{"alerts":[{"node_id":"{{a.Id}}","metric":"reachability","tint":"critical"}]}""";
        var gate = new TaskCompletionSource();
        _server.Handlers["GET /v1/alerts"] = async ctx => { await gate.Task; await LocalTestHttpServer.ReplyAsync(ctx, 200, _alerts); };

        await StartAndConnectAsync();
        _feed.Handle(new EventFrame(new StreamEvent(a.Id, "reachability", "ok", "reachability_changed")));
        gate.SetResult();
        await Task.Delay(200);

        _feed.Alerted.Should().BeEmpty();
    }

    [Fact]
    public async Task ConnectedFollowsTheStream()
    {
        _feed.IsConnected.Should().BeFalse();
        await StartAndConnectAsync();
        _feed.IsConnected.Should().BeTrue();

        await _server.CloseAllWebSocketsAsync();
        await WaitUntil(() => !_feed.IsConnected);
        await WaitUntil(() => _feed.IsConnected);

        await _feed.StopAsync();
        _feed.IsConnected.Should().BeFalse();
    }

    [Fact]
    public async Task ConnectSyncsNodes()
    {
        var id = Guid.NewGuid();
        _remote = [Remote(id, "db")];

        await StartAndConnectAsync();

        await WaitUntil(() => _nodes.ById(id) is not null);
    }

    [Fact]
    public void SummaryUpdatesCloudCardOnly()
    {
        var cloud = AddCloud();
        var local = _nodes.Nodes.First(n => n.Kind == NodeKind.Local);
        var ssh = AddSsh();

        foreach (var id in new[] { cloud.Id, ssh.Id, local.Id })
            _feed.Handle(StreamMessage.Decode(System.Text.Encoding.UTF8.GetBytes(Summary(id, 42)))!);

        _servers.Find(cloud.Id)!.CpuPct.Should().BeApproximately(42, 0.001);
        _servers.Find(ssh.Id)!.CpuPct.Should().BeNull();
        _servers.Find(local.Id)!.CpuPct.Should().BeNull();
    }

    [Fact]
    public void HelloSetsCloudLiveSource()
    {
        var cloud = AddCloud();
        var ssh = AddSsh();
        StreamHello? hello = null;
        _feed.Hello += h => hello = h;

        _feed.Handle(new HelloFrame(new StreamHello(true, 30, 120)));

        _servers.Find(cloud.Id)!.LiveSource.Should().Be(LiveSource.Cloud);
        _servers.Find(ssh.Id)!.LiveSource.Should().Be(LiveSource.Poll);
        hello.Should().Be(new StreamHello(true, 30, 120));
    }

    [Fact]
    public void NoHelloUsesSampleFrames()
    {
        var cloud = AddCloud();
        var ssh = AddSsh();

        _feed.Handle(new SampleFrame(cloud.Id, MakeSample(cloud)));
        _feed.Handle(new SampleFrame(ssh.Id, MakeSample(ssh)));

        _servers.Find(cloud.Id)!.CpuPct.Should().Be(33);
        _servers.Find(ssh.Id)!.CpuPct.Should().BeNull();
    }

    [Fact]
    public void NodeUpdatedRenamesCloudNodeOnly()
    {
        var cloud = AddCloud("old");
        var ssh = AddSsh("web");
        var updated = new List<Guid>();
        _feed.NodeUpdated += updated.Add;

        _feed.Handle(new NodeUpdatedFrame(Remote(cloud.Id, "new")));
        _feed.Handle(new NodeUpdatedFrame(Remote(ssh.Id, "renamed")));

        _nodes.ById(cloud.Id)!.DisplayName.Should().Be("new");
        _nodes.ById(ssh.Id)!.DisplayName.Should().Be("web");
        updated.Should().Equal(cloud.Id);
    }

    [Fact]
    public void SettingsAndDiskScanFramesReachSubscribers()
    {
        var settings = 0;
        var scans = new List<Guid>();
        _feed.SettingsUpdated += () => settings++;
        _feed.DiskScanUpdated += (id, _) => scans.Add(id);
        var thresholds = new RemoteThresholds(0.7, 0.9, 0.7, 0.9, 0.8, 0.95);

        _feed.Handle(new SettingsUpdatedFrame(new RemoteServerSettings(thresholds, 2, 10, true, true, true, 60, false, 15)));
        var node = Guid.NewGuid();
        _feed.Handle(new DiskScanUpdatedFrame(node, new DiskScanJob(Guid.NewGuid(), node, "running")));

        settings.Should().Be(1);
        scans.Should().Equal(node);
    }

    [Fact]
    public async Task LeaseLimitStopsRenew()
    {
        var sent = new List<LiveView>();
        var lease = new ViewLeaseController(v => { sent.Add(v); return Task.CompletedTask; }, _delay.Func);
        var feed = new CloudFeed(_client, _nodes, _servers, _released, lease, () => { }, null, null);
        lease.SetWatched(Guid.NewGuid(), true);
        _delay.Pending.Should().Contain(ViewLeaseController.RenewEvery);

        feed.Handle(new LeaseFrame(new LeaseNotice("ended", "limit")));

        _delay.Pending.Should().NotContain(ViewLeaseController.RenewEvery);
        await feed.StopAsync();
    }

    [Fact]
    public async Task ConnectResendsView()
    {
        var host = Guid.NewGuid();
        _lease.SetWatched(host, true);
        lock (_server.ClientFrames) _server.ClientFrames.Should().BeEmpty();

        await StartAndConnectAsync();

        await WaitUntil(() => { lock (_server.ClientFrames) return _server.ClientFrames.Count == 1; });
        _server.ClientFrames[0].Should().Be(new LiveView([host]).ToFrameJson());
    }

    [Fact]
    public async Task StopSetsPollLiveSource()
    {
        var cloud = AddCloud();
        _feed.Handle(new HelloFrame(new StreamHello(true, 30, 120)));
        _servers.Find(cloud.Id)!.LiveSource.Should().Be(LiveSource.Cloud);

        await _feed.StopAsync();

        _servers.Find(cloud.Id)!.LiveSource.Should().Be(LiveSource.Poll);
    }

    [Fact]
    public async Task StoppedFeedIgnoresFrames()
    {
        var cloud = AddCloud();
        await _feed.StopAsync();

        _feed.Handle(new HelloFrame(new StreamHello(true, 30, 120)));

        _servers.Find(cloud.Id)!.LiveSource.Should().Be(LiveSource.Poll);
    }

    [Fact]
    public async Task StoppedFeedAddsNoNodesFromAnOldSync()
    {
        var id = Guid.NewGuid();
        _remote = [Remote(id, "db")];
        await _feed.StopAsync();

        await _feed.SyncNodesAsync();

        _nodes.ById(id).Should().BeNull();
    }

    [Fact]
    public async Task MarshalRunsFrameChangesThroughTheDelegate()
    {
        var calls = 0;
        var feed = new CloudFeed(_client, _nodes, _servers, _released, _lease, () => { },
            a => { calls++; a(); return Task.CompletedTask; }, null);
        var cloud = AddCloud();

        feed.Handle(new HelloFrame(new StreamHello(true, 30, 120)));

        calls.Should().Be(1);
        _servers.Find(cloud.Id)!.LiveSource.Should().Be(LiveSource.Cloud);
        await feed.StopAsync();
    }
}
