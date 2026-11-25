using FluentAssertions;
using System.Text.Json.Nodes;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudRulesTests : IAsyncLifetime
{
    private static readonly Guid HostA = Guid.Parse("22222222-2222-2222-2222-222222222222");
    private readonly string _dir = Path.Combine(Path.GetTempPath(), $"tt-rules-{Guid.NewGuid():N}");
    private readonly string _fixture = File.ReadAllText(Path.Combine("TestData", "rules-view.json"));
    private LocalTestHttpServer _server = null!;
    private RemoteClient _client = null!;

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _client.DisposeAsync();
        await _server.DisposeAsync();
        if (Directory.Exists(_dir)) Directory.Delete(_dir, recursive: true);
    }

    private string CachePath => Path.Combine(_dir, "cloud-rules.json");

    private CloudRules Make(double gapSeconds = 0.05)
        => new(_client, "test-pc", new CloudRulesCache(CachePath), TimeSpan.FromSeconds(gapSeconds));

    private void ServeRules(int status = 200, string? body = null)
        => _server.Handlers["GET /v1/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, status, body ?? _fixture);

    private int Count(string method, string path) => _server.Requests.Count(r => r.Method == method && r.Path == path);

    private static async Task WaitForAsync(Func<bool> check)
    {
        var deadline = DateTime.UtcNow.AddSeconds(5);
        while (DateTime.UtcNow < deadline)
        {
            if (check()) return;
            await Task.Delay(20);
        }
        throw new TimeoutException("condition not met within 5 s");
    }

    [Fact]
    public async Task RefreshFillsEffective()
    {
        ServeRules();
        var rules = Make();
        rules.Effective(HostA).Should().BeNull();

        await rules.RefreshAsync();

        rules.Effective(HostA)!.Alerts.Cpu.SustainSeconds.Should().Be(60);
        rules.Org!.LongProc.CpuPct.Should().Be(50);
        rules.Host(HostA)!.IgnoredProcs.Should().Equal("pg_dump");
        rules.Error.Should().BeNull();
        rules.Effective(Guid.NewGuid()).Should().BeNull();
    }

    [Fact]
    public async Task FailedRefreshKeepsLastRules()
    {
        ServeRules();
        var rules = Make();
        await rules.RefreshAsync();
        ServeRules(500, "{}");

        await rules.RefreshAsync();

        rules.Error.Should().NotBeNull();
        rules.Effective(HostA).Should().NotBeNull();
    }

    [Fact]
    public async Task CacheLoadsBeforeFirstFetch()
    {
        ServeRules();
        await Make().RefreshAsync();

        var second = Make();

        second.Effective(HostA).Should().NotBeNull("the cache fills the rules before a fetch");
        second.Org!.Thresholds.CpuWarn.Should().Be(0.8);
    }

    [Fact]
    public async Task SaveHostStampsWindowsOrigin()
    {
        ServeRules();
        _server.Handlers[$"PUT /v1/nodes/{HostA}/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200);
        var rules = Make();
        var body = new RemoteHostRules
        {
            Thresholds = new ThresholdOverrides(Cpu: new ThresholdPair(0.5, 0.7)),
            Origin = new RemoteRulesOrigin("web"),
        };

        await rules.SaveHostAsync(HostA, body);

        var json = JsonNode.Parse(_server.Requests.Single(r => r.Method == "PUT").Body)!;
        json["origin"]!["client"]!.GetValue<string>().Should().Be("windows");
        json["origin"]!["device"]!.GetValue<string>().Should().Be("test-pc");
        json["thresholds"].Should().NotBeNull();
        json["muted"]!.AsArray().Should().BeEmpty();
        Count("GET", "/v1/rules").Should().Be(1, "a save reads the rules back");
    }

    [Fact]
    public async Task MuteBodies()
    {
        ServeRules();
        _server.Handlers["PUT /v1/mutes"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200);
        var rules = Make();

        await rules.SetMuteAsync(AlertSignal.Cpu, HostA, true);
        await rules.SetMuteAsync(AlertSignal.Long, null, false);

        var puts = _server.Requests.Where(r => r.Method == "PUT").Select(r => JsonNode.Parse(r.Body)!).ToList();
        puts.Should().HaveCount(2);
        puts[0]["metric"]!.GetValue<string>().Should().Be("cpu");
        puts[0]["nodeId"]!.GetValue<string>().Should().Be(HostA.ToString());
        puts[0]["muted"]!.GetValue<bool>().Should().BeTrue();
        puts[1]["metric"]!.GetValue<string>().Should().Be("long");
        puts[1]["nodeId"].Should().BeNull();
        puts[1]["muted"]!.GetValue<bool>().Should().BeFalse();
    }

    [Fact]
    public async Task ForbiddenGivesPlainMessage()
    {
        _server.Handlers[$"PUT /v1/nodes/{HostA}/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 403);

        var act = () => Make().SaveHostAsync(HostA, new RemoteHostRules());

        (await act.Should().ThrowAsync<RemoteException>()).Which.UserMessage.Should().Be("Only an org admin can change this.");
    }

    [Fact]
    public async Task RefreshSoonCoalescesCalls()
    {
        ServeRules();
        var rules = Make(gapSeconds: 0.2);
        await rules.RefreshAsync();

        for (var i = 0; i < 5; i++) rules.RefreshSoon();
        await WaitForAsync(() => Count("GET", "/v1/rules") >= 2);
        await Task.Delay(400);

        Count("GET", "/v1/rules").Should().Be(2);
    }

    [Fact]
    public async Task StopCancelsPendingFetch()
    {
        ServeRules();
        var rules = Make(gapSeconds: 0.3);
        await rules.RefreshAsync();

        rules.RefreshSoon();
        rules.Stop();
        await Task.Delay(600);

        Count("GET", "/v1/rules").Should().Be(1);
    }

    [Fact]
    public async Task EffectiveGivesCloudRulesForCloudNode()
    {
        ServeRules();
        var rules = Make();
        await rules.RefreshAsync();
        var path = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        var historyPath = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        var history = new HistoryStore(historyPath);
        try
        {
            var nodes = new NodeStore(path);
            var cloudNode = new Node { Id = HostA, DisplayName = "web", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "web", Cloud = new CloudLink(DateTime.UtcNow) };
            var sshNode = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "db" };
            nodes.Add(cloudNode);
            nodes.Add(sshNode);
            var servers = new ServerStore(nodes, new ServerSettings(path), history) { CloudRulesSource = rules };

            servers.Ingest(HostA, Sample(cloudNode));
            servers.Ingest(sshNode.Id, Sample(sshNode));

            var cloudVm = servers.Find(HostA)!;
            cloudVm.Thresholds.MemWarn.Should().Be(0.85, "the host sets memory in the cloud");
            cloudVm.Thresholds.CpuWarn.Should().Be(0.8, "the org sets CPU in the cloud");
            cloudVm.AlertRules.Cpu.SustainSeconds.Should().Be(60);
            cloudVm.Muted.Contains(AlertSignal.Long).Should().BeTrue();
            cloudVm.IgnoredProcs.Should().BeEquivalentTo(["backup"], "the long rule carries the ignore list");
            var sshVm = servers.Find(sshNode.Id)!;
            sshVm.Thresholds.CpuWarn.Should().Be(MetricThresholds.Defaults.CpuWarn, "an SSH host keeps the local rules");
        }
        finally
        {
            await history.DisposeAsync();
            if (File.Exists(path)) File.Delete(path);
            if (File.Exists(historyPath)) File.Delete(historyPath);
        }
    }

    private static Sample Sample(Node n) => new(
        1, DateTime.UtcNow,
        new HostInfo(n.DisplayName, "linux", "amd64", "6.1", 0, "s", null),
        new CpuInfo(10, 0, 0, 0, 8),
        new MemInfo(4_000_000_000, 16_000_000_000),
        new MemInfo(0, 0),
        [new DiskSample("/", "ext4", 100, 500), ],
        null, null, null, null, Array.Empty<string>());
}
