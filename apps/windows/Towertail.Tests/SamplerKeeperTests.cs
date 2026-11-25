using System.Text;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class SamplerKeeperTests : IDisposable
{
    private readonly string _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
    private readonly string _binary = Path.Combine(Path.GetTempPath(), $"tt-bin-{Guid.NewGuid():N}");
    private readonly NodeStore _nodes;
    private readonly List<FakeSamplerSession> _sessions = [];
    private DateTime _now = new(2026, 5, 1, 10, 0, 0, DateTimeKind.Utc);
    private string _status = """{"running":true,"version":"1.0+abc"}""";
    private SamplerManifest? _manifest;

    public SamplerKeeperTests()
    {
        _nodes = new NodeStore(_settingsPath);
        File.WriteAllBytes(_binary, Encoding.UTF8.GetBytes("binary"));
    }

    public void Dispose()
    {
        foreach (var p in new[] { _settingsPath, _binary })
            if (File.Exists(p)) File.Delete(p);
    }

    private SamplerKeeper Keeper => new(_nodes, _manifest, node =>
    {
        var s = new FakeSamplerSession();
        s.Handler = c => c.EndsWith(" status") ? (0, _status, "") : FakeSamplerSession.Default(c);
        _sessions.Add(s);
        return s;
    }, _ => _binary, () => _now);

    private Node Linked(string name = "web")
    {
        var node = new Node { DisplayName = name, Kind = NodeKind.Ssh, SshUser = "u", SshHost = name, Cloud = new CloudLink(_now) };
        _nodes.Add(node);
        return node;
    }

    [Fact]
    public async Task RestartsAStoppedSampler()
    {
        var node = Linked();
        _status = """{"running":false}""";

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        _sessions.Should().ContainSingle().Which.Commands.Should().Contain("~/.towertail/towertail-sampler start");
    }

    [Fact]
    public async Task LeavesARunningSamplerAlone()
    {
        var node = Linked();

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        _sessions.Single().Commands.Should().NotContain(c => c.EndsWith(" start"));
    }

    [Fact]
    public async Task SavesTheStatusOnTheLink()
    {
        var node = Linked();

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        _nodes.ById(node.Id)!.Cloud!.LastStatus!.Value.GetProperty("version").GetString().Should().Be("1.0+abc");
    }

    [Fact]
    public async Task VisitsAHostOnceInTenMinutes()
    {
        var node = Linked();
        var keeper = Keeper;
        var alerted = new HashSet<Guid> { node.Id };

        await keeper.HandleAsync(alerted);
        _now += TimeSpan.FromMinutes(5);
        await keeper.HandleAsync(alerted);
        _sessions.Should().HaveCount(1);

        _now += SamplerKeeper.Revisit;
        await keeper.HandleAsync(alerted);
        _sessions.Should().HaveCount(2);
    }

    [Fact]
    public async Task SkipsHostsThatAreNotLinkedSshHosts()
    {
        var plain = new Node { DisplayName = "plain", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
        var cloudOnly = Node.CloudOnly(Guid.NewGuid(), "db");
        _nodes.Add(plain);
        _nodes.Add(cloudOnly);

        await Keeper.HandleAsync(new HashSet<Guid> { plain.Id, cloudOnly.Id, Guid.NewGuid() });

        _sessions.Should().BeEmpty();
    }

    [Fact]
    public async Task AnUnreachableHostDoesNotThrow()
    {
        var node = Linked();
        var keeper = new SamplerKeeper(_nodes, null, _ => throw new IOException("down"), _ => _binary, () => _now);

        var act = () => keeper.HandleAsync(new HashSet<Guid> { node.Id });

        await act.Should().NotThrowAsync();
    }

    [Fact]
    public async Task RunsTheUpdateThatTheCloudOrders()
    {
        var node = Linked();
        _manifest = new SamplerManifest("1.1", "def", new Dictionary<string, string>());
        _status = """{"running":true,"version":"1.0+abc","update":"1.1"}""";

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        var session = _sessions.Single();
        session.Puts.Should().NotBeEmpty();
        session.Commands.Last().Should().Be("~/.towertail/towertail-sampler start");
    }

    [Fact]
    public async Task SkipsTheUpdateWhenTheVersionMatches()
    {
        var node = Linked();
        _manifest = new SamplerManifest("1.1", "def", new Dictionary<string, string>());
        _status = """{"running":true,"version":"1.1+def","update":"1.1"}""";

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        _sessions.Single().Puts.Should().BeEmpty();
    }

    [Fact]
    public async Task SkipsAnUpdateForAnotherVersion()
    {
        var node = Linked();
        _manifest = new SamplerManifest("1.1", "def", new Dictionary<string, string>());
        _status = """{"running":true,"version":"1.0+abc","update":"2.0"}""";

        await Keeper.HandleAsync(new HashSet<Guid> { node.Id });

        _sessions.Single().Puts.Should().BeEmpty();
    }
}
