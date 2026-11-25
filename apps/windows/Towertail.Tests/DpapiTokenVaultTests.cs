using FluentAssertions;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class DpapiTokenVaultTests : IDisposable
{
    private readonly string _path = Path.Combine(Path.GetTempPath(), $"tt-vault-{Guid.NewGuid():N}.json");

    public void Dispose()
    {
        if (File.Exists(_path)) File.Delete(_path);
    }

    [Fact]
    public void SetGetDeleteRoundTrip()
    {
        Assert.SkipUnless(OperatingSystem.IsWindows(), "DPAPI needs Windows.");
        var vault = new DpapiTokenVault(_path);
        vault.Get("api.towertail.com").Should().BeNull();

        vault.Set("api.towertail.com", "tok-secret");
        vault.Set("127.0.0.1:8080", "tok-two");

        vault.Get("api.towertail.com").Should().Be("tok-secret");
        new DpapiTokenVault(_path).Get("127.0.0.1:8080").Should().Be("tok-two");
        File.ReadAllText(_path).Should().NotContain("tok-secret");

        vault.Delete("api.towertail.com");
        vault.Get("api.towertail.com").Should().BeNull();
        vault.Get("127.0.0.1:8080").Should().Be("tok-two");
    }

    [Fact]
    public void BadCiphertextGivesNull()
    {
        Assert.SkipUnless(OperatingSystem.IsWindows(), "DPAPI needs Windows.");
        File.WriteAllText(_path, """{"entries":{"k":"AAAA"}}""");
        new DpapiTokenVault(_path).Get("k").Should().BeNull();
    }
}
