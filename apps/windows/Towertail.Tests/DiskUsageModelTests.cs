using System.Runtime.CompilerServices;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class DiskUsageModelTests : IDisposable
{
    private sealed class ScriptedScanner : IDiskScanner
    {
        public List<DiskScanEvent> Events { get; } = new();
        public DiskScanRequest? Request { get; private set; }
        /// <summary>When set, the scan waits for cancel after the events.</summary>
        public bool WaitForCancel { get; set; }

        public async IAsyncEnumerable<DiskScanEvent> ScanAsync(
            Node node, DiskScanRequest request, [EnumeratorCancellation] CancellationToken ct = default)
        {
            Request = request;
            foreach (var e in Events) { await Task.Yield(); yield return e; }
            if (!WaitForCancel) yield break;
            try { await Task.Delay(Timeout.Infinite, ct); } catch (OperationCanceledException) { }
            yield return new DiskScanEvent.Cancelled();
        }
    }

    private readonly string _path = Path.Combine(Path.GetTempPath(), $"tt-usage-{Guid.NewGuid():N}.sqlite");
    private readonly DiskScanStore _store;
    private readonly Node _node = Node.LocalWindows("pc");
    private DateTime _now = new(2026, 10, 1, 12, 5, 0, DateTimeKind.Utc);

    public DiskUsageModelTests() { _store = new DiskScanStore(_path); }

    public void Dispose()
    {
        _store.Dispose();
        foreach (var f in new[] { _path, _path + "-wal", _path + "-shm" }) File.Delete(f);
    }

    private DiskUsageModel Make(ScriptedScanner scanner, string? scope = null)
        => new(_node, scanner, _store, now: () => _now, scope: scope);

    [Fact]
    public async Task ScanSelectsSafeItemsAndSavesTheResult()
    {
        var lines = ScanFixture.Lines("windows");
        var scanner = new ScriptedScanner();
        scanner.Events.AddRange(lines.Select(DiskScanCodec.DecodeLine));
        var model = Make(scanner);
        var phases = new List<DiskUsagePhase>();
        model.PropertyChanged += (_, e) => { if (e.PropertyName == nameof(DiskUsageModel.Phase)) phases.Add(model.Phase); };

        model.StatusText.Should().Be("Not scanned yet");
        await model.StartAsync();

        scanner.Request!.Root.Should().Be("~");
        phases.Should().Equal(DiskUsagePhase.Running, DiskUsagePhase.Done);
        model.Progress.Should().NotBeNull();
        var result = model.Result!;
        model.Selected.Should().BeEquivalentTo(result.Recommendations.Where(r => r.IsSafe).Select(r => r.Id));
        model.SelectedBytes.Should().Be(result.Recommendations.Where(r => r.IsSafe).Sum(r => r.Bytes));
        model.Script!.Shell.Should().Be(CleanupShell.PowerShell);
        model.StatusText.Should().Be("Scanned 4 min ago · 12 files · 12 s");
        model.IsStale.Should().BeFalse();
        _store.Latest(_node.Id, "~").Should().NotBeNull();
    }

    [Fact]
    public async Task SelectionCommands()
    {
        var scanner = new ScriptedScanner();
        scanner.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("linux")));
        var model = Make(scanner);
        await model.StartAsync();
        var changes = 0;
        model.SelectionChanged += (_, _) => changes++;

        var review = model.Result!.Recommendations.First(r => !r.IsSafe);
        model.Toggle(review.Id);
        model.Selected.Should().Contain(review.Id);
        model.SelectedNodeIds.Should().Contain(review.NodeIds ?? Array.Empty<string>());
        model.Toggle(review.Id);
        model.Selected.Should().NotContain(review.Id);
        model.ClearSelection();
        model.Selected.Should().BeEmpty();
        model.Script!.Chosen.Should().BeEmpty();
        model.SelectSafe();
        model.Selected.Should().NotBeEmpty();
        changes.Should().Be(4);
    }

    [Fact]
    public async Task ErrorEndsFailed()
    {
        var scanner = new ScriptedScanner();
        scanner.Events.Add(new DiskScanEvent.Error("root not found"));
        var model = Make(scanner);
        await model.StartAsync();
        model.Phase.Should().Be(DiskUsagePhase.Failed);
        model.Error.Should().Be("root not found");
        model.StatusText.Should().Be("Scan failed");
    }

    [Fact]
    public async Task StreamWithoutFinalEventEndsFailed()
    {
        var model = Make(new ScriptedScanner());
        await model.StartAsync();
        model.Phase.Should().Be(DiskUsagePhase.Failed);
        model.Error.Should().Be("The scan ended without a result.");
    }

    [Fact]
    public async Task CancelEndsCancelledAndKeepsThePreviousResult()
    {
        var first = new ScriptedScanner();
        first.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("linux")));
        var model = Make(first);
        await model.StartAsync();
        var previous = model.Result;

        var second = new ScriptedScanner { WaitForCancel = true };
        var run = model.RunAsync(ct => second.ScanAsync(_node, new DiskScanRequest(), ct));
        model.Phase.Should().Be(DiskUsagePhase.Running);
        model.StatusText.Should().StartWith("Scanning… 0:00");
        await model.StartAsync(); // ignored while a scan runs
        model.Cancel();
        await run.WaitAsync(TimeSpan.FromSeconds(5), TestContext.Current.CancellationToken);

        model.Phase.Should().Be(DiskUsagePhase.Cancelled);
        model.Result.Should().BeSameAs(previous);
    }

    [Fact]
    public async Task LoadLatestAndStaleText()
    {
        var scanner = new ScriptedScanner();
        scanner.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("darwin")));
        await Make(scanner).StartAsync();

        _now = _now.AddDays(3);
        var model = Make(new ScriptedScanner());
        model.LoadLatest();
        model.Phase.Should().Be(DiskUsagePhase.Done);
        model.Result!.Os.Should().Be("darwin");
        model.Selected.Should().NotBeEmpty();
        model.IsStale.Should().BeTrue();
        model.StaleText.Should().Be("Scanned 3 days ago. Rescan before cleanup?");

        Make(new ScriptedScanner(), scope: "/").LoadLatest();
        var other = Make(new ScriptedScanner(), scope: "/");
        other.LoadLatest();
        other.Result.Should().BeNull();
    }

    [Fact]
    public async Task MarshallerRunsEachUpdate()
    {
        var calls = 0;
        var scanner = new ScriptedScanner();
        scanner.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("linux")));
        var model = new DiskUsageModel(_node, scanner, null, a => { calls++; a(); return Task.CompletedTask; });
        await model.StartAsync();
        calls.Should().Be(3);
        model.Phase.Should().Be(DiskUsagePhase.Done);
    }

    [Fact]
    public void JobStepsForTheRemoteStrip()
    {
        var model = Make(new ScriptedScanner());
        model.JobSteps.Should().BeEmpty();
        model.Job = new DiskScanJob(Guid.NewGuid(), _node.Id, DiskScanJob.Done,
            CreatedTs: new DateTime(2026, 10, 1, 10, 2, 11, DateTimeKind.Utc),
            StartedTs: new DateTime(2026, 10, 1, 10, 2, 14, DateTimeKind.Utc),
            FinishedTs: new DateTime(2026, 10, 1, 10, 3, 2, DateTimeKind.Utc),
            ResultBytes: 1_800_000);
        var steps = model.JobSteps;
        steps.Select(s => s.Label).Should().Equal("Queued", "Sampler picked up", "Scanned", "Result in");
        steps.Should().OnlyContain(s => s.Reached);
        steps[2].Detail.Should().Be("48 s");
        steps[3].Detail.Should().Be("2 MB");
    }

    [Fact]
    public void ScopesAndTexts()
    {
        DiskUsageModel.DefaultScope("linux").Should().Be("/");
        DiskUsageModel.DefaultScope("windows").Should().Be("~");
        DiskUsageModel.DefaultScope(null).Should().Be("~");
        DiskUsageModel.Scopes(new[] { "C:\\", "D:\\", "C:\\" }).Should().Equal("~", "C:\\", "D:\\");
        DiskUsageModel.Count(640_000).Should().Be("640k");
        DiskUsageModel.Count(1_200_000).Should().Be("1.2M");
        DiskUsageModel.Count(950).Should().Be("950");
        DiskUsageModel.Duration(TimeSpan.FromSeconds(72)).Should().Be("1 m 12 s");
        DiskUsageModel.Elapsed(TimeSpan.FromSeconds(37)).Should().Be("0:37");
        DiskUsageModel.Ago(TimeSpan.FromDays(1.5)).Should().Be("1 day ago");
    }

    private static readonly string[] Temp = { "AppData\\Local", "Temp" };
    private static readonly string[] Captures = { "Videos\\Captures" };

    private async Task<(DiskUsageModel Model, ScriptedScanner Scanner)> Scanned()
    {
        var scanner = new ScriptedScanner();
        scanner.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("windows")));
        var model = Make(scanner);
        await model.StartAsync();
        return (model, scanner);
    }

    [Fact]
    public async Task AddRemoveAndToggleTiles()
    {
        var (model, _) = await Scanned();
        model.Mark(Captures).Should().Be(new TileMark.None());

        model.Add(Captures);
        model.Mark(Captures).Should().Be(new TileMark.Mine(true));
        model.MineItems.Should().ContainSingle().Which.Label.Should().Be("Captures/");
        model.SelectedCount.Should().Be(model.SelectedRecs.Count + 1);
        model.Script!.Text.Should().Contain("Remove-Item -LiteralPath 'C:\\Users\\dev\\Videos\\Captures' -Recurse -Force");
        var bytes = model.SelectedBytes;
        model.ToggleTile(Captures);
        model.Mark(Captures).Should().Be(new TileMark.Mine(false));
        model.SelectedBytes.Should().Be(bytes - 3_000_000_000);
        model.Remove(Captures);
        model.MineItems.Should().BeEmpty();

        // A rule tile: the box checks the rec, Remove hides it, Add shows it again.
        var temp = model.Result!.Recommendations.Single(r => r.Id == "temp");
        model.Mark(Temp).Should().Be(new TileMark.Rule(temp, true));
        model.ToggleTile(Temp);
        model.Selected.Should().NotContain("temp");
        model.Remove(Temp);
        model.IsHidden(temp).Should().BeTrue();
        model.HiddenRecs.Should().ContainSingle();
        model.Mark(Temp).Should().Be(new TileMark.None());
        model.Add(Temp);
        model.IsHidden(temp).Should().BeFalse();
        model.Selected.Should().Contain("temp");

        // A checked folder covers the tiles below it, and Add takes the items below out.
        model.Add(Captures);
        model.Add(new[] { "AppData\\Local" });
        model.Mark(Temp).Should().Be(new TileMark.Covered("AppData\\Local"));
        model.HoldsSelection(new[] { "AppData\\Local" }).Should().BeTrue();
        model.ClearSelection();
        model.MineItems.Should().OnlyContain(i => !i.On);
        model.SelectedCount.Should().Be(0);
    }

    [Fact]
    public async Task PicksStayAcrossRescanAndGoneItemsLeave()
    {
        var (model, scanner) = await Scanned();
        model.Add(Captures);
        model.Remove(Temp);
        _store.SavePick(_node.Id, "~", new CleanupPick(CleanupPickKind.Mine, "C:\\Users\\dev\\Old", _now));

        await model.StartAsync();
        model.Notice.Should().Be("%USERPROFILE%\\Old is gone, so it left the cleanup list.");
        model.MineItems.Select(i => i.Path).Should().Equal("C:\\Users\\dev\\Videos\\Captures");
        model.Selected.Should().NotContain("temp");
        _store.Picks(_node.Id, "~").Should().HaveCount(2);

        var again = Make(scanner);
        again.LoadLatest();
        again.IsHidden(again.Result!.Recommendations.Single(r => r.Id == "temp")).Should().BeTrue();
        again.MineItems.Should().ContainSingle();
        Make(scanner, scope: "C:\\").Picks.Should().BeEmpty();
    }

    [Fact]
    public async Task RefreshMergesTheFolder()
    {
        var (model, scanner) = await Scanned();
        model.Add(Captures);
        var full = model.Result!;
        var sub = full with
        {
            Root = "C:\\Users\\dev\\AppData\\Local\\Temp",
            Tree = new DiskNode("C:\\Users\\dev\\AppData\\Local\\Temp", 500_000_000, "cache",
                C: new[] { new DiskNode("setup-log.txt", 500_000_000, "cache", F: true) }),
            Recs = Array.Empty<DiskRec>(),
        };
        scanner.Events.Clear();
        scanner.Events.Add(new DiskScanEvent.Result(sub));
        _now = _now.AddMinutes(5);

        await model.RefreshAsync(Temp.Append("setup-log.txt").ToList());

        scanner.Request.Should().Be(new DiskScanRequest("C:\\Users\\dev\\AppData\\Local\\Temp", 3, 50_000_000, 0, "~"));
        model.Refreshing.Should().BeNull();
        model.Notice.Should().Be("Temp refreshed. 1.0 GB less than before.");
        model.Result!.Tree.B.Should().Be(full.Tree.B - 1_000_000_000);
        model.Result.RefreshedFolders.Should().Equal(new DiskScanRefresh("C:\\Users\\dev\\AppData\\Local\\Temp", _now));
        model.MineItems.Should().ContainSingle();
        model.Selected.Should().Contain("temp");
        var stored = _store.Latest(_node.Id, "~")!;
        stored.Result.Tree.B.Should().Be(model.Result.Tree.B);
        _store.List(_node.Id, "~").Should().HaveCount(1);

        // The root starts a full scan.
        scanner.Events.Clear();
        scanner.Events.Add(new DiskScanEvent.Result(full));
        await model.RefreshAsync(Array.Empty<string>());
        scanner.Request!.RefreshOf.Should().BeNull();
        model.Result.Tree.B.Should().Be(full.Tree.B);
    }

    [Fact]
    public async Task FailedRefreshKeepsTheResult()
    {
        var (model, scanner) = await Scanned();
        var before = model.Result;
        scanner.Events.Clear();
        scanner.Events.Add(new DiskScanEvent.Error("boom"));
        await model.RefreshAsync(Temp);
        model.Notice.Should().Be("The refresh failed: boom. The map shows the previous result.");
        model.Result.Should().BeSameAs(before);
        model.IsBusy.Should().BeFalse();
    }

    [Fact]
    public async Task RefreshShowsAFullScanThatRunsOnTheServer()
    {
        var (model, scanner) = await Scanned();
        var job = new DiskScanJob(Guid.NewGuid(), _node.Id, DiskScanJob.Running, Root: "~");
        scanner.Events.Clear();
        scanner.Events.Add(new DiskScanEvent.Job(job));
        scanner.Events.Add(new DiskScanEvent.Result(ScanFixture.Result("windows") with { Hostname = "full" }));
        var phases = new List<DiskUsagePhase>();
        model.PropertyChanged += (_, e) => { if (e.PropertyName == nameof(DiskUsageModel.Phase)) phases.Add(model.Phase); };

        await model.RefreshAsync(Temp);

        phases.Should().Equal(DiskUsagePhase.Running, DiskUsagePhase.Done);
        model.Result!.Hostname.Should().Be("full");
        model.Notice.Should().BeNull();
        model.Refreshing.Should().BeNull();
    }
}
