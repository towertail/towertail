using System.Text.Json;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;

namespace Towertail.Tests.Helpers;

/// <summary>A scripted host. The handler answers each command. The session records all calls.</summary>
internal sealed class FakeSamplerSession : ISamplerSession
{
    public const string Home = "/home/u";

    public List<string> Commands { get; } = [];
    public List<(string Path, byte[] Data)> Puts { get; } = [];
    public Func<string, (int Code, string Out, string Err)> Handler { get; set; } = Default;
    public Func<string, Task>? Before { get; set; }
    public Action? OnDispose { get; set; }
    public bool Disposed { get; private set; }

    public static (int, string, string) Default(string command)
    {
        if (command.StartsWith("uname")) return (0, "Linux aarch64\n", "");
        if (command == "echo $HOME") return (0, Home + "\n", "");
        if (command.EndsWith("--once")) return (0, Once("m-1"), "");
        if (command.EndsWith("boot enable --mode auto")) return (0, """{"reboot":"systemd"}""", "");
        return (0, "", "");
    }

    public static string Once(string machineId)
    {
        var sample = new Sample(
            1, DateTime.UtcNow,
            new HostInfo("h", "linux", "arm64", "6", 0, "s", machineId),
            new CpuInfo(1, 0, 0, 0, 1), new MemInfo(1, 2), new MemInfo(0, 0),
            [], null, null, null, null, []);
        return JsonSerializer.Serialize(sample, SampleCodec.Options);
    }

    public async Task<(int Code, string Out, string Err)> RunAsync(string command, CancellationToken ct = default)
    {
        lock (Commands) Commands.Add(command);
        if (Before is not null) await Before(command);
        return Handler(command);
    }

    public Task PutAsync(Stream data, string remotePath, CancellationToken ct = default)
    {
        using var copy = new MemoryStream();
        data.CopyTo(copy);
        lock (Puts) Puts.Add((remotePath, copy.ToArray()));
        return Task.CompletedTask;
    }

    public ValueTask DisposeAsync()
    {
        Disposed = true;
        OnDispose?.Invoke();
        return ValueTask.CompletedTask;
    }
}
