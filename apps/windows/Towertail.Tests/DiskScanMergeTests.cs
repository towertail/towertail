using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class DiskScanMergeTests
{
    private static readonly DateTime Now = new(2026, 10, 16, 9, 0, 0, DateTimeKind.Utc);
    private static readonly string[] Temp = { "AppData\\Local", "Temp" };

    private static DiskScanResult Sub(DiskScanResult like, string root, long bytes, IReadOnlyList<DiskNode>? kids = null,
        IReadOnlyList<DiskRec>? recs = null)
        => like with
        {
            Root = root,
            ScannedBytes = bytes,
            Tree = new DiskNode(root, bytes, "other", C: kids),
            Recs = recs ?? Array.Empty<DiskRec>(),
            Errors = null,
            Limits = null,
        };

    private static DiskRec Rec(string id, string path, long bytes, params string[] nodes)
        => new(id, DiskRec.SafeGroup, id, path, bytes, false, nodes, new DiskRecCmd(new[] { "rm -rf " + path }, null));

    [Fact]
    public void PathsAndRefreshRequest()
    {
        var w = ScanFixture.Result("windows");
        w.AbsolutePath(Array.Empty<string>()).Should().Be("C:\\Users\\dev");
        w.AbsolutePath(Temp).Should().Be("C:\\Users\\dev\\AppData\\Local\\Temp");
        w.NamesOf("C:\\Users\\dev\\AppData\\Local\\Temp").Should().Equal(Temp);
        w.NamesOf("C:\\Users\\dev\\AppData\\Local\\Docker\\wsl\\disk\\docker_data.vhdx")
            .Should().Equal("AppData\\Local", "Docker\\wsl\\disk", "docker_data.vhdx");
        w.NamesOf("C:\\Users\\dev").Should().BeEmpty();
        w.NamesOf("C:\\Users\\dev\\AppData").Should().BeNull();
        w.NamesOf("C:\\Users\\devx\\AppData").Should().BeNull();
        w.DisplayPath("C:\\Users\\dev\\AppData").Should().Be("%USERPROFILE%\\AppData");
        (w with { Root = "C:\\" }).AbsolutePath(new[] { "Users" }).Should().Be("C:\\Users");

        var req = w.RefreshRequest(Temp);
        req.Root.Should().Be("C:\\Users\\dev\\AppData\\Local\\Temp");
        req.MaxDepth.Should().Be(3);
        req.MinBytes.Should().Be(50_000_000);
        req.MinFrac.Should().Be(0);
        (w with { Limits = null }).RefreshRequest(new[] { "Downloads" }).MaxDepth.Should().Be(5);
    }

    [Fact]
    public void MergeReplacesFolderAndMovesSizeUp()
    {
        var w = ScanFixture.Result("windows");
        var root = "C:\\Users\\dev\\AppData\\Local\\Temp";
        var s = Sub(w, root, 500_000_000, new[] { new DiskNode("setup-log.txt", 500_000_000, "other", "n1", F: true) },
            new[] { Rec("temp", "%USERPROFILE%\\AppData\\Local\\Temp\\*", 500_000_000, "n1") });
        var m = w.Merging(s, Temp, Now)!;
        const long delta = 500_000_000 - 1_500_000_000;

        m.Tree.B.Should().Be(w.Tree.B + delta);
        m.ScannedBytes.Should().Be(w.ScannedBytes + delta);
        m.Files.Should().Be(w.Files);
        var local = m.Tree.NodeAt(new[] { "AppData\\Local" })!;
        local.B.Should().Be(17_280_000_000 + delta);
        local.Children.Select(c => c.N).Should().Equal("Docker\\wsl\\disk", "npm-cache\\_cacache\\content-v2", "Temp", "CrashDumps");
        var t = m.Tree.NodeAt(Temp)!;
        t.Id.Should().Be("n1");
        t.Children.Select(c => (c.Id, c.Cat)).Should().Equal(("r1-n1", "cache"));

        m.Recommendations.Should().HaveCount(w.Recommendations.Count);
        var temp = m.Recommendations.Single(r => r.Id == "temp");
        temp.Bytes.Should().Be(500_000_000);
        temp.NodeIds.Should().Equal("n1");
        m.Recommendations.Single(r => r.Id == "npm-cache").Should().Be(w.Recommendations.Single(r => r.Id == "npm-cache"));
        m.RefreshedFolders.Should().Equal(new DiskScanRefresh(root, Now));

        var again = m.Merging(s, Temp, Now.AddSeconds(5))!;
        again.Tree.NodeAt(Temp)!.Children.Select(c => c.Id).Should().Equal("r2-n1");
        again.RefreshedFolders.Should().ContainSingle();
        again.Tree.B.Should().Be(m.Tree.B);

        // A refresh above drops the entry below. The next tag must still be new.
        var up = again.Merging(Sub(w, "C:\\Users\\dev\\AppData\\Local", 1_000,
            new[] { new DiskNode("Temp", 1_000, "cache", "n1") }), new[] { "AppData\\Local" }, Now)!;
        var next = up.Merging(Sub(w, "C:\\Users\\dev\\Downloads", 1_000,
            new[] { new DiskNode("x.msi", 1_000, "docs", "n1", F: true) }), new[] { "Downloads" }, Now)!;
        next.Tree.NodeAt(new[] { "Downloads" })!.Children.Select(c => c.Id).Should().Equal("r4-n1");
    }

    [Fact]
    public void MergeAdjustsRecsAboveAndBelow()
    {
        var tree = new DiskNode("/h", 100, "other", C: new[]
        {
            new DiskNode("a", 100, "cache", "n1", C: new[]
            {
                new DiskNode("b", 60, "cache", C: new[] { new DiskNode("x", 60, "cache", "n2") }),
                new DiskNode("c", 40, "cache", "n3"),
            }),
        });
        var r = ScanFixture.Result("linux") with
        {
            Root = "/h", Home = "/h", ScannedBytes = 100, Tree = tree, Refreshed = null,
            Recs = new[]
            {
                Rec("all", "~/a", 100, "n1"),
                Rec("two", "~/a/b/x, ~/a/c", 100, "n2", "n3"),
                Rec("x", "~/a/b/x", 60, "n2"),
                Rec("glob", "~/a/b/*", 60),
                Rec("far", "~/z", 5),
            },
        };
        var s = Sub(r, "/h/a/b", 20, new[] { new DiskNode("x", 20, "cache", "n1") }, new[] { Rec("x", "~/a/b/x", 20, "n1") });
        var m = r.Merging(s, new[] { "a", "b" }, Now)!;
        m.Tree.B.Should().Be(60);
        m.Tree.NodeAt(new[] { "a" })!.Children.Select(c => c.N).Should().Equal("c", "b");
        m.Recommendations.Select(x => x.Id).Should().Equal("all", "two", "far", "x");
        m.Recommendations[0].Bytes.Should().Be(60);
        m.Recommendations[1].NodeIds.Should().Equal("n3");
        m.Recommendations[1].Bytes.Should().Be(40);
        m.Recommendations[3].NodeIds.Should().Equal("r1-n1");
        r.Merging(s, new[] { "a", "nope" }, Now).Should().BeNull();
    }

    [Fact]
    public void RuleIdDropsTheRepeatNumber()
    {
        Rec("node-modules-2", "", 0).RuleId.Should().Be("node-modules");
        Rec("go-build-cache", "", 0).RuleId.Should().Be("go-build-cache");
    }
}
