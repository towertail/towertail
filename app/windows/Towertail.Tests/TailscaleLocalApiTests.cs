using FluentAssertions;
using System.Text.Json;
using Towertail.WinUI.SystemServices;
using Xunit;

namespace Towertail.Tests;

public sealed class TailscaleLocalApiTests
{
    [Fact]
    public void DeserializesGoldenStatusFixture()
    {
        var json = File.ReadAllText(Path.Combine("TestData", "tailscale-status.json"));
        var status = JsonSerializer.Deserialize<TailscaleLocalApi.StatusResponse>(json);
        status!.Self!.HostName.Should().Be("mbp");
        status.Peer.Should().ContainKey("peer1");
        status.Peer!["peer1"].DnsName.Should().Be("db-primary.tail1234.ts.net");
    }
}
