using System.Runtime.CompilerServices;
using System.Text.RegularExpressions;
using Renci.SshNet;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Runs <c>~/.towertail/towertail-sampler scan …</c> on an SSH exec channel and streams stdout.
/// First it makes sure that the host has the bundled sampler version. There is no short
/// timeout: a watchdog fails the scan when no line comes for 60 s. Cancel closes stdin,
/// and the channel closes when the stream ends.
/// </summary>
public sealed partial class SshDiskScanner : IDiskScanner
{
    public static readonly TimeSpan DefaultWatchdog = TimeSpan.FromSeconds(60);

    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;
    private readonly SamplerUpdateCoordinator? _updater;
    private readonly string _remotePath;
    private readonly TimeSpan _watchdog;

    public SshDiskScanner(
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null,
        SamplerUpdateCoordinator? updater = null,
        string? remotePath = null,
        TimeSpan? watchdog = null)
    {
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
        _updater = updater;
        _remotePath = remotePath ?? "~/.towertail/towertail-sampler";
        _watchdog = watchdog ?? DefaultWatchdog;
    }

    public async IAsyncEnumerable<DiskScanEvent> ScanAsync(
        Node node, DiskScanRequest request, [EnumeratorCancellation] CancellationToken ct = default)
    {
        SshScanChannel? channel = null;
        string? error = null;
        try
        {
            if (_updater != null)
            {
                var reported = await ReportedSamplerAsync(node, ct).ConfigureAwait(false);
                await _updater.EnsureCurrentAsync(node, reported, ct).ConfigureAwait(false);
            }
            var command = CommandLine(_remotePath, request);
            channel = await Task.Run(() => SshScanChannel.Start(Connect(node), command), ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { error = null; }
        catch (Exception ex) { error = ex.Message; }

        if (channel is null)
        {
            yield return error is null
                ? new DiskScanEvent.Cancelled()
                : new DiskScanEvent.Error($"Could not start the scan on {node.UserAtHost}: {error}");
            yield break;
        }

        await using (channel)
        {
            await foreach (var ev in new DiskScanStream(channel, _watchdog).ReadAsync(ct).ConfigureAwait(false))
                yield return ev;
        }
    }

    /// <summary>The shell command. Each argument is quoted for a POSIX shell.</summary>
    public static string CommandLine(string remotePath, DiskScanRequest request)
        => remotePath + " " + string.Join(" ", request.Arguments().Select(ShellQuote));

    public static string ShellQuote(string s)
        => s.Length > 0 && SafeShellWord().IsMatch(s) ? s : "'" + s.Replace("'", "'\\''") + "'";

    [GeneratedRegex(@"^[A-Za-z0-9._/@%+=:,-]+$")]
    private static partial Regex SafeShellWord();

    private Task<string> ReportedSamplerAsync(Node node, CancellationToken ct)
        => Task.Run(() =>
        {
            using var client = Connect(node);
            var output = client.RunCommand($"{_remotePath} --version");
            if (output.ExitStatus != 0) return "";
            // "towertail-sampler 0.3.0+abc" → "0.3.0+abc", the host.sampler form.
            var text = output.Result.Trim();
            var space = text.LastIndexOf(' ');
            return space >= 0 ? text[(space + 1)..] : text;
        }, ct);

    private SshClient Connect(Node node) => SshConnectionFactory.Connect(
        node, _hostKeyPrompt, _onTrust is null ? null : fp => _onTrust(node.Id, fp));

    private sealed class SshScanChannel : IScanChannel
    {
        private readonly SshClient _client;
        private readonly SshCommand _command;
        private readonly Task _execute;
        private readonly Stream _stdin;
        private readonly StreamReader _stdout;

        private SshScanChannel(SshClient client, SshCommand command, Task execute, Stream stdin)
        {
            _client = client;
            _command = command;
            _execute = execute;
            _stdin = stdin;
            _stdout = new StreamReader(command.OutputStream);
        }

        public static SshScanChannel Start(SshClient client, string commandText)
        {
            try
            {
                var command = client.CreateCommand(commandText);
                var execute = command.ExecuteAsync(CancellationToken.None);
                _ = execute.ContinueWith(t => _ = t.Exception, TaskContinuationOptions.OnlyOnFaulted);
                // Keep stdin open: the sampler stops on stdin EOF.
                var stdin = command.CreateInputStream();
                return new SshScanChannel(client, command, execute, stdin);
            }
            catch
            {
                client.Dispose();
                throw;
            }
        }

        public Task<string?> ReadLineAsync() => _stdout.ReadLineAsync();

        public void Stop()
        {
            try { _stdin.Dispose(); } catch { }
        }

        public async Task<string> ExitInfoAsync()
        {
            await Task.WhenAny(_execute, Task.Delay(TimeSpan.FromSeconds(2))).ConfigureAwait(false);
            if (!_execute.IsCompleted) return "no exit code";
            var err = (_command.Error ?? "").Trim();
            var code = _command.ExitStatus is int s ? $"exit {s}" : "no exit code";
            return string.IsNullOrEmpty(err) ? code : $"{code}: {err}";
        }

        public async ValueTask DisposeAsync()
        {
            Stop();
            try { if (!_execute.IsCompleted) _command.CancelAsync(forceKill: false, millisecondsTimeout: 1000); } catch { }
            await Task.WhenAny(_execute, Task.Delay(TimeSpan.FromSeconds(2))).ConfigureAwait(false);
            try { _command.Dispose(); } catch { }
            try { _client.Dispose(); } catch { }
        }
    }
}
