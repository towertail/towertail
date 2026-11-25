using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests;

public sealed class RemoteStreamTests : IAsyncLifetime
{
    private LocalTestHttpServer _server = null!;

    public ValueTask InitializeAsync()
    {
        _server = new LocalTestHttpServer();
        _server.Handlers["GET /v1/stream"] = _server.AcceptWebSocketAsync;
        return ValueTask.CompletedTask;
    }

    public ValueTask DisposeAsync() => _server.DisposeAsync();

    private RemoteStream Open(bool lease, Func<TimeSpan, CancellationToken, Task>? delay = null)
    {
        if (delay is not null) return new RemoteStream(StreamUri(lease), _server.ExpectedToken, delay);
        return new RemoteClient(new Uri(_server.BaseUrl), _server.ExpectedToken).OpenStream(lease);
    }

    private Uri StreamUri(bool lease) => new($"ws://127.0.0.1:{_server.Port}/v1/stream" + (lease ? "?lease=1" : ""));

    private static async Task WaitUntil(Func<bool> condition)
    {
        var until = DateTime.UtcNow.AddSeconds(5);
        while (!condition())
        {
            if (DateTime.UtcNow > until) throw new TimeoutException();
            await Task.Delay(10);
        }
    }

    [Fact]
    public async Task OpensLeaseStreamWithBearer()
    {
        await using var stream = Open(lease: true);
        var connected = 0;
        stream.Connected += () => Interlocked.Increment(ref connected);
        stream.Start();
        await WaitUntil(() => connected == 1);

        var req = _server.Requests.Should().ContainSingle().Subject;
        req.Path.Should().Be("/v1/stream");
        req.Query.Should().Be("lease=1");
        req.Authorization.Should().Be("Bearer test-token");
        stream.IsConnected.Should().BeTrue();
    }

    [Fact]
    public async Task LegacyStreamHasNoQuery()
    {
        await using var stream = Open(lease: false);
        var connected = 0;
        stream.Connected += () => Interlocked.Increment(ref connected);
        stream.Start();
        await WaitUntil(() => connected == 1);
        _server.Requests.Should().ContainSingle().Which.Query.Should().BeEmpty();
    }

    [Fact]
    public async Task ReconnectsAfterClose()
    {
        // The delay returns at once, so the test does not wait for the jitter.
        await using var stream = Open(lease: true, delay: (_, ct) => Task.Delay(1, ct));
        var connected = 0;
        stream.Connected += () => Interlocked.Increment(ref connected);
        stream.Start();
        await WaitUntil(() => connected == 1);

        await _server.CloseAllWebSocketsAsync();
        await WaitUntil(() => connected == 2);
        stream.IsConnected.Should().BeTrue();
    }

    [Fact]
    public async Task SendWritesViewFrame()
    {
        await using var stream = Open(lease: true);
        var connected = 0;
        stream.Connected += () => Interlocked.Increment(ref connected);
        stream.Start();
        await WaitUntil(() => connected == 1);

        var id = Guid.NewGuid();
        var view = new LiveView([id], new ViewDetail(id, DetailTab.Processes));
        await stream.SendAsync(view);

        await WaitUntil(() => { lock (_server.ClientFrames) return _server.ClientFrames.Count == 1; });
        _server.ClientFrames[0].Should().Be(view.ToFrameJson());
    }

    [Fact]
    public async Task SendWithoutConnectionDoesNothing()
    {
        await using var stream = Open(lease: true);
        await stream.SendAsync(LiveView.Empty);
        _server.ClientFrames.Should().BeEmpty();
    }

    [Fact]
    public async Task DeliversDecodedFrames()
    {
        await using var stream = Open(lease: true);
        var got = new List<StreamMessage>();
        var connected = 0;
        stream.Connected += () => Interlocked.Increment(ref connected);
        stream.MessageReceived += m => { lock (got) got.Add(m); };
        stream.Start();
        await WaitUntil(() => connected == 1);

        await _server.SendToAllWebSocketsAsync("""{"type":"hello","hello":{"lease":true,"renew_s":30,"max_ttl_s":120}}""");
        await WaitUntil(() => { lock (got) return got.Count == 1; });
        got[0].Should().BeOfType<StreamMessage.HelloFrame>();
    }
}
