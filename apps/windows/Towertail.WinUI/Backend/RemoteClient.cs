using System.Net.Http.Headers;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.Backend;

/// <summary>
/// REST + WebSocket client for the Towertail server. Mirrors RemoteClient.swift — bearer token
/// on every request, a matching <see cref="RemoteException"/> for 4xx/5xx, and a thin WS
/// wrapper that yields decoded stream frames.
/// </summary>
public sealed class RemoteClient : IAsyncDisposable
{
    private readonly HttpClient _http;
    private readonly Uri _baseUri;
    private readonly string _token;
    private readonly JsonSerializerOptions _json;

    public RemoteClient(Uri baseUri, string token, HttpClient? http = null)
    {
        _baseUri = baseUri;
        _token = token;
        _http = http ?? new HttpClient();
        _json = new JsonSerializerOptions
        {
            DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
            PropertyNamingPolicy = null, // already using snake_case via JsonPropertyName
        };
    }

    public Uri BaseUri => _baseUri;

    public async Task<IReadOnlyList<RemoteNode>> ListNodesAsync(CancellationToken ct = default)
        => await GetJsonAsync<List<RemoteNode>>("/v1/nodes", ct).ConfigureAwait(false) ?? new();

    public Task<RemoteNode> CreateNodeAsync(RemoteNode node, CancellationToken ct = default)
        => SendJsonAsync<RemoteNode, RemoteNode>(HttpMethod.Post, "/v1/nodes", node, ct);

    public Task<RemoteNode> UpdateNodeAsync(RemoteNode node, CancellationToken ct = default)
        => SendJsonAsync<RemoteNode, RemoteNode>(HttpMethod.Put, $"/v1/nodes/{node.Id}", node, ct);

    public Task DeleteNodeAsync(Guid id, CancellationToken ct = default)
        => SendAsync(HttpMethod.Delete, $"/v1/nodes/{id}", null, ct);

    public Task<RemoteServerSettings> GetSettingsAsync(CancellationToken ct = default)
        => GetJsonAsync<RemoteServerSettings>("/v1/settings", ct)!;

    public Task<RemoteServerSettings> PutSettingsAsync(RemoteServerSettings s, CancellationToken ct = default)
        => SendJsonAsync<RemoteServerSettings, RemoteServerSettings>(HttpMethod.Put, "/v1/settings", s, ct);

    public Task<EnrollResponse> EnrollSamplerAsync(EnrollRequest req, CancellationToken ct = default)
        => SendJsonAsync<EnrollRequest, EnrollResponse>(HttpMethod.Post, "/v1/sampler/enroll", req, ct);

    public async IAsyncEnumerable<RemoteStreamFrame> StreamAsync(
        [global::System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        var wsUri = new UriBuilder(_baseUri)
        {
            Scheme = _baseUri.Scheme == "https" ? "wss" : "ws",
            Path = "/v1/stream",
        }.Uri;

        using var ws = new ClientWebSocket();
        ws.Options.SetRequestHeader("Authorization", $"Bearer {_token}");
        await ws.ConnectAsync(wsUri, ct).ConfigureAwait(false);

        var buffer = new byte[64 * 1024];
        while (ws.State == WebSocketState.Open && !ct.IsCancellationRequested)
        {
            using var ms = new MemoryStream();
            WebSocketReceiveResult result;
            do
            {
                result = await ws.ReceiveAsync(buffer, ct).ConfigureAwait(false);
                if (result.MessageType == WebSocketMessageType.Close)
                {
                    await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, null, ct).ConfigureAwait(false);
                    yield break;
                }
                ms.Write(buffer, 0, result.Count);
            } while (!result.EndOfMessage);

            RemoteStreamFrame? frame = null;
            try
            {
                frame = JsonSerializer.Deserialize<RemoteStreamFrame>(ms.ToArray(), _json);
            }
            catch { }
            if (frame != null) yield return frame;
        }
    }

    private async Task<T?> GetJsonAsync<T>(string path, CancellationToken ct)
    {
        using var req = Build(HttpMethod.Get, path);
        using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
        await EnsureOkAsync(resp, ct).ConfigureAwait(false);
        var text = await resp.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        return JsonSerializer.Deserialize<T>(text, _json);
    }

    private async Task<TResp> SendJsonAsync<TReq, TResp>(HttpMethod method, string path, TReq body, CancellationToken ct)
    {
        using var req = Build(method, path);
        req.Content = new StringContent(JsonSerializer.Serialize(body, _json), Encoding.UTF8, "application/json");
        using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
        await EnsureOkAsync(resp, ct).ConfigureAwait(false);
        var text = await resp.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        return JsonSerializer.Deserialize<TResp>(text, _json)!;
    }

    private async Task SendAsync(HttpMethod method, string path, HttpContent? content, CancellationToken ct)
    {
        using var req = Build(method, path);
        req.Content = content;
        using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
        await EnsureOkAsync(resp, ct).ConfigureAwait(false);
    }

    private HttpRequestMessage Build(HttpMethod method, string path)
    {
        var req = new HttpRequestMessage(method, new Uri(_baseUri, path));
        req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _token);
        return req;
    }

    private static async Task EnsureOkAsync(HttpResponseMessage resp, CancellationToken ct)
    {
        if (resp.IsSuccessStatusCode) return;
        var body = await resp.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        throw new RemoteException((int)resp.StatusCode, body);
    }

    public async ValueTask DisposeAsync()
    {
        _http.Dispose();
        await Task.Yield();
    }
}

public sealed class RemoteException : Exception
{
    public int Status { get; }
    public string Body { get; }

    public RemoteException(int status, string body)
        : base($"remote error {status}: {body}")
    {
        Status = status;
        Body = body;
    }
}
