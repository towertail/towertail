using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class NodeStoreTests
{
    [Fact]
    public void AddUpdateRemovePersists()
    {
        var path = Path.GetTempFileName();
        try
        {
            var store = new NodeStore(path);
            var n = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h" };
            store.Add(n);

            var reopened = new NodeStore(path);
            reopened.Nodes.Should().Contain(x => x.Id == n.Id && x.DisplayName == "db");

            reopened.SetFavorite(n.Id, true);
            var third = new NodeStore(path);
            third.ById(n.Id)!.Favorite.Should().BeTrue();

            third.Remove(n.Id);
            var final = new NodeStore(path);
            final.Nodes.Should().NotContain(x => x.Id == n.Id);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void CloudKindAndLinkRoundTrip()
    {
        var path = Path.GetTempFileName();
        try
        {
            var store = new NodeStore(path);
            var only = Node.CloudOnly(Guid.NewGuid(), "cloud-only");
            var enrolled = new DateTime(2026, 5, 1, 10, 0, 0, DateTimeKind.Utc);
            var linked = new Node { DisplayName = "db", Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", Cloud = new CloudLink(enrolled, "auto", "0.2.0+abc") };
            store.Add(only);
            store.Add(linked);

            var text = File.ReadAllText(path);
            text.Should().Contain("\"kind\": \"cloud\"");

            var reopened = new NodeStore(path);
            var a = reopened.ById(only.Id)!;
            a.Kind.Should().Be(NodeKind.Cloud);
            a.IsCloud.Should().BeTrue();
            a.Cloud.Should().BeNull();
            var b = reopened.ById(linked.Id)!;
            b.Kind.Should().Be(NodeKind.Ssh);
            b.IsCloud.Should().BeTrue();
            b.Cloud.Should().Be(new CloudLink(enrolled, "auto", "0.2.0+abc"));
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void NodeWithoutCloudLinkWritesNoCloudKey()
    {
        var path = Path.GetTempFileName();
        try
        {
            new NodeStore(path).Add(new Node { DisplayName = "db", Kind = NodeKind.Ssh });
            File.ReadAllText(path).Should().NotContain("\"cloud\"");
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void ReadsLegacyCustomThresholdsAsOverrides()
    {
        var path = Path.GetTempFileName();
        try
        {
            File.WriteAllText(path, """
            {"nodes":[{"id":"6f1c0d1e-0000-4000-8000-000000000001","displayName":"n","kind":"ssh",
              "customThresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.55,"memCritical":0.65,"diskWarn":0.7,"diskCritical":0.8}}]}
            """);
            var node = new NodeStore(path).Nodes[0];
            node.ThresholdOverrides.Should().Be(new ThresholdOverrides(
                Cpu: new ThresholdPair(0.5, 0.6), Mem: new ThresholdPair(0.55, 0.65), Disk: new ThresholdPair(0.7, 0.8)));

            new NodeStore(path).Update(node);
            var text = File.ReadAllText(path);
            text.Should().Contain("\"thresholdOverrides\"").And.NotContain("customThresholds");
            new NodeStore(path).Nodes[0].ThresholdOverrides.Should().Be(node.ThresholdOverrides);
        }
        finally { File.Delete(path); }
    }
}
