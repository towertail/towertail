using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests.Helpers;

/// <summary>
/// The golden scan streams of the pinned sampler release:
/// <c>dist/samplers/scan-{linux,darwin,windows}.ndjson</c>.
/// </summary>
internal static class ScanFixture
{
    public static string[] Lines(string os)
    {
        var name = $"scan-{os}.ndjson";
        for (var dir = new DirectoryInfo(AppContext.BaseDirectory); dir != null; dir = dir.Parent)
        {
            var path = Path.Combine(dir.FullName, "dist", "samplers", name);
            if (File.Exists(path))
                return File.ReadAllLines(path).Where(l => !string.IsNullOrWhiteSpace(l)).ToArray();
        }
        Assert.Skip($"missing dist/samplers/{name}; run scripts/fetch-samplers.sh");
        return Array.Empty<string>();
    }

    /// <summary>The fetched sampler for this machine, or a skip.</summary>
    public static string HostSampler()
    {
        var os = OperatingSystem.IsWindows() ? "windows" : OperatingSystem.IsMacOS() ? "darwin" : "linux";
        var arch = System.Runtime.InteropServices.RuntimeInformation.OSArchitecture switch
        {
            System.Runtime.InteropServices.Architecture.Arm64 => "arm64",
            _ => "amd64",
        };
        var file = OperatingSystem.IsWindows() ? "towertail-sampler.exe" : "towertail-sampler";
        for (var dir = new DirectoryInfo(AppContext.BaseDirectory); dir != null; dir = dir.Parent)
        {
            var path = Path.Combine(dir.FullName, "dist", "samplers", $"{os}-{arch}", file);
            if (File.Exists(path)) return path;
        }
        Assert.Skip($"missing dist/samplers/{os}-{arch}/{file}; run scripts/fetch-samplers.sh");
        return "";
    }

    public static DiskScanResult Result(string os) => DiskScanCodec.DecodeResult(Lines(os)[^1]);
}
