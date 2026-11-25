using FluentAssertions;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class DiskScannerFactoryTests
{
    private static Node Ssh() => new() { DisplayName = "web", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };

    [Fact]
    public async Task CloudNodeUsesRemoteScanner()
    {
        await using var client = new RemoteClient(new Uri("http://127.0.0.1:1"), "t");
        var factory = new DiskScannerFactory(remoteFor: n => n.IsCloud ? client : null);
        var node = Node.CloudOnly(Guid.NewGuid(), "db");

        factory.Create(node).Should().BeOfType<RemoteDiskScanner>();
    }

    [Fact]
    public async Task LinkedSshNodeUsesRemoteScanner()
    {
        await using var client = new RemoteClient(new Uri("http://127.0.0.1:1"), "t");
        var factory = new DiskScannerFactory(remoteFor: n => n.IsCloud ? client : null);
        var node = Ssh() with { Cloud = new CloudLink(DateTime.UtcNow) };

        factory.Create(node).Should().BeOfType<RemoteDiskScanner>();
    }

    [Fact]
    public void SshNodeUsesSshScanner()
    {
        var factory = new DiskScannerFactory(remoteFor: _ => null);

        factory.Create(Ssh()).Should().BeOfType<SshDiskScanner>();
    }

    [Fact]
    public void SignedOutCloudClientFallsBackToSsh()
    {
        var factory = new DiskScannerFactory(remoteFor: _ => null);
        var node = Ssh() with { Cloud = new CloudLink(DateTime.UtcNow) };

        factory.Create(node).Should().BeOfType<SshDiskScanner>();
    }
}
