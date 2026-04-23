using System.Net;
using System.Net.WebSockets;
using System.Text;

namespace Towertail.Tests.Helpers;

/// <summary>
/// In-process HTTP/1.1 + WebSocket-upgrade server on 127.0.0.1:0. Mirrors the Mac app's
/// LocalTestServer.swift — handlers keyed by <c>"METHOD PATH"</c> with prefix match, request
/// capture, and <see cref="SendToAllWebSocketsAsync"/> for scripted frames.
/// </summary>
public sealed class LocalTestHttpServer : IAsyncDisposable
{
    private readonly HttpListener _listener;
    private readonly List<WebSocket> _sockets = new();
    private readonly CancellationTokenSource _cts = new();
    private readonly Task _loop;
    public int Port { get; }
    public string BaseUrl => $"http://127.0.0.1:{Port}";
    public List<CapturedRequest> Requests { get; } = new();
    public string ExpectedToken { get; set; } = "test-token";

    public Dictionary<string, Func<HttpListenerContext, Task>> Handlers { get; } = new();

    public LocalTestHttpServer()
    {
        // Bind to an ephemeral loopback port. HttpListener doesn't expose port allocation,
        // so we try a small range until one sticks.
        var rng = new Random();
        HttpListener? bound = null;
        int boundPort = 0;
        for (int i = 0; i < 32; i++)
        {
            var p = rng.Next(12000, 19000);
            var l = new HttpListener();
            l.Prefixes.Add($"http://127.0.0.1:{p}/");
            try { l.Start(); bound = l; boundPort = p; break; }
            catch { l.Close(); }
        }
        _listener = bound ?? throw new InvalidOperationException("could not bind ephemeral port");
        Port = boundPort;
        _loop = Task.Run(LoopAsync);
    }

    private async Task LoopAsync()
    {
        while (!_cts.IsCancellationRequested)
        {
            HttpListenerContext ctx;
            try { ctx = await _listener.GetContextAsync().ConfigureAwait(false); }
            catch { return; }

            _ = Task.Run(async () =>
            {
                try
                {
                    var path = ctx.Request.Url!.AbsolutePath;
                    var method = ctx.Request.HttpMethod;
                    var key = $"{method} {path}";
                    var auth = ctx.Request.Headers["Authorization"] ?? "";
                    var tokenOk = auth == $"Bearer {ExpectedToken}";

                    var body = await new StreamReader(ctx.Request.InputStream).ReadToEndAsync().ConfigureAwait(false);
                    Requests.Add(new CapturedRequest(method, path, auth, body));

                    if (!tokenOk && !path.StartsWith("/healthz"))
                    {
                        ctx.Response.StatusCode = 401;
                        ctx.Response.Close();
                        return;
                    }

                    // Prefix match against registered handlers.
                    Func<HttpListenerContext, Task>? handler = null;
                    foreach (var kv in Handlers)
                    {
                        if (key.StartsWith(kv.Key)) { handler = kv.Value; break; }
                    }
                    if (handler is null)
                    {
                        ctx.Response.StatusCode = 404;
                        ctx.Response.Close();
                        return;
                    }
                    await handler(ctx).ConfigureAwait(false);
                }
                catch (Exception ex)
                {
                    try
                    {
                        ctx.Response.StatusCode = 500;
                        using var w = new StreamWriter(ctx.Response.OutputStream);
                        await w.WriteAsync(ex.Message).ConfigureAwait(false);
                    }
                    catch { }
                }
            });
        }
    }

    public async Task SendToAllWebSocketsAsync(string json)
    {
        var bytes = Encoding.UTF8.GetBytes(json);
        foreach (var ws in _sockets.ToList())
        {
            if (ws.State != WebSocketState.Open) continue;
            try { await ws.SendAsync(bytes, WebSocketMessageType.Text, true, CancellationToken.None).ConfigureAwait(false); }
            catch { }
        }
    }

    public async Task AcceptWebSocketAsync(HttpListenerContext ctx)
    {
        var wsCtx = await ctx.AcceptWebSocketAsync(null).ConfigureAwait(false);
        _sockets.Add(wsCtx.WebSocket);
    }

    public async ValueTask DisposeAsync()
    {
        _cts.Cancel();
        try { _listener.Stop(); } catch { }
        foreach (var ws in _sockets)
        {
            try { await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, null, CancellationToken.None).ConfigureAwait(false); }
            catch { }
        }
        try { await _loop.ConfigureAwait(false); } catch { }
    }

    public sealed record CapturedRequest(string Method, string Path, string Authorization, string Body);
}
