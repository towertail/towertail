using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;
using Microsoft.Extensions.Logging;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Per-node pacer. Runs one Task per enabled node that invokes the appropriate
/// <see cref="ISamplerInvoker"/> on the configured cadence (2s local / 10s SSH by default),
/// respects <see cref="SystemReachabilityMonitor.ShouldPoll"/>, and backs off on failure.
/// </summary>
public sealed class RealCollector : ICollector, IAsyncDisposable
{
    private readonly NodeStore _nodes;
    private readonly ServerStore _servers;
    private readonly ServerSettings _settings;
    private readonly ISamplerInvokerFactory _factory;
    private readonly Logger _logger;
    private readonly Dictionary<Guid, Worker> _workers = new();
    private readonly object _lock = new();
    private bool _started;

    /// <summary>
    /// Optional UI-thread marshaller. The WinUI app sets this at bootstrap so
    /// sample ingestion (which mutates bound ObservableCollections + raises
    /// PropertyChanged) happens on the dispatcher. When null, Ingest runs on
    /// the worker thread — fine for tests, unsafe for production.
    /// </summary>
    public Func<Action, Task>? UiMarshaller { get; set; }

    public RealCollector(
        NodeStore nodes, ServerStore servers, ServerSettings settings,
        ISamplerInvokerFactory factory, Logger logger)
    {
        _nodes = nodes;
        _servers = servers;
        _settings = settings;
        _factory = factory;
        _logger = logger;

        _nodes.NodeAdded += (_, id) => EnsureWorker(id);
        _nodes.NodeRemoved += (_, id) => StopWorker(id);
    }

    public void Start()
    {
        lock (_lock)
        {
            if (_started) return;
            _started = true;
            foreach (var n in _nodes.Nodes) EnsureWorker(n.Id);
        }
    }

    public Task RefreshAsync(Guid nodeId)
    {
        lock (_lock)
            if (_workers.TryGetValue(nodeId, out var w)) w.TriggerNow();
        return Task.CompletedTask;
    }

    public Task StopAsync()
    {
        lock (_lock)
        {
            foreach (var w in _workers.Values) w.Stop();
            _workers.Clear();
            _started = false;
        }
        return Task.CompletedTask;
    }

    private void EnsureWorker(Guid id)
    {
        var node = _nodes.ById(id); if (node == null) return;
        lock (_lock)
        {
            if (_workers.ContainsKey(id)) return;
            var invoker = _factory.Create(node);
            var worker = new Worker(id, node, invoker, _servers, _settings, _logger, UiMarshaller);
            _workers[id] = worker;
            worker.Start();
        }
    }

    private void StopWorker(Guid id)
    {
        lock (_lock)
        {
            if (_workers.Remove(id, out var w)) w.Stop();
        }
    }

    public async ValueTask DisposeAsync()
    {
        await StopAsync().ConfigureAwait(false);
    }

    private sealed class Worker
    {
        private readonly Guid _id;
        private readonly Node _node;
        private readonly ISamplerInvoker _invoker;
        private readonly ServerStore _servers;
        private readonly ServerSettings _settings;
        private readonly Logger _logger;
        private readonly Func<Action, Task>? _marshal;
        private readonly CancellationTokenSource _cts = new();
        private readonly ManualResetEventSlim _trigger = new(false);
        private Task? _loop;
        private int _failureStreak;

        public Worker(Guid id, Node n, ISamplerInvoker invoker,
            ServerStore servers, ServerSettings settings, Logger logger,
            Func<Action, Task>? marshal)
        {
            _id = id; _node = n; _invoker = invoker;
            _servers = servers; _settings = settings; _logger = logger;
            _marshal = marshal;
        }

        public void Start() { _loop = Task.Run(LoopAsync); }
        public void TriggerNow() => _trigger.Set();
        public void Stop() { _cts.Cancel(); _trigger.Set(); }

        private async Task LoopAsync()
        {
            var ct = _cts.Token;
            while (!ct.IsCancellationRequested)
            {
                if (_node.Enabled)
                {
                    try
                    {
                        var sample = await _invoker.RunOnceAsync(ct).ConfigureAwait(false);
                        // ObservableCollections + PropertyChanged consumers are WinUI-
                        // bound; mutate on the UI dispatcher when one is injected.
                        if (_marshal is null) _servers.Ingest(_id, sample);
                        else await _marshal(() => _servers.Ingest(_id, sample)).ConfigureAwait(false);
                        _failureStreak = 0;
                    }
                    catch (OperationCanceledException) { return; }
                    catch (Exception ex)
                    {
                        _failureStreak = Math.Min(_failureStreak + 1, 10);
                        _logger.Warning($"collector[{_node.DisplayName}] fail #{_failureStreak}: {ex.Message}");
                        if (_marshal is null) _servers.MarkUnreachable(_id);
                        else await _marshal(() => _servers.MarkUnreachable(_id)).ConfigureAwait(false);
                    }
                }

                var baseInterval = TimeSpan.FromSeconds(_settings.PollingInterval(_node.Kind));
                var backoff = _failureStreak == 0
                    ? baseInterval
                    : TimeSpan.FromSeconds(Math.Min(60, (int)baseInterval.TotalSeconds * (1 << Math.Min(5, _failureStreak))));
                _trigger.Reset();
                try
                {
                    using var delayCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
                    var delay = Task.Delay(backoff, delayCts.Token);
                    var wake = Task.Run(() => { _trigger.Wait(ct); }, ct);
                    var winner = await Task.WhenAny(delay, wake).ConfigureAwait(false);
                    if (winner == wake) delayCts.Cancel();
                }
                catch (OperationCanceledException) { return; }
            }
        }
    }
}
