using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Outcome of an attempted kill, surfaced to the UI as either a success
/// toast or an error dialog. Mirrors the Mac side's <c>KillResult</c>.
/// </summary>
public abstract record KillResult
{
    public sealed record Success(int Pid, string Name) : KillResult;
    public sealed record Failure(string Message) : KillResult;
}

/// <summary>
/// Dispatches a hard kill against a PID either locally (Windows
/// <c>taskkill /F /PID</c>) or over SSH (<c>kill -9</c>). Local Windows
/// hosts use taskkill because POSIX <c>kill</c> isn't on PATH out of the
/// box; SSH targets are assumed to be POSIX (the sampler ships for Linux
/// and macOS).
///
/// Connection setup and host-key validation reuse <see cref="SshConnectionFactory"/>
/// so the kill flow honors the same pinned fingerprint and password
/// store as the sampler invoker.
/// </summary>
public static class ProcessKiller
{
    public static Task<KillResult> KillAsync(int pid, Node node, CancellationToken ct = default)
        => node.Kind switch
        {
            NodeKind.Local => KillLocalAsync(pid, ct),
            NodeKind.Ssh => KillSshAsync(pid, node, ct),
            _ => Task.FromResult<KillResult>(new KillResult.Failure($"Unsupported node kind: {node.Kind}")),
        };

    /// <summary>
    /// Shell preview shown in the confirmation dialog. Matches the actual
    /// command the kill path will run so the user sees exactly what's
    /// about to happen.
    /// </summary>
    public static string CommandPreview(int pid, Node? node)
    {
        if (node is null) return $"taskkill /F /PID {pid}";
        return node.Kind switch
        {
            NodeKind.Local => $"taskkill /F /PID {pid}",
            NodeKind.Ssh => $"ssh {node.SshUser ?? "?"}@{node.SshHost ?? "?"} 'kill -9 {pid}'",
            _ => $"kill -9 {pid}",
        };
    }

    private static async Task<KillResult> KillLocalAsync(int pid, CancellationToken ct)
    {
        try
        {
            var runner = new DefaultProcessRunner();
            var result = await runner.RunAsync(new ProcessRequest(
                Executable: "taskkill",
                Arguments: new[] { "/F", "/PID", pid.ToString() },
                Timeout: TimeSpan.FromSeconds(10)
            ), ct).ConfigureAwait(false);
            if (result.Ok) return new KillResult.Success(pid, "");
            var err = result.StdErr.Trim();
            if (string.IsNullOrEmpty(err)) err = result.StdOut.Trim();
            return new KillResult.Failure(string.IsNullOrEmpty(err)
                ? $"taskkill exited {result.ExitCode}"
                : $"taskkill exited {result.ExitCode}: {err}");
        }
        catch (Exception ex)
        {
            return new KillResult.Failure(ex.Message);
        }
    }

    private static Task<KillResult> KillSshAsync(int pid, Node node, CancellationToken ct)
        => Task.Run<KillResult>(() =>
        {
            try
            {
                using var client = SshConnectionFactory.Connect(node);
                var output = client.RunCommand($"kill -9 {pid}");
                if (output.ExitStatus == 0) return new KillResult.Success(pid, "");
                var err = output.Error.Trim();
                return new KillResult.Failure(string.IsNullOrEmpty(err)
                    ? $"ssh kill exited {output.ExitStatus}"
                    : $"ssh kill exited {output.ExitStatus}: {err}");
            }
            catch (Exception ex)
            {
                return new KillResult.Failure(ex.Message);
            }
        }, ct);
}
