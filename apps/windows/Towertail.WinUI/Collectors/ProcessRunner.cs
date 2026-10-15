using System.Diagnostics;
using System.Text;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Seam so tests can assert the exact argv shape handed to ssh.exe / scp.exe / the sampler
/// without spawning real processes. Production code uses <see cref="DefaultProcessRunner"/>.
/// </summary>
public interface IProcessRunner
{
    Task<ProcessResult> RunAsync(ProcessRequest request, CancellationToken ct = default);

    /// <summary>
    /// Spawn a streaming process (NDJSON producer). The returned stream is the process's
    /// stdout; the caller is responsible for disposing it to terminate the process.
    /// </summary>
    Task<IStreamingProcess> StartStreamingAsync(ProcessRequest request, CancellationToken ct = default);
}

public sealed record ProcessRequest(
    string Executable,
    IReadOnlyList<string> Arguments,
    string? WorkingDirectory = null,
    IReadOnlyDictionary<string, string>? Environment = null,
    TimeSpan? Timeout = null);

public sealed record ProcessResult(int ExitCode, string StdOut, string StdErr)
{
    public bool Ok => ExitCode == 0;
}

public interface IStreamingProcess : IAsyncDisposable
{
    StreamReader StdOut { get; }
    bool HasExited { get; }
    Task<int> WaitAsync(CancellationToken ct = default);
}

public sealed class DefaultProcessRunner : IProcessRunner
{
    public async Task<ProcessResult> RunAsync(ProcessRequest request, CancellationToken ct = default)
    {
        var psi = BuildPsi(request, redirect: true);
        using var p = new Process { StartInfo = psi };
        var stdOut = new StringBuilder();
        var stdErr = new StringBuilder();
        p.OutputDataReceived += (_, e) => { if (e.Data != null) stdOut.AppendLine(e.Data); };
        p.ErrorDataReceived += (_, e) => { if (e.Data != null) stdErr.AppendLine(e.Data); };

        if (!p.Start()) throw new InvalidOperationException($"failed to start: {request.Executable}");
        p.BeginOutputReadLine();
        p.BeginErrorReadLine();

        using var linked = CancellationTokenSource.CreateLinkedTokenSource(ct);
        if (request.Timeout is { } t) linked.CancelAfter(t);

        try
        {
            await p.WaitForExitAsync(linked.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            try { if (!p.HasExited) p.Kill(entireProcessTree: true); } catch { }
            throw;
        }

        return new ProcessResult(p.ExitCode, stdOut.ToString(), stdErr.ToString());
    }

    public Task<IStreamingProcess> StartStreamingAsync(ProcessRequest request, CancellationToken ct = default)
    {
        var psi = BuildPsi(request, redirect: true);
        var p = new Process { StartInfo = psi };
        if (!p.Start()) throw new InvalidOperationException($"failed to start: {request.Executable}");
        return Task.FromResult<IStreamingProcess>(new StreamingProcess(p));
    }

    private static ProcessStartInfo BuildPsi(ProcessRequest request, bool redirect)
    {
        var psi = new ProcessStartInfo
        {
            FileName = request.Executable,
            CreateNoWindow = true,
            UseShellExecute = false,
            RedirectStandardOutput = redirect,
            RedirectStandardError = redirect,
            RedirectStandardInput = redirect,
            WorkingDirectory = request.WorkingDirectory ?? "",
        };
        foreach (var arg in request.Arguments) psi.ArgumentList.Add(arg);
        if (request.Environment is { } env)
            foreach (var (k, v) in env) psi.Environment[k] = v;
        return psi;
    }

    private sealed class StreamingProcess : IStreamingProcess
    {
        private readonly Process _p;
        public StreamingProcess(Process p) { _p = p; StdOut = p.StandardOutput; }
        public StreamReader StdOut { get; }
        public bool HasExited => _p.HasExited;
        public Task<int> WaitAsync(CancellationToken ct = default)
        {
            var tcs = new TaskCompletionSource<int>();
            _p.EnableRaisingEvents = true;
            _p.Exited += (_, _) => tcs.TrySetResult(_p.ExitCode);
            ct.Register(() => { try { if (!_p.HasExited) _p.Kill(entireProcessTree: true); } catch { } });
            if (_p.HasExited) tcs.TrySetResult(_p.ExitCode);
            return tcs.Task;
        }
        public async ValueTask DisposeAsync()
        {
            try { if (!_p.HasExited) _p.Kill(entireProcessTree: true); } catch { }
            await Task.Yield();
            _p.Dispose();
        }
    }
}
