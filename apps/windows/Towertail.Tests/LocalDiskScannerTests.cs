using System.IO.Pipes;
using FluentAssertions;
using Towertail.Tests.Helpers;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class LocalDiskScannerTests
{
    /// <summary>A sampler process: the test writes its stdout, and stdin close is recorded.</summary>
    private sealed class FakeScanProcess : IStreamingProcess
    {
        private readonly AnonymousPipeServerStream _server = new(PipeDirection.Out);
        private readonly StreamWriter _writer;
        private readonly TaskCompletionSource<int> _exit = new(TaskCreationOptions.RunContinuationsAsynchronously);
        public StreamReader StdOut { get; }
        public bool StdInClosed { get; private set; }
        public bool Disposed { get; private set; }
        /// <summary>What the sampler does on stdin EOF. Default: it writes <c>cancelled</c> and exits.</summary>
        public Action<FakeScanProcess>? OnStdInClosed { get; set; } = p => { p.Write("""{"t":"cancelled","v":1}"""); p.Exit(0); };
        public string StdErrText { get; set; } = "";

        public FakeScanProcess()
        {
            _writer = new StreamWriter(_server) { AutoFlush = true };
            StdOut = new StreamReader(new AnonymousPipeClientStream(PipeDirection.In, _server.ClientSafePipeHandle));
        }

        public void Write(string line) { lock (_writer) _writer.WriteLine(line); }
        public void Exit(int code)
        {
            lock (_writer) { try { _writer.Dispose(); } catch { } }
            _exit.TrySetResult(code);
        }

        public bool HasExited => _exit.Task.IsCompleted;
        public Task<int> WaitAsync(CancellationToken ct = default) => _exit.Task.WaitAsync(ct);
        public void CloseStdIn()
        {
            if (StdInClosed) return;
            StdInClosed = true;
            OnStdInClosed?.Invoke(this);
        }
        public ValueTask DisposeAsync()
        {
            Disposed = true;
            Exit(-1);
            StdOut.Dispose();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class FakeRunner : IProcessRunner
    {
        public FakeScanProcess Process { get; } = new();
        public ProcessRequest? Request { get; private set; }
        public Exception? StartError { get; set; }
        public Task<ProcessResult> RunAsync(ProcessRequest r, CancellationToken ct = default) => throw new NotSupportedException();
        public Task<IStreamingProcess> StartStreamingAsync(ProcessRequest r, CancellationToken ct = default)
        {
            Request = r;
            return StartError is null ? Task.FromResult<IStreamingProcess>(Process) : Task.FromException<IStreamingProcess>(StartError);
        }
    }

    private static readonly Node Local = Node.LocalWindows("pc");

    private static async Task<List<DiskScanEvent>> Collect(IAsyncEnumerable<DiskScanEvent> events)
    {
        var list = new List<DiskScanEvent>();
        await foreach (var e in events) list.Add(e);
        return list;
    }

    [Fact]
    public async Task StreamsTheFixtureInOrder()
    {
        var lines = ScanFixture.Lines("windows");
        var runner = new FakeRunner();
        var scanner = new LocalDiskScanner(runner, explicitPath: "sampler.exe");
        var task = Collect(scanner.ScanAsync(Local, new DiskScanRequest("~"), TestContext.Current.CancellationToken));
        _ = Task.Run(() =>
        {
            runner.Process.Write("garbage line");
            foreach (var l in lines) runner.Process.Write(l);
            runner.Process.Exit(0);
        });

        var events = await task;
        runner.Request!.Executable.Should().Be("sampler.exe");
        runner.Request.Arguments.Should().Equal("scan", "--root", "~");
        events.Should().HaveCount(lines.Length);
        events[^1].Should().BeOfType<DiskScanEvent.Result>();
        runner.Process.Disposed.Should().BeTrue();
    }

    [Fact]
    public async Task CancelClosesStdInAndEndsCancelled()
    {
        var runner = new FakeRunner();
        var scanner = new LocalDiskScanner(runner, explicitPath: "sampler.exe");
        using var cts = new CancellationTokenSource();
        var events = new List<DiskScanEvent>();
        runner.Process.Write(ScanFixture.Lines("linux")[0]);
        await foreach (var e in scanner.ScanAsync(Local, new DiskScanRequest(), cts.Token))
        {
            events.Add(e);
            if (e is DiskScanEvent.Progress) cts.Cancel();
        }
        runner.Process.StdInClosed.Should().BeTrue();
        events[^1].Should().BeOfType<DiskScanEvent.Cancelled>();
    }

    [Fact]
    public async Task CancelEndsAfterGraceWhenSamplerStaysSilent()
    {
        var runner = new FakeRunner();
        runner.Process.OnStdInClosed = null;
        var scanner = new LocalDiskScanner(runner, explicitPath: "s", cancelGrace: TimeSpan.FromMilliseconds(200));
        using var cts = new CancellationTokenSource(TimeSpan.FromMilliseconds(100));
        var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest(), cts.Token));
        events.Should().ContainSingle().Which.Should().BeOfType<DiskScanEvent.Cancelled>();
        runner.Process.StdInClosed.Should().BeTrue();
    }

    [Fact]
    public async Task WatchdogStopsASilentScan()
    {
        var runner = new FakeRunner();
        var scanner = new LocalDiskScanner(runner, explicitPath: "s", watchdog: TimeSpan.FromMilliseconds(200));
        var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest(), TestContext.Current.CancellationToken));
        events.Should().ContainSingle().Which.Should().BeOfType<DiskScanEvent.Error>()
            .Which.Message.Should().Contain("no output");
        runner.Process.StdInClosed.Should().BeTrue();
    }

    [Fact]
    public async Task ExitWithoutResultIsAnErrorWithStderr()
    {
        var runner = new FakeRunner();
        runner.Process.StdErrText = "unknown command \"scan\"\n";
        var scanner = new LocalDiskScanner(runner, explicitPath: "s");
        _ = Task.Run(() => runner.Process.Exit(2));
        var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest(), TestContext.Current.CancellationToken));
        events.Should().ContainSingle().Which.Should().BeOfType<DiskScanEvent.Error>()
            .Which.Message.Should().Be("The scan ended without a result (exit 2: unknown command \"scan\").");
    }

    [Fact]
    public async Task SamplerErrorLineEndsTheScan()
    {
        var runner = new FakeRunner();
        runner.Process.Write("""{"t":"error","v":1,"message":"root not found: /nope"}""");
        var scanner = new LocalDiskScanner(runner, explicitPath: "s");
        var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest("/nope"), TestContext.Current.CancellationToken));
        events.Should().ContainSingle().Which.Should().Be(new DiskScanEvent.Error("root not found: /nope"));
    }

    [Fact]
    public async Task StartFailureIsAnError()
    {
        var runner = new FakeRunner { StartError = new FileNotFoundException("no sampler") };
        var scanner = new LocalDiskScanner(runner, explicitPath: "s");
        var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest(), TestContext.Current.CancellationToken));
        events.Should().ContainSingle().Which.Should().Be(new DiskScanEvent.Error("Could not start the sampler: no sampler"));
    }

    [Fact]
    public void SshCommandLineQuotesArguments()
    {
        SshDiskScanner.CommandLine("~/.towertail/towertail-sampler", new DiskScanRequest("/var/lib", 3))
            .Should().Be("~/.towertail/towertail-sampler scan --root /var/lib --max-depth 3");
        SshDiskScanner.CommandLine("s", new DiskScanRequest("~"))
            .Should().Be("s scan --root '~'");
        SshDiskScanner.ShellQuote("/data/it's here").Should().Be("'/data/it'\\''s here'");
        SshDiskScanner.ShellQuote("$(reboot)").Should().Be("'$(reboot)'");
        SshDiskScanner.ShellQuote("").Should().Be("''");
    }
    [Fact]
    public async Task RealSamplerScansAFolder()
    {
        var sampler = ScanFixture.HostSampler();
        var root = Directory.CreateTempSubdirectory("tt-scan-");
        try
        {
            File.WriteAllBytes(Path.Combine(root.FullName, "big.bin"), new byte[300_000]);
            var scanner = new LocalDiskScanner(new DefaultProcessRunner(), sampler);
            var events = await Collect(scanner.ScanAsync(Local, new DiskScanRequest(root.FullName),
                TestContext.Current.CancellationToken)).WaitAsync(TimeSpan.FromSeconds(60), TestContext.Current.CancellationToken);
            var result = events[^1].Should().BeOfType<DiskScanEvent.Result>().Subject.Value;
            result.Files.Should().Be(1);
            result.ScannedBytes.Should().BeGreaterThanOrEqualTo(300_000);
        }
        finally { root.Delete(recursive: true); }
    }

    [Fact]
    public async Task RealSamplerStopsOnCancel()
    {
        var sampler = ScanFixture.HostSampler();
        var scanner = new LocalDiskScanner(new DefaultProcessRunner(), sampler);
        using var cts = new CancellationTokenSource();
        var events = new List<DiskScanEvent>();
        var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
        await foreach (var e in scanner.ScanAsync(Local, new DiskScanRequest(home), cts.Token)
                           .WithCancellation(TestContext.Current.CancellationToken))
        {
            events.Add(e);
            cts.Cancel();
        }
        events[^1].Should().BeOfType<DiskScanEvent.Cancelled>();
    }
}
