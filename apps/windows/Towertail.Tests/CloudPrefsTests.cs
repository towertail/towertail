using FluentAssertions;
using Towertail.WinUI.Cloud;
using Xunit;

namespace Towertail.Tests;

public sealed class CloudPrefsTests : IDisposable
{
    private readonly string _path = Path.Combine(Path.GetTempPath(), $"tt-cloud-{Guid.NewGuid():N}.json");

    public void Dispose()
    {
        if (File.Exists(_path)) File.Delete(_path);
    }

    [Fact]
    public void DefaultsWithoutFile()
    {
        var prefs = new CloudPrefs(_path);
        prefs.Endpoint.Should().Be(new Uri("https://api.towertail.com"));
        prefs.Email.Should().BeNull();
        prefs.ReleasedHosts.Should().BeEmpty();
        prefs.MoveOffered.Should().BeFalse();
    }

    [Fact]
    public void RoundTrip()
    {
        var id = Guid.NewGuid();
        var prefs = new CloudPrefs(_path)
        {
            Endpoint = new Uri("http://127.0.0.1:8080"),
            Email = "a@b.co",
            ReleasedHosts = new HashSet<Guid> { id },
            MoveOffered = true,
        };
        prefs.Save();

        var again = new CloudPrefs(_path);
        again.Endpoint.Should().Be(new Uri("http://127.0.0.1:8080"));
        again.Email.Should().Be("a@b.co");
        again.ReleasedHosts.Should().BeEquivalentTo([id]);
        again.MoveOffered.Should().BeTrue();
        File.ReadAllText(_path).Should().Contain("\"releasedHosts\"").And.Contain("\"moveOffered\"");
    }

    [Fact]
    public void BadFileGivesDefaults()
    {
        File.WriteAllText(_path, "{ not json");
        var prefs = new CloudPrefs(_path);
        prefs.Endpoint.Should().Be(new Uri("https://api.towertail.com"));
        prefs.ReleasedHosts.Should().BeEmpty();
    }

    [Fact]
    public void BadEndpointKeepsDefault()
    {
        File.WriteAllText(_path, """{"endpoint":"not a url","email":"a@b.co"}""");
        var prefs = new CloudPrefs(_path);
        prefs.Endpoint.Should().Be(new Uri("https://api.towertail.com"));
        prefs.Email.Should().Be("a@b.co");
    }

    [Fact]
    public void ReleasedHostsAddRemoveRetain()
    {
        var a = Guid.NewGuid();
        var b = Guid.NewGuid();
        var released = new ReleasedHosts(new CloudPrefs(_path));
        released.AddRange([a, b]);
        released.Contains(a).Should().BeTrue();

        released.Remove(a);
        new ReleasedHosts(new CloudPrefs(_path)).Ids.Should().BeEquivalentTo([b]);

        released.Retain(new HashSet<Guid>());
        new CloudPrefs(_path).ReleasedHosts.Should().BeEmpty();
    }
}
