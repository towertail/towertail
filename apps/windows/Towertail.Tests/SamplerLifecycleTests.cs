using System.Text;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Backend;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class SamplerLifecycleTests : IDisposable
{
    private readonly FakeSamplerSession _session = new();
    private readonly Enroller _enroller = new();
    private readonly string _binary = Path.Combine(Path.GetTempPath(), $"tt-bin-{Guid.NewGuid():N}");

    public SamplerLifecycleTests() => File.WriteAllBytes(_binary, Encoding.UTF8.GetBytes("binary-bytes"));

    public void Dispose()
    {
        if (File.Exists(_binary)) File.Delete(_binary);
    }

    private SamplerLifecycle Lifecycle => new(_session, _enroller);

    private sealed class Enroller : IEnroller
    {
        public EnrollRequest? Request { get; private set; }
        public Exception? Fail { get; set; }

        public Task<EnrollResponse> EnrollSamplerAsync(EnrollRequest req, CancellationToken ct = default)
        {
            Request = req;
            if (Fail is not null) throw Fail;
            return Task.FromResult(new EnrollResponse(req.NodeId ?? Guid.NewGuid(), "stk-secret", "https://cloud.example"));
        }
    }

    [Fact]
    public async Task DetectGivesTheTriple()
    {
        (await Lifecycle.DetectAsync()).Should().Be("linux-arm64");
    }

    [Fact]
    public async Task DetectReadsMacHosts()
    {
        _session.Handler = _ => (0, "Darwin arm64\n", "");
        (await Lifecycle.DetectAsync()).Should().Be("darwin-arm64");
    }

    [Theory]
    [InlineData(1, "")]
    [InlineData(0, "Windows_NT")]
    public async Task DetectRejectsOtherHosts(int code, string text)
    {
        _session.Handler = _ => (code, text, "");
        var act = () => Lifecycle.DetectAsync();
        (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which.Kind.Should().Be(LifecycleFailure.UnsupportedHost);
    }

    [Fact]
    public async Task InstallStopsCopiesAndMakesExecutable()
    {
        await Lifecycle.InstallAsync(_binary);

        _session.Commands.First().Should().Contain("stop");
        _session.Puts.Should().ContainSingle().Which.Path.Should().Be("/home/u/.towertail/towertail-sampler.gz");
        _session.Commands.Should().Contain("gunzip -f ~/.towertail/towertail-sampler.gz");
        _session.Commands.Last().Should().Be("chmod 755 ~/.towertail/towertail-sampler");
    }

    [Fact]
    public async Task InstallSendsTheRawFileWhenGunzipFails()
    {
        _session.Handler = c => c.StartsWith("gunzip") ? (1, "", "no gunzip") : FakeSamplerSession.Default(c);

        await Lifecycle.InstallAsync(_binary);

        _session.Puts.Should().HaveCount(2);
        _session.Puts[1].Path.Should().Be("/home/u/.towertail/towertail-sampler");
        Encoding.UTF8.GetString(_session.Puts[1].Data).Should().Be("binary-bytes");
    }

    [Fact]
    public async Task MachineIdReadsTheOnceSample()
    {
        (await Lifecycle.MachineIdAsync()).Should().Be("m-1");
    }

    [Theory]
    [InlineData(1, "")]
    [InlineData(0, "not json")]
    public async Task MachineIdIsNullWhenTheSampleFails(int code, string text)
    {
        _session.Handler = _ => (code, text, "");
        (await Lifecycle.MachineIdAsync()).Should().BeNull();
    }

    [Fact]
    public async Task EnrollSendsNodeAndMachineId()
    {
        var node = new Node { DisplayName = "web", Kind = NodeKind.Ssh };

        var reply = await Lifecycle.EnrollAsync(node);

        _enroller.Request.Should().Be(new EnrollRequest("web", node.Id, "m-1"));
        reply.NodeId.Should().Be(node.Id);
    }

    [Fact]
    public async Task EnrollMapsA402ToPlanFull()
    {
        _enroller.Fail = new RemoteException(402, "the plan allows 3 hosts");
        var act = () => Lifecycle.EnrollAsync(new Node { DisplayName = "web" });

        var ex = (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which;
        ex.Kind.Should().Be(LifecycleFailure.PlanFull);
        ex.PlanMax.Should().Be(3);
    }

    [Fact]
    public async Task EnrollNeedsAnEnroller()
    {
        var act = () => new SamplerLifecycle(_session).EnrollAsync(new Node());
        (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which.Kind.Should().Be(LifecycleFailure.NotSignedIn);
    }

    [Fact]
    public async Task StartKeepsTheTokenOutOfTheCommandLine()
    {
        await Lifecycle.StartAsync(new Uri("https://cloud.example/"), "stk-secret");

        _session.Commands.Should().NotContain(c => c.Contains("stk-secret"));
        var put = _session.Puts.Should().ContainSingle().Which;
        put.Path.Should().Be("/home/u/.towertail/sampler.token.in");
        Encoding.UTF8.GetString(put.Data).Should().Be("stk-secret\n");
        _session.Commands.Should().Contain(c => c.Contains("umask 077"));
        var start = _session.Commands.Last();
        start.Should().Contain("--token-file -").And.Contain("< ~/.towertail/sampler.token.in").And.Contain("rm -f ~/.towertail/sampler.token.in");
    }

    [Fact]
    public async Task StartFailureCarriesTheError()
    {
        _session.Handler = c => c.Contains(" start ") ? (1, "", "no space") : FakeSamplerSession.Default(c);

        var act = () => Lifecycle.StartAsync(new Uri("https://cloud.example/"), "t");

        var ex = (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which;
        ex.Kind.Should().Be(LifecycleFailure.StartFailed);
        ex.Message.Should().Contain("no space");
    }

    [Fact]
    public async Task RestartRunsStartWithTheSavedConfig()
    {
        await Lifecycle.RestartAsync();
        _session.Commands.Should().ContainSingle().Which.Should().Be("~/.towertail/towertail-sampler start");
    }

    [Fact]
    public async Task EnableBootGivesTheMode()
    {
        (await Lifecycle.EnableBootAsync()).Should().Be("systemd");
    }

    [Theory]
    [InlineData(1, "")]
    [InlineData(0, "{}")]
    [InlineData(0, "garbage")]
    public async Task EnableBootFailsWithNoMode(int code, string text)
    {
        _session.Handler = _ => (code, text, "");
        var act = () => Lifecycle.EnableBootAsync();
        (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which.Kind.Should().Be(LifecycleFailure.NoBootMethod);
    }

    [Fact]
    public async Task StatusReadsTheJson()
    {
        _session.Handler = _ => (0, """{"running":true,"pid":7,"version":"1.2+abc","last_status":200,"update":"1.3"}""", "");

        var status = await Lifecycle.StatusAsync();

        status.Running.Should().BeTrue();
        status.Pid.Should().Be(7);
        status.Version.Should().Be("1.2+abc");
        status.LastStatus.Should().Be(200);
        status.Update.Should().Be("1.3");
    }

    [Fact]
    public async Task StatusRejectsBadJson()
    {
        _session.Handler = _ => (0, "oops", "");
        var act = () => Lifecycle.StatusAsync();
        (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which.Kind.Should().Be(LifecycleFailure.StatusUnreadable);
    }

    [Fact]
    public async Task UpdateInstallsThenRestarts()
    {
        await Lifecycle.UpdateAsync(_binary);
        _session.Commands.Last().Should().Be("~/.towertail/towertail-sampler start");
        _session.Puts.Should().NotBeEmpty();
    }

    [Fact]
    public async Task RemoveStopsDisablesBootAndDeletesTheConfig()
    {
        await Lifecycle.RemoveAsync();

        _session.Commands.Should().ContainInOrder(
            "~/.towertail/towertail-sampler stop",
            "~/.towertail/towertail-sampler boot disable",
            "rm -f ~/.towertail/sampler.token ~/.towertail/sampler.json");
    }

    [Fact]
    public async Task RemoveAcceptsAHostWithNoSampler()
    {
        _session.Handler = c => c.EndsWith("boot disable") ? (127, "", "not found") : FakeSamplerSession.Default(c);
        await Lifecycle.RemoveAsync();
    }

    [Fact]
    public async Task RemoveFailsWhenBootDisableFails()
    {
        _session.Handler = c => c.EndsWith("boot disable") ? (1, "", "denied") : FakeSamplerSession.Default(c);
        var act = () => Lifecycle.RemoveAsync();
        (await act.Should().ThrowAsync<SamplerLifecycleException>()).Which.Message.Should().Contain("denied");
    }
}
