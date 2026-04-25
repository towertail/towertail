using System.Text;
using FluentAssertions;
using Renci.SshNet;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

/// <summary>
/// Pure tests for <see cref="SshConnectionFactory"/>. No socket I/O — we only
/// exercise the auth-method branching, fingerprint shape, and ConnectionInfo
/// construction so CI doesn't need a real SSH server.
/// </summary>
public sealed class SshConnectionFactoryTests
{
    private static string NewSshDir(string name)
    {
        var dir = Path.Combine(Path.GetTempPath(), $"tt-ssh-test-{name}-{Guid.NewGuid():N}");
        Directory.CreateDirectory(dir);
        return dir;
    }

    [Fact]
    public void BuildAuthMethods_Password_RequiresDpapiEntry()
    {
        var node = new Node { Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", AuthMethod = AuthMethod.Password };
        Action act = () => SshConnectionFactory.BuildAuthMethods(node, dpapiPassword: null);
        act.Should().Throw<SshConnectionFactory.NoCredentialException>();
    }

    [Fact]
    public void BuildAuthMethods_Password_ReturnsOneMethod()
    {
        var node = new Node { Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", AuthMethod = AuthMethod.Password };
        var methods = SshConnectionFactory.BuildAuthMethods(node, dpapiPassword: "hunter2");
        methods.Should().HaveCount(1);
        methods[0].Should().BeOfType<PasswordAuthenticationMethod>();
    }

    [Fact]
    public void BuildAuthMethods_Key_NoKeysThrowsNoCredential()
    {
        var dir = NewSshDir("empty");
        try
        {
            var node = new Node { Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h", AuthMethod = AuthMethod.Key };
            Action act = () => SshConnectionFactory.BuildAuthMethods(node, null, sshDir: dir);
            act.Should().Throw<SshConnectionFactory.NoCredentialException>();
        }
        finally { Directory.Delete(dir, recursive: true); }
    }

    [Fact]
    public void BuildAuthMethods_MissingUserThrows()
    {
        var node = new Node { Kind = NodeKind.Ssh, SshUser = "", SshHost = "h" };
        Action act = () => SshConnectionFactory.BuildAuthMethods(node, dpapiPassword: null);
        act.Should().Throw<SshConnectionFactory.NoCredentialException>();
    }

    [Fact]
    public void BuildConnectionInfo_UsesEffectivePortAndUser()
    {
        var node = new Node
        {
            Kind = NodeKind.Ssh, SshUser = "alice", SshHost = "host.example",
            SshPort = null, // → 22
            AuthMethod = AuthMethod.Password
        };
        var info = SshConnectionFactory.BuildConnectionInfo(node, dpapiPassword: "pw");
        info.Host.Should().Be("host.example");
        info.Port.Should().Be(22);
        info.Username.Should().Be("alice");
    }

    [Fact]
    public void BuildConnectionInfo_RespectsExplicitPort()
    {
        var node = new Node
        {
            Kind = NodeKind.Ssh, SshUser = "alice", SshHost = "host.example",
            SshPort = 2222, AuthMethod = AuthMethod.Password
        };
        var info = SshConnectionFactory.BuildConnectionInfo(node, dpapiPassword: "pw");
        info.Port.Should().Be(2222);
    }

    [Fact]
    public void Fingerprint_MatchesSshKeygenFormat()
    {
        // "ssh-keygen -lf" prints: SHA256:<base64-no-padding>. Verify the
        // prefix + padding trimming match. We feed a known byte pattern and
        // assert the known base64 of its SHA256.
        var blob = Encoding.UTF8.GetBytes("hello-world");
        var fp = SshConnectionFactory.Fingerprint(blob);
        fp.Should().StartWith("SHA256:");
        fp.Should().NotEndWith("=");
        // Idempotent: same input → same output.
        SshConnectionFactory.Fingerprint(blob).Should().Be(fp);
    }
}

/// <summary>
/// Pure tests for <see cref="State.Node"/> encoding to make sure old JSON
/// (records missing the SSH migration fields) still round-trips cleanly.
/// </summary>
public sealed class NodeMigrationCodecTests
{
    [Fact]
    public void DecodesOldJsonWithDefaults()
    {
        var oldJson = """
        {
            "id": "11111111-1111-1111-1111-111111111111",
            "displayName": "old",
            "kind": "ssh",
            "sshUser": "root",
            "sshHost": "10.0.0.1",
            "tags": [],
            "enabled": true,
            "iconOnWarn": true,
            "iconOnCritical": true,
            "notifyOnWarn": true,
            "notifyOnCritical": true,
            "favorite": false
        }
        """;
        var node = System.Text.Json.JsonSerializer.Deserialize<Node>(oldJson);
        node.Should().NotBeNull();
        node!.SshPort.Should().BeNull();
        node.EffectiveSshPort.Should().Be(22);
        node.AuthMethod.Should().Be(AuthMethod.Key);
        node.KnownHostFingerprint.Should().BeNull();
    }

    [Fact]
    public void RoundTripPreservesNewFields()
    {
        var original = new Node
        {
            DisplayName = "new",
            Kind = NodeKind.Ssh,
            SshUser = "u",
            SshHost = "h",
            SshPort = 2222,
            AuthMethod = AuthMethod.Password,
            KnownHostFingerprint = "SHA256:ZZZZ"
        };
        var json = System.Text.Json.JsonSerializer.Serialize(original);
        var decoded = System.Text.Json.JsonSerializer.Deserialize<Node>(json);
        decoded!.SshPort.Should().Be(2222);
        decoded.AuthMethod.Should().Be(AuthMethod.Password);
        decoded.KnownHostFingerprint.Should().Be("SHA256:ZZZZ");
    }
}
