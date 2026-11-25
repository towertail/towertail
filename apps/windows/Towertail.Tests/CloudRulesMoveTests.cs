using FluentAssertions;
using System.Text.Json.Nodes;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudRulesMoveTests : IAsyncLifetime
{
    private readonly string _dir = Path.Combine(Path.GetTempPath(), $"tt-move-{Guid.NewGuid():N}");
    private readonly string _fixture = File.ReadAllText(Path.Combine("TestData", "rules-view.json"));
    private LocalTestHttpServer _server = null!;
    private RemoteClient _client = null!;
    private ServerSettings _settings = null!;
    private NodeStore _nodes = null!;
    private CloudPrefs _prefs = null!;
    private bool _admin = true;

    public ValueTask InitializeAsync()
    {
        Directory.CreateDirectory(_dir);
        _server = new LocalTestHttpServer();
        _client = new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken);
        _settings = new ServerSettings(Path.Combine(_dir, "settings.json"));
        _nodes = new NodeStore(Path.Combine(_dir, "nodes.json"));
        _prefs = new CloudPrefs(Path.Combine(_dir, "cloud.json"));
        _server.Handlers["GET /v1/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, _fixture);
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _client.DisposeAsync();
        await _server.DisposeAsync();
        if (Directory.Exists(_dir)) Directory.Delete(_dir, recursive: true);
    }

    private CloudRulesMove Make(CloudRules? rules = null)
        => new(_settings, _nodes, _prefs, () => rules, () => _client, () => _admin, "test-pc");

    /// <summary>The org of the fixture has other rules, so the local rules differ.</summary>
    private void SetLocalCpu() => _settings.Thresholds = _settings.Thresholds with { CpuWarn = 0.7, CpuCritical = 0.9 };

    [Fact]
    public async Task OpenLoadsRowsAndPicksAll()
    {
        SetLocalCpu();
        var move = Make();
        var phases = new List<RulesMovePhase>();
        move.Changed += () => phases.Add(move.Phase);

        await move.OpenAsync();

        move.IsPresented.Should().BeTrue();
        move.Phase.Should().Be(RulesMovePhase.Pick);
        phases.First().Should().Be(RulesMovePhase.Loading);
        move.Rows.Should().NotBeEmpty();
        move.PickedCount.Should().Be(move.Rows.Count);
    }

    [Fact]
    public async Task ToggleChangesPickedCount()
    {
        SetLocalCpu();
        var move = Make();
        await move.OpenAsync();
        var all = move.PickedCount;

        move.Toggle(move.Rows[0].Id);
        move.PickedCount.Should().Be(all - 1);
        move.Toggle(move.Rows[0].Id);
        move.PickedCount.Should().Be(all);
    }

    [Fact]
    public async Task StartSendsPickedRowsAndSetsToast()
    {
        SetLocalCpu();
        _server.Handlers["POST /v1/rules/import"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, $$"""{"backupId":"{{Guid.NewGuid()}}","hosts":0,"org":true}""");
        var move = Make();
        await move.OpenAsync();
        var count = move.Rows.Count;

        await move.StartAsync();

        var req = _server.Requests.Single(r => r.Method == "POST");
        var json = JsonNode.Parse(req.Body)!;
        json["client"]!.GetValue<string>().Should().Be("windows");
        json["device"]!.GetValue<string>().Should().Be("test-pc");
        json["org"]!["thresholds"]!["cpuWarn"]!.GetValue<double>().Should().Be(0.7);
        move.IsPresented.Should().BeFalse();
        move.Toast.Should().Be($"Moved {count} {(count == 1 ? "rule" : "rules")} to the cloud. This PC now reads them from the cloud.");
    }

    [Fact]
    public async Task StartDoesNothingWhenNothingPicked()
    {
        SetLocalCpu();
        var move = Make();
        await move.OpenAsync();
        foreach (var id in move.Picks.ToList()) move.Toggle(id);

        await move.StartAsync();

        _server.Requests.Count(r => r.Method == "POST").Should().Be(0);
    }

    [Fact]
    public async Task FailedImportKeepsWindowOpenWithMessage()
    {
        SetLocalCpu();
        _server.Handlers["POST /v1/rules/import"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 403);
        var move = Make();
        await move.OpenAsync();

        await move.StartAsync();

        move.Phase.Should().Be(RulesMovePhase.Failed);
        move.IsPresented.Should().BeTrue();
        move.Error.Should().NotBeNullOrEmpty();
        move.Toast.Should().BeNull();
    }

    [Fact]
    public async Task FailedLoadGivesFailedPhase()
    {
        _server.Handlers["GET /v1/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 500, "{}");
        var move = Make();

        await move.OpenAsync();

        move.Phase.Should().Be(RulesMovePhase.Failed);
        move.Rows.Should().BeEmpty();
        move.Error.Should().NotBeNullOrEmpty();
    }

    [Fact]
    public async Task OfferOnceOpensOnceWhenRulesDiffer()
    {
        SetLocalCpu();
        var move = Make();

        await move.OfferOnceAsync();

        move.IsPresented.Should().BeTrue();
        _prefs.RulesMoveOffered.Should().BeTrue();
        new CloudPrefs(Path.Combine(_dir, "cloud.json")).RulesMoveOffered.Should().BeTrue();

        move.Close();
        await move.OfferOnceAsync();
        move.IsPresented.Should().BeFalse("the offer shows one time");
    }

    [Fact]
    public async Task OfferOnceStaysClosedWithoutDifference()
    {
        _server.Handlers["GET /v1/rules"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, """{"org":{},"hosts":[]}""");
        var move = Make();

        await move.OfferOnceAsync();

        move.IsPresented.Should().BeFalse();
        _prefs.RulesMoveOffered.Should().BeFalse();
    }

    [Fact]
    public async Task CanMoveFollowsAdminRole()
    {
        SetLocalCpu();
        var move = Make();
        _admin = false;

        move.CanMove.Should().BeFalse();
        await move.OfferOnceAsync();
        move.IsPresented.Should().BeFalse();

        _admin = true;
        move.CanMove.Should().BeTrue();
    }

    [Fact]
    public async Task ClearToastRaisesChanged()
    {
        SetLocalCpu();
        _server.Handlers["POST /v1/rules/import"] = ctx => LocalTestHttpServer.ReplyAsync(ctx, 200, $$"""{"backupId":"{{Guid.NewGuid()}}","hosts":0,"org":true}""");
        var move = Make();
        await move.OpenAsync();
        await move.StartAsync();
        var raised = 0;
        move.Changed += () => raised++;

        move.ClearToast();

        move.Toast.Should().BeNull();
        raised.Should().Be(1);
    }
}
