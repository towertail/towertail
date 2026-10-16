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
}
