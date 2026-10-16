using System.IO.Compression;
using System.Net;
using System.Text;
using System.Text.Json;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class RemoteDiskScannerTests : IAsyncLifetime
{
    private sealed class FakeUpdates : IDiskScanUpdates
    {
        public event Action<Guid, DiskScanJob>? DiskScanUpdated;
        public bool HasListener => DiskScanUpdated != null;
        public void Raise(DiskScanJob job) => DiskScanUpdated?.Invoke(job.NodeId, job);
    }

    private LocalTestHttpServer _server = null!;
    private RemoteClient _client = null!;
    private readonly FakeUpdates _updates = new();
    private readonly Node _node = new() { DisplayName = "web-1", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
    private readonly Guid _jobId = Guid.NewGuid();
    private static readonly JsonSerializerOptions Json = DiskScanCodec.Options;

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
    }

    private DiskScanJob Job(string state, DiskScanProgress? progress = null) => new(
        _jobId, _node.Id, state, Root: "/", Progress: progress,
        CreatedTs: new DateTime(2026, 10, 1, 10, 2, 11, DateTimeKind.Utc),
        StartedTs: state == DiskScanJob.Queued ? null : new DateTime(2026, 10, 1, 10, 2, 14, DateTimeKind.Utc),
        FinishedTs: state == DiskScanJob.Done ? new DateTime(2026, 10, 1, 10, 3, 2, DateTimeKind.Utc) : null,
        ResultBytes: state == DiskScanJob.Done ? 1_800_000 : 0);

    private static async Task WriteJson(HttpListenerContext ctx, object body, int status = 200)
    {
        ctx.Response.StatusCode = status;
        ctx.Response.ContentType = "application/json";
        var bytes = Encoding.UTF8.GetBytes(JsonSerializer.Serialize(body, Json));
        await ctx.Response.OutputStream.WriteAsync(bytes);
        ctx.Response.Close();
    }

    private void ServeResult(string os)
    {
        _server.Handlers[$"GET /v1/disk-scans/{_jobId}/result"] = async ctx =>
        {
            using var ms = new MemoryStream();
            using (var gz = new GZipStream(ms, CompressionLevel.Fastest, leaveOpen: true))
                gz.Write(Encoding.UTF8.GetBytes(ScanFixture.Lines(os)[^1]));
            ctx.Response.ContentType = "application/x-ndjson";
            ctx.Response.AddHeader("Content-Encoding", "gzip");
            await ctx.Response.OutputStream.WriteAsync(ms.ToArray());
            ctx.Response.Close();
        };
    }

    private static async Task<List<DiskScanEvent>> Collect(IAsyncEnumerable<DiskScanEvent> events)
    {
        var list = new List<DiskScanEvent>();
        await foreach (var e in events) list.Add(e);
        return list;
    }

    [Fact]
    public async Task FollowsFramesAndDownloadsTheGzipResult()
    {
        ServeResult("linux");
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] = ctx => WriteJson(ctx, Job(DiskScanJob.Queued), 202);
        _server.Handlers[$"GET /v1/disk-scans/{_jobId}"] = ctx => WriteJson(ctx, Job(DiskScanJob.Queued));
        var scanner = new RemoteDiskScanner(_client, _updates, pollInterval: TimeSpan.FromSeconds(30));

        var task = Collect(scanner.ScanAsync(_node, new DiskScanRequest("/"), TestContext.Current.CancellationToken));
        await WaitUntil(() => _updates.HasListener);
        var progress = DiskScanCodec.DecodeLine(ScanFixture.Lines("linux")[0]) is DiskScanEvent.Progress p ? p.Line : null;
        _updates.Raise(Job(DiskScanJob.Running, progress));
        await Task.Delay(50, TestContext.Current.CancellationToken);
        _updates.Raise(Job(DiskScanJob.Done, progress));
        var events = await task.WaitAsync(TimeSpan.FromSeconds(10), TestContext.Current.CancellationToken);

        var post = _server.Requests.First(r => r.Method == "POST");
        JsonDocument.Parse(post.Body).RootElement.GetProperty("root").GetString().Should().Be("/");
        events.OfType<DiskScanEvent.Job>().Select(j => j.Value.State)
            .Should().Equal(DiskScanJob.Queued, DiskScanJob.Running, DiskScanJob.Done);
        events.OfType<DiskScanEvent.Progress>().Should().ContainSingle();
        events[^1].Should().BeOfType<DiskScanEvent.Result>().Which.Value.Hostname.Should().Be("dev-linux");
        _updates.HasListener.Should().BeFalse();
    }

    [Fact]
    public async Task PollsWhenNoFrameComes()
    {
        ServeResult("windows");
        var polls = 0;
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] = ctx => WriteJson(ctx, Job(DiskScanJob.Queued), 202);
        _server.Handlers[$"GET /v1/disk-scans/{_jobId}"] = ctx =>
            WriteJson(ctx, Job(Interlocked.Increment(ref polls) < 2 ? DiskScanJob.Running : DiskScanJob.Done));
        var scanner = new RemoteDiskScanner(_client, updates: null, pollInterval: TimeSpan.FromMilliseconds(50));

        var events = await Collect(scanner.ScanAsync(_node, new DiskScanRequest(), TestContext.Current.CancellationToken))
            .WaitAsync(TimeSpan.FromSeconds(10), TestContext.Current.CancellationToken);
        events[^1].Should().BeOfType<DiskScanEvent.Result>().Which.Value.Os.Should().Be("windows");
    }

    [Fact]
    public async Task ForbiddenMeansOptInIsOff()
    {
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] =
            ctx => WriteJson(ctx, new { error = new { code = "forbidden", message = "disk scans are off for this node" } }, 403);
        var scanner = new RemoteDiskScanner(_client, _updates);
        var events = await Collect(scanner.ScanAsync(_node, new DiskScanRequest(), TestContext.Current.CancellationToken));
        events.Should().ContainSingle().Which.Should().Be(new DiskScanEvent.Error(RemoteDiskScanner.OptInOffMessage));
    }

    [Fact]
    public async Task ConflictFollowsTheRunningJob()
    {
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] = ctx => WriteJson(ctx, new
        {
            error = new { code = "conflict", message = "a scan of this node runs now" },
            job = Job(DiskScanJob.Running),
        }, 409);
        _server.Handlers[$"GET /v1/disk-scans/{_jobId}"] = ctx =>
            WriteJson(ctx, Job(DiskScanJob.Failed) with { Error = "root not found" });
        var scanner = new RemoteDiskScanner(_client, _updates, pollInterval: TimeSpan.FromMilliseconds(50));
        var events = await Collect(scanner.ScanAsync(_node, new DiskScanRequest(), TestContext.Current.CancellationToken))
            .WaitAsync(TimeSpan.FromSeconds(10), TestContext.Current.CancellationToken);
        events[0].Should().BeOfType<DiskScanEvent.Job>().Which.Value.State.Should().Be(DiskScanJob.Running);
        events[^1].Should().Be(new DiskScanEvent.Error("root not found"));
    }

    [Fact]
    public async Task CancelPostsCancelAndWaitsForTheFinalState()
    {
        var cancelled = false;
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] = ctx => WriteJson(ctx, Job(DiskScanJob.Running), 202);
        _server.Handlers[$"POST /v1/disk-scans/{_jobId}/cancel"] = ctx =>
        {
            cancelled = true;
            return WriteJson(ctx, Job(DiskScanJob.Running) with { CancelRequested = true });
        };
        _server.Handlers[$"GET /v1/disk-scans/{_jobId}"] = ctx =>
            WriteJson(ctx, Job(cancelled ? DiskScanJob.CancelledState : DiskScanJob.Running));
        var scanner = new RemoteDiskScanner(_client, _updates, pollInterval: TimeSpan.FromMilliseconds(50));

        using var cts = new CancellationTokenSource();
        var events = new List<DiskScanEvent>();
        await foreach (var e in scanner.ScanAsync(_node, new DiskScanRequest(), cts.Token))
        {
            events.Add(e);
            cts.Cancel();
        }
        cancelled.Should().BeTrue();
        events.OfType<DiskScanEvent.Job>().Should().Contain(j => j.Value.CancelRequested);
        events[^1].Should().BeOfType<DiskScanEvent.Cancelled>();
    }

    [Fact]
    public async Task ExpiredJobIsAnError()
    {
        _server.Handlers[$"POST /v1/nodes/{_node.Id}/disk-scans"] = ctx => WriteJson(ctx, Job(DiskScanJob.Expired), 202);
        var scanner = new RemoteDiskScanner(_client, _updates);
        var events = await Collect(scanner.ScanAsync(_node, new DiskScanRequest(), TestContext.Current.CancellationToken));
        events[^1].Should().Be(new DiskScanEvent.Error("No sampler picked up the scan in 10 min."));
    }

    [Fact]
    public async Task ListsJobsNewestFirst()
    {
        _server.Handlers[$"GET /v1/nodes/{_node.Id}/disk-scans"] =
            ctx => WriteJson(ctx, new { jobs = new[] { Job(DiskScanJob.Running), Job(DiskScanJob.Done) } });
        var scanner = new RemoteDiskScanner(_client, _updates);
        (await scanner.LatestJobAsync(_node.Id, TestContext.Current.CancellationToken))!.State.Should().Be(DiskScanJob.Running);
    }

    [Fact]
    public void StreamFrameCarriesTheJob()
    {
        var json = $$$"""{"type":"disk_scan_updated","nodeId":"{{{_node.Id}}}","job":{"id":"{{{_jobId}}}","node_id":"{{{_node.Id}}}","state":"running","cancel_requested":false}}""";
        var frame = JsonSerializer.Deserialize<RemoteStreamFrame>(json)!;
        frame.Type.Should().Be("disk_scan_updated");
        frame.Job!.Id.Should().Be(_jobId);
        frame.Job.State.Should().Be(DiskScanJob.Running);
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
}
