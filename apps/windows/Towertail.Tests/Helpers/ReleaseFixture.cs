using Xunit;

namespace Towertail.Tests.Helpers;

/// <summary>
/// The sample fixture of the pinned sampler release. <c>scripts/fetch-samplers.sh</c>
/// puts it in <c>dist/samplers/sample-v1.json</c>.
/// </summary>
internal static class ReleaseFixture
{
    public static string ReadSampleV1()
    {
        for (var dir = new DirectoryInfo(AppContext.BaseDirectory); dir != null; dir = dir.Parent)
        {
            var path = Path.Combine(dir.FullName, "dist", "samplers", "sample-v1.json");
            if (File.Exists(path)) return File.ReadAllText(path);
        }
        Assert.Skip("missing dist/samplers/sample-v1.json; run scripts/fetch-samplers.sh");
        return "";
    }
}
