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
}
