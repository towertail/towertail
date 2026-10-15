using System.Collections.Generic;
using System.IO;

namespace Towertail.Tests.Integration;

/// <summary>
/// Shared configuration for Tier 2 integration tests that talk to a real
/// <c>towertail-server</c> booted via <c>server/docker/docker-compose.test.yaml</c>.
/// Tests skip gracefully when the flag isn't set so the default
/// <c>dotnet test</c> path (no Docker) still passes.
///
/// Activation: <c>scripts/test.ps1 --remote-integration</c> writes
/// <c>%TEMP%\towertail-integration-test.env</c> before invoking the test
/// runner and also sets the matching env vars. Both the env vars and the
/// flag file are accepted (flag file wins if env is absent) — parity with
/// the Mac suite so CI is symmetric.
/// </summary>
public static class IntegrationConfig
{
    private static readonly Dictionary<string, string> _fromFile = Load();

    private static Dictionary<string, string> Load()
    {
        var path = Path.Combine(Path.GetTempPath(), "towertail-integration-test.env");
        var dict = new Dictionary<string, string>();
        if (!File.Exists(path)) return dict;
        foreach (var line in File.ReadAllLines(path))
        {
            var i = line.IndexOf('=');
            if (i < 0) continue;
            dict[line[..i]] = line[(i + 1)..];
        }
        return dict;
    }

    private static string? Get(string key)
        => Environment.GetEnvironmentVariable(key) ?? (_fromFile.TryGetValue(key, out var v) ? v : null);

    /// <summary>
    /// True when either the Docker/Compose mode flag (<c>TOWERTAIL_INTEGRATION=1</c>)
    /// or the no-Docker local-server mode flag (<c>TOWERTAIL_LOCAL_SERVER=1</c>)
    /// is set. Either mode gives tests a real server to talk to.
    /// </summary>
    public static bool Enabled =>
        Get("TOWERTAIL_INTEGRATION") == "1"
        || Get("TOWERTAIL_LOCAL_SERVER") == "1";

    public static Uri Endpoint
    {
        get
        {
            var s = Get("TOWERTAIL_ENDPOINT") ?? "http://127.0.0.1:18080";
            return new Uri(s);
        }
    }

    public static string AdminToken
        => Get("TOWERTAIL_ADMIN_TOKEN") ?? "tt-test-admin-token-0000000000000000";

    /// <summary>
    /// The sampler binary to spawn in push mode. Resolves in order:
    /// 1. <c>TOWERTAIL_SAMPLER_BINARY</c> explicit path.
    /// 2. Under <c>TOWERTAIL_REPO_ROOT/dist/samplers/&lt;host-triple&gt;/</c>.
    /// 3. Walk up from CWD and from this source file for 10/6 levels.
    /// Returns null if nothing found — test marks itself skipped.
    /// </summary>
    public static string? LocateSamplerBinary()
    {
        var triple = System.Runtime.InteropServices.RuntimeInformation.ProcessArchitecture switch
        {
            System.Runtime.InteropServices.Architecture.Arm64 => "windows-arm64",
            _ => "windows-amd64",
        };
        var exe = $"towertail-sampler.exe";

        var explicitPath = Get("TOWERTAIL_SAMPLER_BINARY");
        if (!string.IsNullOrWhiteSpace(explicitPath) && File.Exists(explicitPath)) return explicitPath;

        var roots = new List<string>();
        var envRoot = Get("TOWERTAIL_REPO_ROOT");
        if (!string.IsNullOrWhiteSpace(envRoot)) roots.Add(envRoot);

        var dir = Directory.GetCurrentDirectory();
        for (int i = 0; i < 10 && dir != null; i++)
        {
            roots.Add(dir);
            dir = Path.GetDirectoryName(dir);
        }

        foreach (var root in roots)
        {
            var p = Path.Combine(root, "dist", "samplers", triple, exe);
            if (File.Exists(p)) return p;
        }
        return null;
    }
}

/// <summary>
/// xUnit skip condition attribute: skips the fact when the integration
/// flag is off. Equivalent to the Swift side's <c>XCTSkipUnless</c>.
/// </summary>
public sealed class SkipWithoutIntegrationAttribute : Xunit.FactAttribute
{
    public SkipWithoutIntegrationAttribute()
    {
        if (!IntegrationConfig.Enabled)
        {
            Skip = "set TOWERTAIL_INTEGRATION=1 (compose) or TOWERTAIL_LOCAL_SERVER=1 (no-Docker) " +
                   "— use scripts/test.ps1 --remote-integration";
        }
    }
}
