using FluentAssertions;
using Towertail.WinUI.Collectors;
using Xunit;

namespace Towertail.Tests;

public sealed class SamplerInvokerTests
{
    private sealed class FakeRunner : IProcessRunner
    {
        public List<ProcessRequest> Requests { get; } = new();
        public Func<ProcessRequest, ProcessResult>? OnRun;
        public Task<ProcessResult> RunAsync(ProcessRequest r, CancellationToken ct = default)
        {
            Requests.Add(r);
            return Task.FromResult(OnRun?.Invoke(r) ?? new ProcessResult(0, "", ""));
        }
        public Task<IStreamingProcess> StartStreamingAsync(ProcessRequest r, CancellationToken ct = default)
            => Task.FromResult<IStreamingProcess>(null!);
    }

    [Fact]
    public async Task OnceArgShape()
    {
        var runner = new FakeRunner
        {
            OnRun = _ => new ProcessResult(0, File.ReadAllText(Path.Combine("TestData", "sample-v1.json")), "")
        };
        var invoker = new LocalSamplerInvoker(runner, explicitPath: @"C:\fake\towertail-sampler.exe");
        var sample = await invoker.RunOnceAsync();
        sample.V.Should().Be(1);
        runner.Requests.Single().Arguments.Should().BeEquivalentTo(new[] { "--once" });
    }

    [Fact]
    public async Task SelfCheckParsesOk()
    {
        var runner = new FakeRunner { OnRun = _ => new ProcessResult(0, "ok\n", "") };
        var invoker = new LocalSamplerInvoker(runner, explicitPath: @"C:\fake\x.exe");
        (await invoker.SelfCheckAsync()).Should().BeTrue();
    }

    [Fact]
    public async Task VersionShapeIsSingleArg()
    {
        var runner = new FakeRunner { OnRun = _ => new ProcessResult(0, "towertail-sampler 0.1.0+abc\n", "") };
        var invoker = new LocalSamplerInvoker(runner, explicitPath: @"C:\fake\x.exe");
        var v = await invoker.VersionAsync();
        v.Should().Be("towertail-sampler 0.1.0+abc");
        runner.Requests.Single().Arguments.Should().BeEquivalentTo(new[] { "--version" });
    }
}
