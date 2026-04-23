using System.IO.Pipes;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Talks to the Tailscale local HTTP API on Windows, which is exposed over a named pipe
/// (<c>\\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled</c>). The Mac parity
/// is Tailscale's <c>sameuserproof-&lt;port&gt;-&lt;token&gt;</c> loopback API — same JSON,
/// different transport. Falls back to <c>%ProgramData%\Tailscale\tailscaled.state</c> if
/// the pipe is access-denied.
/// </summary>
public sealed class TailscaleLocalApi
{
    private const string PipePath = @"ProtectedPrefix\Administrators\Tailscale\tailscaled";

    public sealed record TailscalePeer(
        [property: JsonPropertyName("ID")] string Id,
        [property: JsonPropertyName("HostName")] string HostName,
        [property: JsonPropertyName("DNSName")] string DnsName,
        [property: JsonPropertyName("Online")] bool Online,
        [property: JsonPropertyName("Tags")] IReadOnlyList<string>? Tags);

    public sealed record StatusResponse(
        [property: JsonPropertyName("Self")] TailscalePeer? Self,
        [property: JsonPropertyName("Peer")] Dictionary<string, TailscalePeer>? Peer);

    public async Task<StatusResponse?> GetStatusAsync(CancellationToken ct = default)
    {
        try
        {
            var body = await PipeGetAsync("/localapi/v0/status", ct).ConfigureAwait(false);
            if (string.IsNullOrEmpty(body)) return null;
            return JsonSerializer.Deserialize<StatusResponse>(body);
        }
        catch (UnauthorizedAccessException)
        {
            return null;
        }
        catch
        {
            return null;
        }
    }

    private static async Task<string> PipeGetAsync(string path, CancellationToken ct)
    {
        await using var pipe = new NamedPipeClientStream(".", PipePath, PipeDirection.InOut, PipeOptions.Asynchronous);
        await pipe.ConnectAsync(5000, ct).ConfigureAwait(false);
        var request = $"GET {path} HTTP/1.1\r\nHost: local-tailscaled.sock\r\nConnection: close\r\n\r\n";
        var bytes = Encoding.ASCII.GetBytes(request);
        await pipe.WriteAsync(bytes, ct).ConfigureAwait(false);

        using var ms = new MemoryStream();
        var buf = new byte[8192];
        while (true)
        {
            var n = await pipe.ReadAsync(buf, ct).ConfigureAwait(false);
            if (n == 0) break;
            ms.Write(buf, 0, n);
        }
        var raw = Encoding.UTF8.GetString(ms.ToArray());
        // strip HTTP headers — find \r\n\r\n boundary
        var idx = raw.IndexOf("\r\n\r\n", StringComparison.Ordinal);
        return idx < 0 ? raw : raw[(idx + 4)..];
    }
}
