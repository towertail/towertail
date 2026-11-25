using System.IO.Compression;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Backend;

/// <summary>Makes the cloud node and the sampler token. <see cref="RemoteClient"/> is the real one.</summary>
public interface IEnroller
{
    Task<EnrollResponse> EnrollSamplerAsync(EnrollRequest req, CancellationToken ct = default);
}

/// <summary>
/// REST + WebSocket client for the Towertail server. Mirrors RemoteClient.swift — bearer token
/// on every request, a matching <see cref="RemoteException"/> for 4xx/5xx, and a factory for
/// the self-reconnecting <see cref="RemoteStream"/>.
/// </summary>
public sealed class RemoteClient : IEnroller, IAsyncDisposable
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

    /// <summary>
    /// Start a disk scan. When a job of the node is queued or running (409), returns that
    /// job, so that the caller follows it. A 403 means that disk scans are off for the node.
    /// </summary>
    public async Task<DiskScanJob> StartDiskScanAsync(Guid nodeId, DiskScanRequest request, CancellationToken ct = default)
    {
        try
        {
            return await SendJsonAsync<DiskScanRequest, DiskScanJob>(
                HttpMethod.Post, $"/v1/nodes/{nodeId}/disk-scans", request, ct).ConfigureAwait(false);
        }
        catch (RemoteException ex) when (ex.Status == 409)
        {
            DiskScanConflict? conflict = null;
            try { conflict = JsonSerializer.Deserialize<DiskScanConflict>(ex.Body, _json); } catch { }
            if (conflict?.Job is { } job) return job;
            throw;
        }
    }

    /// <summary>The disk-scan jobs of a node, the newest first.</summary>
    public async Task<IReadOnlyList<DiskScanJob>> ListDiskScansAsync(Guid nodeId, CancellationToken ct = default)
        => (await GetJsonAsync<DiskScanJobList>($"/v1/nodes/{nodeId}/disk-scans", ct).ConfigureAwait(false))?.Jobs
           ?? Array.Empty<DiskScanJob>();

    public async Task<DiskScanJob> GetDiskScanAsync(Guid jobId, CancellationToken ct = default)
        => (await GetJsonAsync<DiskScanJob>($"/v1/disk-scans/{jobId}", ct).ConfigureAwait(false))
           ?? throw new RemoteException(200, "empty job");

    public async Task<DiskScanJob> CancelDiskScanAsync(Guid jobId, CancellationToken ct = default)
    {
        using var req = Build(HttpMethod.Post, $"/v1/disk-scans/{jobId}/cancel");
        using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
        await EnsureOkAsync(resp, ct).ConfigureAwait(false);
        var text = await resp.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        return JsonSerializer.Deserialize<DiskScanJob>(text, _json)!;
    }

    /// <summary>The result line of a done job. The server sends it gzip.</summary>
    public async Task<DiskScanResult> GetDiskScanResultAsync(Guid jobId, CancellationToken ct = default)
    {
        using var req = Build(HttpMethod.Get, $"/v1/disk-scans/{jobId}/result");
        using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
        await EnsureOkAsync(resp, ct).ConfigureAwait(false);
        var bytes = await resp.Content.ReadAsByteArrayAsync(ct).ConfigureAwait(false);
        // The handler can decode the gzip already; look at the magic bytes, not the header.
        if (bytes.Length >= 2 && bytes[0] == 0x1f && bytes[1] == 0x8b)
        {
            using var gz = new GZipStream(new MemoryStream(bytes), CompressionMode.Decompress);
            using var ms = new MemoryStream();
            await gz.CopyToAsync(ms, ct).ConfigureAwait(false);
            bytes = ms.ToArray();
        }
        return DiskScanCodec.DecodeResult(Encoding.UTF8.GetString(bytes));
    }

    /// <summary>
    /// Makes a stream for <c>/v1/stream</c> (<c>?lease=1</c> in lease mode, docs/wire.md §4).
    /// Attach the handlers, then call <see cref="RemoteStream.Start"/>.
    /// </summary>
    public RemoteStream OpenStream(bool lease, Logger? log = null)
    {
        var wsUri = new UriBuilder(_baseUri)
        {
            Scheme = _baseUri.Scheme == "https" ? "wss" : "ws",
            Path = "/v1/stream",
            Query = lease ? "lease=1" : "",
        }.Uri;
        return new RemoteStream(wsUri, _token, log: log);
    }

    /// <summary>The open alerts of the org. The cloud feed reads the reachability ones.</summary>
    public async Task<IReadOnlyList<RemoteAlert>> OpenAlertsAsync(CancellationToken ct = default)
        => (await GetJsonAsync<RemoteAlertList>("/v1/alerts", ct).ConfigureAwait(false))?.Alerts
           ?? Array.Empty<RemoteAlert>();

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
