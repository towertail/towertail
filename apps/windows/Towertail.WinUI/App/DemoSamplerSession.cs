#if DEBUG
using Towertail.WinUI.Collectors;

namespace Towertail.WinUI.Bootstrap;

/// <summary>A fake SSH host for renders: it answers the install commands after a short wait.</summary>
internal sealed class DemoSamplerSession : ISamplerSession
{
    public static string? Binary(string triple)
    {
        var path = Path.Combine(Path.GetTempPath(), "towertail-demo-sampler");
        if (!File.Exists(path)) File.WriteAllText(path, "demo");
        return path;
    }

    public async Task<(int Code, string Out, string Err)> RunAsync(string command, CancellationToken ct = default)
    {
        await Task.Delay(1500, ct);
        return command switch
        {
            _ when command.StartsWith("uname") => (0, "Linux x86_64\n", ""),
            _ when command.StartsWith("echo $HOME") => (0, "/home/demo\n", ""),
            _ when command.Contains("boot enable") => (0, """{"reboot":"systemd"}""", ""),
            _ when command.Contains("--once") => (1, "", ""),
            _ => (0, "ok\n", ""),
        };
    }

    public async Task PutAsync(Stream data, string remotePath, CancellationToken ct = default) => await Task.Delay(1500, ct);

    public ValueTask DisposeAsync() => ValueTask.CompletedTask;
}
#endif
