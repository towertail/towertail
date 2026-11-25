using System.Net.WebSockets;
using System.Text;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Backend;

/// <summary>
/// A WebSocket to <c>/v1/stream</c> that reconnects by itself. One loop connects, reads
/// frames, and after an error waits 0.5–1.5 s, then connects again. Mirrors the Mac
/// <c>openStream</c>. Call <see cref="Start"/> after you attach the handlers.
/// </summary>
public sealed class RemoteStream : IAsyncDisposable
{
    private readonly Uri _uri;
    private readonly string _token;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly Logger? _log;
    private readonly Random _rng = new();
    private readonly CancellationTokenSource _cts = new();
    private readonly SemaphoreSlim _sendLock = new(1, 1);
    private readonly object _gate = new();
    private readonly StreamErrorLog _errors = new();
    private ClientWebSocket? _socket;
    private Task? _loop;

    public RemoteStream(Uri uri, string token, Func<TimeSpan, CancellationToken, Task>? delay = null, Logger? log = null)
    {
        _uri = uri;
        _token = token;
        _delay = delay ?? Task.Delay;
        _log = log;
    }

    public event Action? Connected;
    public event Action<Exception?>? Disconnected;
    public event Action<StreamMessage>? MessageReceived;

    public bool IsConnected { get; private set; }

    public void Start()
    {
        lock (_gate) _loop ??= Task.Run(() => RunAsync(_cts.Token));
    }

    /// <summary>
    /// Sends one <c>view</c> frame. Without a connection it does nothing: <see cref="Connected"/>
    /// lets the caller send the view again. A send error closes the socket and the loop connects again.
    /// </summary>
    public async Task SendAsync(LiveView view, CancellationToken ct = default)
    {
        ClientWebSocket? ws;
        lock (_gate) ws = IsConnected ? _socket : null;
        if (ws is null) return;
        var bytes = Encoding.UTF8.GetBytes(view.ToFrameJson());
        try
        {
            await _sendLock.WaitAsync(ct).ConfigureAwait(false);
            try { await ws.SendAsync(bytes, WebSocketMessageType.Text, true, ct).ConfigureAwait(false); }
            finally { _sendLock.Release(); }
        }
        catch (OperationCanceledException) when (ct.IsCancellationRequested) { throw; }
        catch { ws.Abort(); }
    }

    private async Task RunAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            Exception? failure = null;
            try { await OnceAsync(ct).ConfigureAwait(false); }
            catch (OperationCanceledException) when (ct.IsCancellationRequested) { return; }
            catch (Exception ex)
            {
                failure = ex;
                if (_errors.ShouldLog(ex)) _log?.Warning($"stream: connection failed: {StreamErrorLog.Text(ex)}");
            }
            lock (_gate) { IsConnected = false; _socket = null; }
            Raise(() => Disconnected?.Invoke(failure));
            try { await _delay(TimeSpan.FromMilliseconds(_rng.Next(500, 1501)), ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
    }

    private async Task OnceAsync(CancellationToken ct)
    {
        using var ws = new ClientWebSocket();
        ws.Options.SetRequestHeader("Authorization", $"Bearer {_token}");
        await ws.ConnectAsync(_uri, ct).ConfigureAwait(false);
        lock (_gate) { _socket = ws; IsConnected = true; }
        _errors.Reset();
        Raise(() => Connected?.Invoke());

        var buffer = new byte[64 * 1024];
        using var ms = new MemoryStream();
        while (ws.State == WebSocketState.Open && !ct.IsCancellationRequested)
        {
            ms.SetLength(0);
            WebSocketReceiveResult result;
            do
            {
                result = await ws.ReceiveAsync(buffer, ct).ConfigureAwait(false);
                if (result.MessageType == WebSocketMessageType.Close) return;
                ms.Write(buffer, 0, result.Count);
            } while (!result.EndOfMessage);

            if (result.MessageType != WebSocketMessageType.Text) continue;
            if (StreamMessage.Decode(ms.ToArray()) is { } message)
                Raise(() => MessageReceived?.Invoke(message));
        }
    }

    // A handler error must not drop the connection.
    private void Raise(Action a)
    {
        try { a(); }
        catch (Exception ex) { _log?.Warning($"stream: handler failed: {ex.Message}"); }
    }

    public async ValueTask DisposeAsync()
    {
        _cts.Cancel();
        ClientWebSocket? ws;
        Task? loop;
        lock (_gate) { ws = _socket; loop = _loop; }
        ws?.Abort();
        if (loop is not null) { try { await loop.ConfigureAwait(false); } catch { } }
        _cts.Dispose();
        _sendLock.Dispose();
    }
}

/// <summary>
/// Keeps the retry loop of the stream from a flood of log lines. It logs a new kind of error
/// at once, and the same kind again only after the next connect.
/// </summary>
public sealed class StreamErrorLog
{
    private string? _lastKey;

    public bool ShouldLog(Exception error)
    {
        var key = Key(error);
        if (key == _lastKey) return false;
        _lastKey = key;
        return true;
    }

    public void Reset() => _lastKey = null;

    public static string Text(Exception error) => $"{Key(error)}: {error.Message}";

    /// <summary>The type and the codes only: the message changes at each try.</summary>
    private static string Key(Exception error)
    {
        var inner = error.InnerException;
        var key = $"{error.GetType().Name} {error.HResult}";
        return inner is null ? key : $"{key} <- {inner.GetType().Name} {inner.HResult}";
    }
}
