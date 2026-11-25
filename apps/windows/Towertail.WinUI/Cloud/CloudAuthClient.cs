using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.Cloud;

public enum CloudAuthErrorKind { BadEmail, WrongCode, Locked, Unauthorized, Network, Server }

public sealed class CloudAuthException(CloudAuthErrorKind kind, string message, int? status = null, Exception? inner = null)
    : Exception(message, inner)
{
    public CloudAuthErrorKind Kind { get; } = kind;
    public int? Status { get; } = status;
}

public sealed record CloudUser([property: JsonPropertyName("email")] string Email);

public sealed record CloudOrg(
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("plan")] string Plan);

/// <summary>The reply of <c>GET /v1/me</c>. <c>Role</c> is empty on a server that sends none.</summary>
public sealed record CloudMe(
    [property: JsonPropertyName("user")] CloudUser User,
    [property: JsonPropertyName("org")] CloudOrg Org,
    [property: JsonPropertyName("role")] string Role = "")
{
    [JsonIgnore] public bool IsAdmin => Role is "owner" or "admin";
    [JsonIgnore] public bool CanMute => Role is "owner" or "admin" or "member";
}

public sealed record CloudPlan(
    [property: JsonPropertyName("id")] string Id,
    [property: JsonPropertyName("maxNodes")] int MaxNodes);

public sealed record CloudBilling(
    [property: JsonPropertyName("plan")] CloudPlan Plan,
    [property: JsonPropertyName("nodes")] int Nodes,
    // The host cap of the org with packs. Null when the server is older.
    [property: JsonPropertyName("maxNodes")] int? MaxNodes = null,
    // Null when the server is older than the payments flag. Null means on.
    [property: JsonPropertyName("payments")] bool? Payments = null)
{
    /// <summary>Hosts that the org can still add. Null when the cap is not known.</summary>
    [JsonIgnore] public int? Room => MaxNodes is int max ? Math.Max(0, max - Nodes) : null;
    [JsonIgnore] public bool CanBuy => Payments ?? true;
}

public sealed record CloudOrgDeletion(
    [property: JsonPropertyName("id")] Guid Id,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("role")] string Role,
    // "delete", "leave" or "blocked".
    [property: JsonPropertyName("fate")] string Fate,
    [property: JsonPropertyName("members")] int Members,
    [property: JsonPropertyName("nodes")] int Nodes,
    [property: JsonPropertyName("reason")] string? Reason = null);

public sealed record CloudDeletion(
    [property: JsonPropertyName("email")] string Email,
    [property: JsonPropertyName("orgs")] IReadOnlyList<CloudOrgDeletion> Orgs,
    [property: JsonPropertyName("blocked")] bool Blocked);

/// <summary>
/// The sign-in and account routes of Towertail Cloud. They are separate from
/// <see cref="Backend.RemoteClient"/>, which needs a token for each call. Mirrors the Mac <c>CloudAuthClient</c>.
/// </summary>
public sealed class CloudAuthClient(Uri endpoint, HttpClient? http = null)
{
    public const string Label = "Towertail for Windows";

    private static readonly JsonSerializerOptions s_json = new() { PropertyNameCaseInsensitive = true };

    private readonly HttpClient _http = http ?? new HttpClient { Timeout = TimeSpan.FromSeconds(20) };

    public Uri Endpoint => endpoint;

    public async Task SendCodeAsync(string email, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Post, "/v1/auth/code", null, new { email }, ct).ConfigureAwait(false);
        if (status is >= 200 and < 300) return;
        throw status == 400
            ? new CloudAuthException(CloudAuthErrorKind.BadEmail, "The email address is not valid.", status)
            : Server(status, body);
    }

    /// <summary>Gives the user token.</summary>
    public async Task<string> VerifyAsync(string email, string code, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Post, "/v1/auth/code/verify", null,
            new { email, code, want = "token", label = Label }, ct).ConfigureAwait(false);
        switch (status)
        {
            case >= 200 and < 300:
                var token = Decode<TokenReply>(status, body).Token;
                return string.IsNullOrEmpty(token) ? throw new CloudAuthException(CloudAuthErrorKind.Server, "Cloud sent no token.", status) : token;
            case 401: throw new CloudAuthException(CloudAuthErrorKind.WrongCode, "The code is not correct.", status);
            case 429: throw new CloudAuthException(CloudAuthErrorKind.Locked, "Too many wrong codes. Send a new code.", status);
            default: throw Server(status, body);
        }
    }

    public async Task<CloudMe> MeAsync(string token, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Get, "/v1/me", token, null, ct).ConfigureAwait(false);
        Ensure(status, body);
        return Decode<CloudMe>(status, body);
    }

    /// <summary>Null when this cloud has no billing routes (404).</summary>
    public async Task<CloudBilling?> BillingAsync(string token, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Get, "/v1/billing", token, null, ct).ConfigureAwait(false);
        if (status == 404) return null;
        Ensure(status, body);
        return Decode<CloudBilling>(status, body);
    }

    /// <summary>A one-time link that opens the dashboard signed in.</summary>
    public async Task<Uri> HandoffAsync(string token, string redirect, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Post, "/v1/auth/handoff", token, new { redirect }, ct).ConfigureAwait(false);
        Ensure(status, body);
        return Uri.TryCreate(Decode<HandoffReply>(status, body).Url, UriKind.Absolute, out var url)
            ? url : throw new CloudAuthException(CloudAuthErrorKind.Server, "Cloud sent no link.", status);
    }

    /// <summary>What an account delete does to each org. Null when this cloud cannot delete accounts (404).</summary>
    public async Task<CloudDeletion?> DeletionAsync(string token, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Get, "/v1/me/deletion", token, null, ct).ConfigureAwait(false);
        if (status == 404) return null;
        Ensure(status, body);
        return Decode<CloudDeletion>(status, body);
    }

    /// <summary>Deletes the account. The server checks the email.</summary>
    public async Task DeleteAccountAsync(string token, string email, CancellationToken ct = default)
    {
        var (status, body) = await SendAsync(HttpMethod.Delete, "/v1/me", token, new { email }, ct).ConfigureAwait(false);
        Ensure(status, body);
    }

    private sealed record TokenReply([property: JsonPropertyName("token")] string? Token);
    private sealed record HandoffReply([property: JsonPropertyName("url")] string? Url);

    private static void Ensure(int status, string body)
    {
        if (status is >= 200 and < 300) return;
        throw status == 401
            ? new CloudAuthException(CloudAuthErrorKind.Unauthorized, "The sign-in is not valid any more.", status)
            : Server(status, body);
    }

    private static CloudAuthException Server(int status, string body)
        => new(CloudAuthErrorKind.Server, ErrorText(body) ?? $"Cloud replied with status {status}.", status);

    private static T Decode<T>(int status, string body)
    {
        try { return JsonSerializer.Deserialize<T>(body, s_json) ?? throw new JsonException("empty"); }
        catch (JsonException)
        {
            throw new CloudAuthException(CloudAuthErrorKind.Server, "Cloud sent a reply that the app cannot read.", status);
        }
    }

    /// <summary>The text of a wire error envelope (<c>{"error":{"message"}}</c>) or of an echo <c>{"message"}</c> reply.</summary>
    private static string? ErrorText(string body)
    {
        try
        {
            using var doc = JsonDocument.Parse(body);
            var root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object) return null;
            if (root.TryGetProperty("error", out var err) && err.ValueKind == JsonValueKind.Object
                && err.TryGetProperty("message", out var m) && m.ValueKind == JsonValueKind.String) return m.GetString();
            return root.TryGetProperty("message", out var top) && top.ValueKind == JsonValueKind.String ? top.GetString() : null;
        }
        catch (JsonException) { return null; }
    }

    private async Task<(int Status, string Body)> SendAsync(HttpMethod method, string path, string? token, object? body, CancellationToken ct)
    {
        using var req = new HttpRequestMessage(method, new Uri(endpoint, path));
        if (token is not null) req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        if (body is not null) req.Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json");
        try
        {
            using var resp = await _http.SendAsync(req, ct).ConfigureAwait(false);
            var text = await resp.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            return ((int)resp.StatusCode, text);
        }
        catch (OperationCanceledException) when (ct.IsCancellationRequested) { throw; }
        catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException or IOException)
        {
            throw new CloudAuthException(CloudAuthErrorKind.Network, "Cannot reach Towertail Cloud. Check your network.", null, ex);
        }
    }
}
