using System.IO.Compression;
using System.Text.Json;
using System.Text.Json.Serialization;
using Towertail.WinUI.Backend;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>The JSON of <c>towertail-sampler status</c>.</summary>
public sealed record SamplerStatus(
    [property: JsonPropertyName("running")] bool Running,
    [property: JsonPropertyName("pid")] int? Pid = null,
    [property: JsonPropertyName("version")] string? Version = null,
    [property: JsonPropertyName("endpoint")] string? Endpoint = null,
    [property: JsonPropertyName("last_push")] DateTime? LastPush = null,
    [property: JsonPropertyName("last_status")] int? LastStatus = null,
    [property: JsonPropertyName("last_error")] string? LastError = null,
    [property: JsonPropertyName("reboot")] string? Reboot = null,
    [property: JsonPropertyName("update")] string? Update = null,
    [property: JsonPropertyName("interval")] string? Interval = null,
    [property: JsonPropertyName("paused_until")] DateTime? PausedUntil = null,
    [property: JsonPropertyName("gone")] string? Gone = null);

public enum LifecycleFailure { PlanFull, NotSignedIn, StartFailed, StatusUnreadable, NoBootMethod, UnsupportedHost }

/// <summary>A step of the sampler install failed. The message is the text for the user.</summary>
public sealed class SamplerLifecycleException(LifecycleFailure kind, string message, int? planMax = null)
    : Exception(message)
{
    public LifecycleFailure Kind { get; } = kind;
    public int? PlanMax { get; } = planMax;

    public static SamplerLifecycleException PlanFull(int? max)
        => new(LifecycleFailure.PlanFull, max is { } m ? $"The plan is full ({m} of {m})." : "The plan is full.", max);

    public static SamplerLifecycleException NotSignedIn()
        => new(LifecycleFailure.NotSignedIn, "Sign in to Towertail Cloud first.");

    public static SamplerLifecycleException StartFailed(string detail)
        => new(LifecycleFailure.StartFailed, detail.Trim().Length == 0 ? "The sampler did not start." : $"The sampler did not start: {detail.Trim()}");

    public static SamplerLifecycleException StatusUnreadable()
        => new(LifecycleFailure.StatusUnreadable, "The sampler status is not readable.");

    public static SamplerLifecycleException NoBootMethod()
        => new(LifecycleFailure.NoBootMethod, "No way to start the sampler after a reboot.");

    public static SamplerLifecycleException Unsupported()
        => new(LifecycleFailure.UnsupportedHost, "Not supported on this host");
}

/// <summary>
/// The remote commands that install and run a sampler. It needs only a session, so tests need no SSH.
/// Mirrors the Mac <c>SamplerLifecycle</c>.
/// </summary>
public sealed class SamplerLifecycle(ISamplerSession session, IEnroller? enroller = null)
{
    private const string Bin = "~/.towertail/towertail-sampler";
    private const string TokenIn = ".towertail/sampler.token.in";

    private static readonly JsonSerializerOptions s_json = new() { PropertyNameCaseInsensitive = true };

    /// <summary>The sampler triple of the host, for example <c>linux-arm64</c>.</summary>
    public async Task<string> DetectAsync(CancellationToken ct = default)
    {
        var (code, text, _) = await session.RunAsync("uname -sm", ct).ConfigureAwait(false);
        if (code != 0) throw SamplerLifecycleException.Unsupported();
        var found = SshBootstrap.ParseUnameOrVer(text);
        if (found.Os is not (SshBootstrap.RemoteOs.Linux or SshBootstrap.RemoteOs.Darwin))
            throw SamplerLifecycleException.Unsupported();
        return found.Triple;
    }

    /// <summary>The copy step. A sampler that runs is stopped, so that the new binary replaces it.</summary>
    public async Task InstallAsync(string localBinary, CancellationToken ct = default)
    {
        await session.RunAsync($"if [ -x {Bin} ]; then {Bin} stop; fi", ct).ConfigureAwait(false);
        var home = await HomeAsync(ct).ConfigureAwait(false);
        await session.RunAsync("mkdir -p ~/.towertail", ct).ConfigureAwait(false);
        var remote = $"{home}/.towertail/towertail-sampler";

        // A gzip upload sends about 60% less. A host with no gunzip gets the raw file.
        using (var packed = new MemoryStream())
        {
            await using (var gz = new GZipStream(packed, CompressionLevel.Optimal, leaveOpen: true))
            await using (var file = File.OpenRead(localBinary))
                await file.CopyToAsync(gz, ct).ConfigureAwait(false);
            packed.Position = 0;
            await session.PutAsync(packed, remote + ".gz", ct).ConfigureAwait(false);
        }
        var (code, _, _) = await session.RunAsync($"gunzip -f {Bin}.gz", ct).ConfigureAwait(false);
        if (code != 0)
        {
            await using var raw = File.OpenRead(localBinary);
            await session.PutAsync(raw, remote, ct).ConfigureAwait(false);
        }
        await session.RunAsync($"chmod 755 {Bin}", ct).ConfigureAwait(false);
    }

    /// <summary>The machine id that the sampler reports in <c>--once</c>. Null when it has none.</summary>
    public async Task<string?> MachineIdAsync(CancellationToken ct = default)
    {
        try
        {
            var (code, text, _) = await session.RunAsync($"{Bin} --once", ct).ConfigureAwait(false);
            if (code != 0) return null;
            var id = SampleCodec.Decode(text).Host.MachineId;
            return string.IsNullOrEmpty(id) ? null : id;
        }
        catch (Exception ex) when (ex is JsonException or FormatException or InvalidOperationException) { return null; }
    }

    public async Task<EnrollResponse> EnrollAsync(Node node, CancellationToken ct = default)
    {
        if (enroller is null) throw SamplerLifecycleException.NotSignedIn();
        var machine = await MachineIdAsync(ct).ConfigureAwait(false);
        try
        {
            return await enroller.EnrollSamplerAsync(new EnrollRequest(node.DisplayName, node.Id, machine), ct).ConfigureAwait(false);
        }
        catch (RemoteException ex) when (ex.Status == 402)
        {
            throw SamplerLifecycleException.PlanFull(PlanMax(ex.Body));
        }
    }

    /// <summary>The token goes to a file that only the user reads, and the sampler reads it on stdin. It is never in argv.</summary>
    public async Task<string> StartAsync(Uri endpoint, string samplerToken, CancellationToken ct = default)
    {
        var home = await HomeAsync(ct).ConfigureAwait(false);
        await session.RunAsync($"mkdir -p ~/.towertail && umask 077 && : > ~/{TokenIn}", ct).ConfigureAwait(false);
        using (var token = new MemoryStream(System.Text.Encoding.UTF8.GetBytes(samplerToken + "\n")))
            await session.PutAsync(token, $"{home}/{TokenIn}", ct).ConfigureAwait(false);
        var file = "~/" + TokenIn;
        return await StartCommandAsync(
            $"{Bin} start --endpoint {SshDiskScanner.ShellQuote(endpoint.ToString())} --token-file - --boot none < {file}; rc=$?; rm -f {file}; exit $rc", ct)
            .ConfigureAwait(false);
    }

    /// <summary>Starts the sampler with the saved config.</summary>
    public Task<string> RestartAsync(CancellationToken ct = default) => StartCommandAsync($"{Bin} start", ct);

    /// <summary>Sets the reboot entry and returns the mode that works.</summary>
    public async Task<string> EnableBootAsync(CancellationToken ct = default)
    {
        var (code, text, _) = await session.RunAsync($"{Bin} boot enable --mode auto", ct).ConfigureAwait(false);
        if (code != 0) throw SamplerLifecycleException.NoBootMethod();
        try
        {
            if (JsonSerializer.Deserialize<BootReply>(text, s_json)?.Reboot is { Length: > 0 } mode) return mode;
        }
        catch (JsonException) { }
        throw SamplerLifecycleException.NoBootMethod();
    }

    public async Task<SamplerStatus> StatusAsync(CancellationToken ct = default)
    {
        var (_, text, _) = await session.RunAsync($"{Bin} status", ct).ConfigureAwait(false);
        try { return JsonSerializer.Deserialize<SamplerStatus>(text, s_json) ?? throw SamplerLifecycleException.StatusUnreadable(); }
        catch (JsonException) { throw SamplerLifecycleException.StatusUnreadable(); }
    }

    public async Task UpdateAsync(string localBinary, CancellationToken ct = default)
    {
        await InstallAsync(localBinary, ct).ConfigureAwait(false);
        await RestartAsync(ct).ConfigureAwait(false);
    }

    public async Task RemoveAsync(CancellationToken ct = default)
    {
        await session.RunAsync($"{Bin} stop", ct).ConfigureAwait(false);
        var (code, _, err) = await session.RunAsync($"{Bin} boot disable", ct).ConfigureAwait(false);
        // 127: no sampler on the host, so no reboot entry.
        if (code is not (0 or 127)) throw SamplerLifecycleException.StartFailed(err);
        await session.RunAsync("rm -f ~/.towertail/sampler.token ~/.towertail/sampler.json", ct).ConfigureAwait(false);
    }

    private async Task<string> StartCommandAsync(string command, CancellationToken ct)
    {
        var (code, text, err) = await session.RunAsync(command, ct).ConfigureAwait(false);
        if (code != 0) throw SamplerLifecycleException.StartFailed(err);
        return text.Trim();
    }

    /// <summary>SFTP needs the absolute home. It does not expand <c>~</c>.</summary>
    private async Task<string> HomeAsync(CancellationToken ct)
    {
        var (_, text, _) = await session.RunAsync("echo $HOME", ct).ConfigureAwait(false);
        var home = text.Trim();
        if (home.Length == 0) throw SamplerLifecycleException.StartFailed("The home folder is empty.");
        return home;
    }

    /// <summary>The limit in "the plan allows 3 hosts".</summary>
    private static int? PlanMax(string body)
    {
        var digits = new string(body.SkipWhile(c => !char.IsAsciiDigit(c)).TakeWhile(char.IsAsciiDigit).ToArray());
        return int.TryParse(digits, out var n) ? n : null;
    }

    private sealed record BootReply([property: JsonPropertyName("reboot")] string? Reboot);
}
