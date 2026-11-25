using Renci.SshNet;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>The commands that <see cref="SamplerLifecycle"/> needs on a host. Tests use a fake.</summary>
public interface ISamplerSession : IAsyncDisposable
{
    /// <summary>Runs a shell command. A failed command gives a non-zero code, not an exception.</summary>
    Task<(int Code, string Out, string Err)> RunAsync(string command, CancellationToken ct = default);

    /// <summary>Writes a file. An existing file keeps its mode.</summary>
    Task PutAsync(Stream data, string remotePath, CancellationToken ct = default);
}

/// <summary>An SSH session with the host-key prompt of the app. It connects at the first call.</summary>
public sealed class SshSamplerSession(Node node, HostKeyPrompt? hostKeyPrompt = null, Action<Guid, string>? onTrust = null)
    : ISamplerSession
{
    private SshClient? _ssh;
    private SftpClient? _sftp;

    public Task<(int Code, string Out, string Err)> RunAsync(string command, CancellationToken ct = default)
        => Task.Run(() =>
        {
            ct.ThrowIfCancellationRequested();
            _ssh ??= SshConnectionFactory.Connect(node, hostKeyPrompt, Trust);
            var cmd = _ssh.CreateCommand(command);
            cmd.Execute();
            return (cmd.ExitStatus ?? -1, cmd.Result, cmd.Error);
        }, ct);

    public Task PutAsync(Stream data, string remotePath, CancellationToken ct = default)
        => Task.Run(() =>
        {
            ct.ThrowIfCancellationRequested();
            _sftp ??= SshConnectionFactory.ConnectSftp(node, hostKeyPrompt, Trust);
            _sftp.UploadFile(data, remotePath, canOverride: true);
        }, ct);

    private Action<string>? Trust => onTrust is null ? null : fp => onTrust(node.Id, fp);

    public ValueTask DisposeAsync()
    {
        _ssh?.Dispose();
        _sftp?.Dispose();
        return ValueTask.CompletedTask;
    }
}
