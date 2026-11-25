using FluentAssertions;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class RealCollectorTests : IAsyncLifetime
{
    private string _settingsPath = "";
    private string _historyPath = "";
    private HistoryStore _history = null!;
    private NodeStore _nodes = null!;
    private ServerStore _servers = null!;
    private ServerSettings _settings = null!;
    private FakeFactory _factory = null!;
    private RealCollector _collector = null!;

    public ValueTask InitializeAsync()
    {
        _settingsPath = Path.Combine(Path.GetTempPath(), $"tt-s-{Guid.NewGuid():N}.json");
        _historyPath = Path.Combine(Path.GetTempPath(), $"tt-h-{Guid.NewGuid():N}.sqlite");
        _history = new HistoryStore(_historyPath);
        _nodes = new NodeStore(_settingsPath);
        _settings = new ServerSettings(_settingsPath);
        _servers = new ServerStore(_nodes, _settings, _history);
        _factory = new FakeFactory();
        _collector = new RealCollector(_nodes, _servers, _settings, _factory, Logger.Create());
        return ValueTask.CompletedTask;
    }

    public async ValueTask DisposeAsync()
    {
        await _collector.DisposeAsync();
        await _history.DisposeAsync();
        if (File.Exists(_settingsPath)) File.Delete(_settingsPath);
        if (File.Exists(_historyPath)) File.Delete(_historyPath);
    }

    private static async Task Within(Task task)
        => await task.WaitAsync(TimeSpan.FromSeconds(5));

    [Fact]
    public void SkipsCloudNodes()
    {
        var cloud = Node.CloudOnly(Guid.NewGuid(), "cloud");
        var linked = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshHost = "h", Cloud = new CloudLink(DateTime.UtcNow) };
        _nodes.Add(cloud);
        _nodes.Add(linked);

        _collector.Start();

        _factory.Created.Should().NotContain(n => n.Id == cloud.Id || n.Id == linked.Id);
    }

    [Fact]
    public async Task StopsWorkerWhenNodeBecomesCloud()
    {
        var node = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshHost = "h" };
        _nodes.Add(node);
        _collector.Start();
        var invoker = _factory.Invokers[node.Id];
        await Within(invoker.Started.Task);

        _nodes.Update(node with { Cloud = new CloudLink(DateTime.UtcNow) });

        await Within(invoker.Cancelled.Task);
    }

    [Fact]
    public void StartsWorkerWhenLinkGoesAway()
    {
        var node = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshHost = "h", Cloud = new CloudLink(DateTime.UtcNow) };
        _nodes.Add(node);
        _collector.Start();
        _factory.Created.Should().NotContain(n => n.Id == node.Id);

        _nodes.Update(node with { Cloud = null });

        _factory.Created.Should().ContainSingle(n => n.Id == node.Id);
    }

    [Fact]
    public void LinkChangeBeforeStartStartsNoWorker()
    {
        var node = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshHost = "h", Cloud = new CloudLink(DateTime.UtcNow) };
        _nodes.Add(node);
        _factory.Created.Clear();
        _nodes.Update(node with { Cloud = null });
        _factory.Created.Should().NotContain(n => n.Id == node.Id);
    }

    private sealed class FakeFactory : ISamplerInvokerFactory
    {
        public List<Node> Created { get; } = new();
        public Dictionary<Guid, FakeInvoker> Invokers { get; } = new();

        public ISamplerInvoker Create(Node node)
        {
            lock (Created) Created.Add(node);
            var inv = new FakeInvoker();
            lock (Invokers) Invokers[node.Id] = inv;
            return inv;
        }
    }

    private sealed class FakeInvoker : ISamplerInvoker
    {
        public TaskCompletionSource Started { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        public TaskCompletionSource Cancelled { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

        public async Task<Sample> RunOnceAsync(CancellationToken ct = default)
        {
            Started.TrySetResult();
            try { await Task.Delay(Timeout.Infinite, ct); }
            catch (OperationCanceledException) { Cancelled.TrySetResult(); throw; }
            throw new InvalidOperationException();
        }

        public IAsyncEnumerable<Sample> StreamAsync(TimeSpan interval, CancellationToken ct = default) => throw new NotSupportedException();
        public Task<string> VersionAsync(CancellationToken ct = default) => Task.FromResult("v");
        public Task<bool> SelfCheckAsync(CancellationToken ct = default) => Task.FromResult(true);
    }
}
