using FluentAssertions;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ProcessRunnerTests
{
    private sealed class FakeRunner : IProcessRunner
    {
        public List<ProcessRequest> Requests { get; } = new();
        public Task<ProcessResult> RunAsync(ProcessRequest r, CancellationToken ct = default)
        {
            Requests.Add(r);
            return Task.FromResult(new ProcessResult(0, "ok\n", ""));
        }
        public Task<IStreamingProcess> StartStreamingAsync(ProcessRequest r, CancellationToken ct = default)
            => Task.FromResult<IStreamingProcess>(null!);
    }

    [Fact]
    public async Task SshInvokerPassesTargetAndRemoteCommand()
    {
        var runner = new FakeRunner();
        var n = new Node { Kind = NodeKind.Ssh, SshUser = "u", SshHost = "h.tail.ts.net" };
        var invoker = new SshSamplerInvoker(runner, n, sshExe: "ssh.exe");
        await invoker.SelfCheckAsync();
        var req = runner.Requests.Single();
        req.Executable.Should().Be("ssh.exe");
        req.Arguments.Should().Contain("u@h.tail.ts.net");
        string.Join(" ", req.Arguments).Should().Contain("--self-check");
    }
}
