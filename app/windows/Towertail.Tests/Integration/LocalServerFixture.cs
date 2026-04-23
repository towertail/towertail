using System.Diagnostics;
using System.Net.Http;
using System.Net.Sockets;
using Xunit;

namespace Towertail.Tests.Integration;

/// <summary>
/// Boots the real Go <c>towertail-server</c> binary as a child process
/// configured with <c>TT_CLICKHOUSE__DISABLED=true</c> so Tier 2 tests can
/// run without Docker. This is the no-mock path the Windows dev box wants:
/// real HTTP handlers, real WS hub, real sampler push — only persistence
/// is skipped (no ClickHouse, no BoltDB data survives across runs since
/// TT_STORAGE__PATH points at a unique temp dir).
///
/// Triggered when <c>TOWERTAIL_LOCAL_SERVER=1</c> is set (by
/// <c>scripts/test.ps1 --remote-integration</c> on machines without
/// Docker). When off, tests fall through to whatever <c>TOWERTAIL_ENDPOINT</c>
/// points at — typically the Docker Compose stack on CI or a preexisting
/// server.
/// </summary>
public sealed class LocalServerFixture : IAsyncLifetime
{
    public Uri Endpoint { get; private set; } = null!;
    public string AdminToken { get; private set; } = null!;

    private Process? _proc;
    private string? _storageDir;
    private string? _outLogPath;
    private string? _errLogPath;
    private bool _ownsProcess;

    public static bool RequestedByEnv =>
        Environment.GetEnvironmentVariable("TOWERTAIL_LOCAL_SERVER") == "1";

    public async ValueTask InitializeAsync()
    {
        // If the user already has a server reachable via IntegrationConfig
        // (Docker mode, or a long-running dev server), defer to it — no
        // child process is spawned. The fixture is a thin passthrough.
        if (!RequestedByEnv)
        {
            Endpoint = IntegrationConfig.Endpoint;
            AdminToken = IntegrationConfig.AdminToken;
            return;
        }

        AdminToken = "tt-test-admin-token-0000000000000000";
        var repoRoot = LocateRepoRoot();
        var port = PickFreeLoopbackPort();
        Endpoint = new Uri($"http://127.0.0.1:{port}");
        _ownsProcess = true;

        _storageDir = Path.Combine(Path.GetTempPath(), $"tt-server-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_storageDir);
        _outLogPath = Path.Combine(_storageDir, "server.stdout.log");
        _errLogPath = Path.Combine(_storageDir, "server.stderr.log");

        // Build the server on demand. `go run` is simpler than finding a
        // prebuilt binary and matches the dev ergonomics of running
        // `go test` in server/. Requires a working Go toolchain on PATH.
        var psi = new ProcessStartInfo
        {
            FileName = "go",
            WorkingDirectory = Path.Combine(repoRoot, "server"),
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        };
        psi.ArgumentList.Add("run");
        psi.ArgumentList.Add("./cmd/towertail-server");
        psi.ArgumentList.Add("serve");

        psi.Environment["TT_HTTP__ADDR"] = $":{port}";
        psi.Environment["TT_CLICKHOUSE__DISABLED"] = "true";
        psi.Environment["TT_STORAGE__PATH"] = _storageDir;
        psi.Environment["TT_AUTH__BOOTSTRAP_TOKEN"] = AdminToken;
        psi.Environment["TT_CLOUD__MANAGED"] = "false";
        psi.Environment["TT_LOG__FORMAT"] = "text";
        psi.Environment["TT_LOG__LEVEL"] = "info";

        _proc = Process.Start(psi) ?? throw new InvalidOperationException("failed to start towertail-server");

        // Drain stdout/stderr into rotating log files so we can diagnose failures.
        _ = DrainAsync(_proc.StandardOutput, _outLogPath);
        _ = DrainAsync(_proc.StandardError, _errLogPath);

        await WaitReadyAsync(TimeSpan.FromSeconds(60));
    }

    public async ValueTask DisposeAsync()
    {
        if (!_ownsProcess) return;
        if (_proc is not null && !_proc.HasExited)
        {
            try { _proc.Kill(entireProcessTree: true); } catch { }
            try { await _proc.WaitForExitAsync(); } catch { }
        }
        _proc?.Dispose();
        if (_storageDir != null && Directory.Exists(_storageDir))
        {
            try { Directory.Delete(_storageDir, recursive: true); } catch { }
        }
    }

    private async Task WaitReadyAsync(TimeSpan timeout)
    {
        using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(2) };
        var url = new Uri(Endpoint, "/readyz");
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            if (_proc is not null && _proc.HasExited)
            {
                throw new InvalidOperationException(
                    $"towertail-server exited unexpectedly (code {_proc.ExitCode}). " +
                    $"See {_errLogPath}");
            }
            try
            {
                using var resp = await http.GetAsync(url);
                if (resp.IsSuccessStatusCode) return;
            }
            catch { /* connection refused while booting is expected */ }
            await Task.Delay(500);
        }
        throw new TimeoutException($"server /readyz did not return 200 within {timeout}. See {_errLogPath}");
    }

    private static int PickFreeLoopbackPort()
    {
        using var listener = new TcpListener(System.Net.IPAddress.Loopback, 0);
        listener.Start();
        var port = ((System.Net.IPEndPoint)listener.LocalEndpoint).Port;
        listener.Stop();
        return port;
    }

    private static string LocateRepoRoot()
    {
        var explicitRoot = Environment.GetEnvironmentVariable("TOWERTAIL_REPO_ROOT");
        if (!string.IsNullOrWhiteSpace(explicitRoot) && Directory.Exists(explicitRoot))
        {
            return explicitRoot;
        }
        var dir = Directory.GetCurrentDirectory();
        for (int i = 0; i < 10 && dir != null; i++)
        {
            if (File.Exists(Path.Combine(dir, "server", "go.mod")))
            {
                return dir;
            }
            dir = Path.GetDirectoryName(dir);
        }
        throw new InvalidOperationException(
            "couldn't locate repo root (no server/go.mod found walking up from CWD); " +
            "set TOWERTAIL_REPO_ROOT.");
    }

    private static async Task DrainAsync(StreamReader reader, string path)
    {
        try
        {
            await using var sw = new StreamWriter(path, append: false);
            string? line;
            while ((line = await reader.ReadLineAsync()) != null)
            {
                await sw.WriteLineAsync(line);
                await sw.FlushAsync();
            }
        }
        catch { }
    }
}

/// <summary>
/// xUnit collection fixture so the server boots once for all
/// integration tests that opt into it, rather than per-test.
/// </summary>
[CollectionDefinition("LocalServer")]
public sealed class LocalServerCollection : ICollectionFixture<LocalServerFixture> { }
