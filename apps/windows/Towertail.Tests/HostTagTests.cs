using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class HostTagTests
{
    private static readonly Node Local = Node.LocalWindows("PC");
    private static readonly Node Ssh = new() { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "ops", SshHost = "db" };
    private static readonly Node Linked = Ssh with { Id = Guid.NewGuid(), Cloud = new CloudLink(DateTime.UtcNow) };

    private static HostTag? Tag(Node node, bool signedIn = true, bool connected = true, bool alerted = false)
        => HostTags.For(node, signedIn, connected, alerted);

    [Fact]
    public void LocalHostIsLocalSignedInOrNot()
    {
        Tag(Local).Should().Be(HostTag.Local);
        Tag(Local, signedIn: false).Should().Be(HostTag.Local);
        HostTag.Local.Label().Should().Be("LOCAL");
    }

    [Fact]
    public void CloudHostIsCloud()
    {
        Tag(Linked).Should().Be(HostTag.Cloud);
        Tag(Node.CloudOnly(Guid.NewGuid(), "web")).Should().Be(HostTag.Cloud);
        HostTag.Cloud.Label().Should().Be("CLOUD");
    }

    [Fact]
    public void SshHostWithNoLinkIsSshOnlyWhenSignedIn()
    {
        Tag(Ssh).Should().Be(HostTag.SshOnly);
        HostTag.SshOnly.Label().Should().Be("SSH ONLY");
    }

    [Fact]
    public void SshHostWithNoLinkHasNoTagWhenSignedOut()
        => Tag(Ssh, signedIn: false).Should().BeNull();

    [Fact]
    public void LinkedSshHostIsCloudNotSshOnly()
    {
        Tag(Linked).Should().Be(HostTag.Cloud);
        Tag(Linked, connected: false).Should().Be(HostTag.CloudOffline);
    }

    [Fact]
    public void CloudHostWithTheFeedDownIsCloudOffline()
    {
        Tag(Linked, connected: false).Should().Be(HostTag.CloudOffline);
        HostTag.CloudOffline.Label().Should().Be("CLOUD OFFLINE");
    }

    [Fact]
    public void AlertedCloudHostIsOfflineAlertedAlsoWithTheFeedDown()
    {
        Tag(Linked, alerted: true).Should().Be(HostTag.OfflineAlerted);
        Tag(Linked, connected: false, alerted: true).Should().Be(HostTag.OfflineAlerted);
        HostTag.OfflineAlerted.Label().Should().Be("OFFLINE · ALERTED");
    }

    [Fact]
    public void AlertedFlagDoesNotTagALocalOrSshHost()
    {
        Tag(Local, alerted: true).Should().Be(HostTag.Local);
        Tag(Ssh, alerted: true).Should().Be(HostTag.SshOnly);
    }
}
