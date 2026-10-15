using System.Security.Cryptography;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// For SSH nodes: on first connect (or when the bundled sampler is newer), rev the remote
/// binary via the <see cref="SshBootstrap"/> handshake. Mirrors SamplerUpdateCoordinator.swift.
/// </summary>
public sealed class SamplerUpdateCoordinator
{
    private readonly SamplerManifest? _manifest;
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;
    private readonly HashSet<Guid> _completed = new();
    private readonly object _lock = new();

    public SamplerUpdateCoordinator(
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null)
    {
        _manifest = SamplerManifest.LoadFromBundle();
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
    }

    public async Task<bool> EnsureDeployedAsync(Node node, CancellationToken ct = default)
    {
        if (node.Kind != NodeKind.Ssh) return true;

        lock (_lock) if (_completed.Contains(node.Id)) return true;

        var bootstrap = new SshBootstrap(node, _hostKeyPrompt, _onTrust);
        var triple = await bootstrap.DetectAsync(ct).ConfigureAwait(false);
        if (triple.Os == SshBootstrap.RemoteOs.Unknown) return false;

        var invoker = new SshSamplerInvoker(
            node,
            remotePath: triple.IsWindows
                ? "%USERPROFILE%/.towertail/towertail-sampler.exe"
                : "~/.towertail/towertail-sampler",
            hostKeyPrompt: _hostKeyPrompt,
            onTrust: _onTrust);

        string remoteVersion = "";
        try { remoteVersion = await invoker.VersionAsync(ct).ConfigureAwait(false); }
        catch { /* missing binary, treat as stale */ }

        var localVersion = _manifest?.Version ?? "0.0.0-dev";

        if (string.IsNullOrEmpty(remoteVersion) || !remoteVersion.Contains(localVersion))
        {
            var deployed = await bootstrap.DeployAsync(triple, ct: ct).ConfigureAwait(false);
            if (!deployed) return false;
            var verified = await bootstrap.VerifyAsync(triple, ct).ConfigureAwait(false);
            if (!verified) return false;
        }

        lock (_lock) _completed.Add(node.Id);
        return true;
    }

    public static string Sha256Of(string path)
    {
        using var s = File.OpenRead(path);
        var hash = SHA256.HashData(s);
        return Convert.ToHexString(hash).ToLowerInvariant();
    }
}
