using System.Runtime.InteropServices;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs the bundled <c>towertail-sampler.exe</c> from the Assets directory for this PC's arch.
/// Parity with Mac's LocalSamplerInvoker — resolves the triple from the runtime arch, then
/// spawns the binary with <c>--once</c> / <c>--interval</c> / <c>--self-check</c>.
/// </summary>
public sealed class LocalSamplerInvoker : ISamplerInvoker
{
    private readonly IProcessRunner _runner;
    private readonly string _executablePath;

    public LocalSamplerInvoker(IProcessRunner runner, string? explicitPath = null)
    {
        _runner = runner;
        _executablePath = explicitPath ?? ResolveBundledSampler();
    }

    public string ExecutablePath => _executablePath;

    public async Task<Sample> RunOnceAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            new ProcessRequest(_executablePath, new[] { "--once" }, Timeout: TimeSpan.FromSeconds(15)),
            ct).ConfigureAwait(false);
        if (!result.Ok)
            throw new InvalidOperationException($"sampler exit {result.ExitCode}: {result.StdErr.Trim()}");
        return SampleCodec.Decode(result.StdOut);
    }

    public async IAsyncEnumerable<Sample> StreamAsync(
        TimeSpan interval,
        [global::System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        var seconds = Math.Max(1, (int)interval.TotalSeconds);
        await using var proc = await _runner.StartStreamingAsync(
            new ProcessRequest(_executablePath, new[] { "--interval", $"{seconds}s" }),
            ct).ConfigureAwait(false);
        while (!ct.IsCancellationRequested)
        {
            var line = await proc.StdOut.ReadLineAsync(ct).ConfigureAwait(false);
            if (line is null) yield break;
            if (string.IsNullOrWhiteSpace(line)) continue;
            Sample? sample = null;
            try { sample = SampleCodec.Decode(line); }
            catch { /* drop malformed line; see errors[] in following samples */ }
            if (sample != null) yield return sample;
        }
    }

    public async Task<string> VersionAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            new ProcessRequest(_executablePath, new[] { "--version" }, Timeout: TimeSpan.FromSeconds(5)),
            ct).ConfigureAwait(false);
        return result.StdOut.Trim();
    }

    public async Task<bool> SelfCheckAsync(CancellationToken ct = default)
    {
        var result = await _runner.RunAsync(
            new ProcessRequest(_executablePath, new[] { "--self-check" }, Timeout: TimeSpan.FromSeconds(5)),
            ct).ConfigureAwait(false);
        return result.Ok && result.StdOut.Trim() == "ok";
    }

    /// <summary>
    /// Pick the triple (windows-amd64 / windows-arm64) matching the current runtime and
    /// return the absolute path to the bundled executable under <c>Assets\samplers\</c>.
    /// </summary>
    public static string ResolveBundledSampler(string? bundleRoot = null)
    {
        var arch = RuntimeInformation.ProcessArchitecture switch
        {
            Architecture.X64 => "windows-amd64",
            Architecture.Arm64 => "windows-arm64",
            _ => "windows-amd64",
        };
        var root = bundleRoot ?? AppContext.BaseDirectory;
        var path = Path.Combine(root, "Assets", "samplers", arch, "towertail-sampler.exe");
        return path;
    }
}
