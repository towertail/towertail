using System.IO.Pipes;
using System.Security.Principal;
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
        var body = await PipeGetAsync("/localapi/v0/status", ct).ConfigureAwait(false);
        if (string.IsNullOrEmpty(body)) return null;
        return JsonSerializer.Deserialize<StatusResponse>(body);
    }

    /// <summary>
    /// Same as <see cref="GetStatusAsync"/> but returns an error string instead
    /// of swallowing exceptions. Used by the wizard so the user sees "Tailscale
    /// not running" rather than an empty list.
    /// </summary>
    public async Task<(StatusResponse? Status, string? Error)> TryGetStatusAsync(CancellationToken ct = default)
    {
        try
        {
            var status = await GetStatusAsync(ct).ConfigureAwait(false);
            if (status is null) return (null, "Tailscale returned an empty response.");
            return (status, null);
        }
        catch (TimeoutException)
        {
            return (null, "Timed out talking to tailscaled — is Tailscale running?");
        }
        catch (UnauthorizedAccessException)
        {
            return (null, "Access denied by tailscaled. Update Tailscale or sign in to the tailnet.");
        }
        catch (System.IO.FileNotFoundException)
        {
            return (null, "Tailscale isn't installed, or its service isn't running.");
        }
        catch (Exception ex)
        {
            return (null, $"Couldn't reach Tailscale: {ex.Message}");
        }
    }

    private static async Task<string> PipeGetAsync(string path, CancellationToken ct)
    {
        // tailscaled requires at least SECURITY_IDENTIFICATION impersonation so
        // it can GetTokenInformation on the caller (see tailscale#18212). The
        // no-impersonation default on NamedPipeClientStream makes the daemon
        // refuse the connection with UnauthorizedAccessException even though
        // the DACL (set in safesocket/pipe_windows.go) grants BUILTIN\Users GRGW.
        await using var pipe = new NamedPipeClientStream(
            ".",
            PipePath,
            PipeDirection.InOut,
            PipeOptions.Asynchronous,
            TokenImpersonationLevel.Identification);
        await pipe.ConnectAsync(5000, ct).ConfigureAwait(false);
        // Empty user + empty password Basic auth mirrors what tsnet / the macOS
        // client sends. Some tailscaled versions require the header even though
        // the transport itself is authenticated by the pipe ACL.
        var basic = Convert.ToBase64String(Encoding.ASCII.GetBytes(":"));
        var request =
            $"GET {path} HTTP/1.1\r\n" +
            "Host: local-tailscaled.sock\r\n" +
            $"Authorization: Basic {basic}\r\n" +
            "Connection: close\r\n\r\n";
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
        return DecodeHttpResponse(ms.ToArray());
    }

    /// <summary>
    /// Minimal HTTP/1.1 response decoder. Splits headers from body at the
    /// first blank line, then decodes the body per Transfer-Encoding: chunked
    /// or Content-Length. tailscaled returns chunked responses, and the
    /// previous "everything after \r\n\r\n" strategy left hex chunk-size
    /// markers inline with the JSON — which made System.Text.Json report a
    /// bogus "'d' is invalid within a number" because it was looking at
    /// "5d\r\n{...}" instead of just "{...}".
    /// </summary>
    private static string DecodeHttpResponse(byte[] bytes)
    {
        int headerEnd = IndexOfSequence(bytes, HeaderSep);
        if (headerEnd < 0) return Encoding.UTF8.GetString(bytes);
        var headerText = Encoding.ASCII.GetString(bytes, 0, headerEnd);
        int bodyStart = headerEnd + HeaderSep.Length;
        int bodyLen = bytes.Length - bodyStart;

        bool chunked = false;
        foreach (var line in headerText.Split("\r\n"))
        {
            var c = line.IndexOf(':');
            if (c < 0) continue;
            if (string.Equals(line[..c].Trim(), "Transfer-Encoding", StringComparison.OrdinalIgnoreCase)
                && line[(c + 1)..].Contains("chunked", StringComparison.OrdinalIgnoreCase))
            {
                chunked = true;
                break;
            }
        }

        if (!chunked) return Encoding.UTF8.GetString(bytes, bodyStart, bodyLen);

        using var dec = new MemoryStream();
        int p = bodyStart;
        int end = bytes.Length;
        while (p < end)
        {
            int lineEnd = IndexOfSequence(bytes, CrLf, p);
            if (lineEnd < 0) break;
            var sizeLine = Encoding.ASCII.GetString(bytes, p, lineEnd - p);
            var semi = sizeLine.IndexOf(';');
            if (semi >= 0) sizeLine = sizeLine[..semi];
            if (!int.TryParse(sizeLine.Trim(), System.Globalization.NumberStyles.HexNumber, null, out var size))
                break;
            p = lineEnd + CrLf.Length;
            if (size == 0) break;
            if (p + size > end) break;
            dec.Write(bytes, p, size);
            p += size;
            // Each chunk is followed by CRLF.
            if (p + CrLf.Length <= end) p += CrLf.Length;
        }
        return Encoding.UTF8.GetString(dec.ToArray());
    }

    private static readonly byte[] HeaderSep = Encoding.ASCII.GetBytes("\r\n\r\n");
    private static readonly byte[] CrLf = Encoding.ASCII.GetBytes("\r\n");

    private static int IndexOfSequence(byte[] haystack, byte[] needle, int start = 0)
    {
        for (int i = start; i <= haystack.Length - needle.Length; i++)
        {
            bool match = true;
            for (int j = 0; j < needle.Length; j++)
            {
                if (haystack[i + j] != needle[j]) { match = false; break; }
            }
            if (match) return i;
        }
        return -1;
    }
}
