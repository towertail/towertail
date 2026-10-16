using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class DiskScanStoreTests : IDisposable
{
    private readonly string _path = Path.Combine(Path.GetTempPath(), $"tt-disk-scans-{Guid.NewGuid():N}.sqlite");
    private readonly DiskScanStore _store;

    public DiskScanStoreTests() { _store = new DiskScanStore(_path); }

    public void Dispose()
    {
        _store.Dispose();
        foreach (var f in new[] { _path, _path + "-wal", _path + "-shm" }) File.Delete(f);
    }

    [Fact]
    public void KeepsTheNewestThreePerNodeAndScope()
    {
        var node = Guid.NewGuid();
        var other = Guid.NewGuid();
        var baseResult = ScanFixture.Result("linux");
        for (var i = 0; i < 5; i++)
            _store.Save(node, "/", baseResult with { StartedTs = baseResult.StartedTs.AddHours(i), Hostname = $"h{i}" });
        _store.Save(node, "~", baseResult);
        _store.Save(other, "/", baseResult);

        var list = _store.List(node, "/");
        list.Should().HaveCount(DiskScanStore.KeepPerScope);
        list.Select(e => e.Result.Hostname).Should().Equal("h4", "h3", "h2");
        list[0].FinishedAt.Should().BeCloseTo(baseResult.FinishedAt.AddHours(4), TimeSpan.FromMilliseconds(5));
        _store.List(node, "~").Should().ContainSingle();
        _store.List(other, "/").Should().ContainSingle();
        _store.Latest(node, "/")!.Result.Hostname.Should().Be("h4");
        _store.Latest(Guid.NewGuid(), "/").Should().BeNull();
    }

    [Fact]
    public void ResultsSurviveReopen()
    {
        var node = Guid.NewGuid();
        var result = ScanFixture.Result("windows");
        _store.Save(node, "~", result);
        using var again = new DiskScanStore(_path);
        var back = again.Latest(node, "~")!.Result;
        back.Recommendations.Select(r => r.Id).Should().Equal(result.Recommendations.Select(r => r.Id));
        back.Tree.B.Should().Be(result.Tree.B);
    }
}
