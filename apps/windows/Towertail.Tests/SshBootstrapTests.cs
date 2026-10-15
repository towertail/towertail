using FluentAssertions;
using Towertail.WinUI.Collectors;
using Xunit;

namespace Towertail.Tests;

public sealed class SshBootstrapTests
{
    [Theory]
    [InlineData("Linux x86_64", "linux-amd64")]
    [InlineData("Linux aarch64", "linux-arm64")]
    [InlineData("Linux armv7l", "linux-armv7")]
    [InlineData("Linux armv7", "linux-armv7")]
    [InlineData("Darwin arm64", "darwin-arm64")]
    [InlineData("Microsoft Windows [Version 10.0.22000.1]", "windows-amd64")]
    public void MapsUnameToTriple(string uname, string triple)
        => SshBootstrap.ParseUnameOrVer(uname).Triple.Should().Be(triple);

    [Theory]
    [InlineData("~/.towertail/towertail-sampler", "/home/u", "/home/u/.towertail/towertail-sampler")]
    [InlineData("~/.towertail/towertail-sampler", "/home/u/", "/home/u/.towertail/towertail-sampler")]
    [InlineData("%USERPROFILE%/.towertail/towertail-sampler.exe", "/C:/Users/u", "/C:/Users/u/.towertail/towertail-sampler.exe")]
    [InlineData("/opt/towertail-sampler", "/home/u", "/opt/towertail-sampler")]
    public void ResolvesHomeInRemotePath(string templated, string home, string want)
        => SshBootstrap.ResolveRemotePath(templated, home).Should().Be(want);
}
