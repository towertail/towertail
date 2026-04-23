using System.Diagnostics;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests.Integration;

/// <summary>
/// Tier 2 end-to-end tests against the full real stack. Two activation modes:
///
/// 1. <c>TOWERTAIL_LOCAL_SERVER=1</c> — <see cref="LocalServerFixture"/>
///    boots the real Go <c>towertail-server</c> as a child process with
///    <c>TT_CLICKHOUSE__DISABLED=true</c>. No Docker required. Sample
///    ingest still publishes to the WS hub; persistence is skipped.
///
/// 2. <c>TOWERTAIL_INTEGRATION=1</c> (only) — point at an existing server
///    brought up via <c>server/docker/docker-compose.test.yaml</c>. Full
///    ClickHouse persistence, matches the Mac Tier 2 path.
///
/// The suite is skipped when neither flag is set, so the default
/// <c>dotnet test</c> (Tier 1) path stays green on developer boxes.
///
/// Mirrors <c>app/mac/Tests/Integration/RemoteBackendIntegrationTests.swift</c>.
/// </summary>
[Trait("Category", "Integration")]
public sealed class RemoteBackendIntegrationTests : IClassFixture<LocalServerFixture>
{
    private readonly LocalServerFixture _fixture;

    public RemoteBackendIntegrationTests(LocalServerFixture fixture)
    {
        _fixture = fixture;
    }

    private static bool AnyModeEnabled =>
        IntegrationConfig.Enabled || LocalServerFixture.RequestedByEnv;

    [SkipWithoutIntegration]
    public async Task AdminTokenCanListNodes()
    {
        await using var client = new RemoteClient(_fixture.Endpoint, _fixture.AdminToken);
        var nodes = await client.ListNodesAsync();
        nodes.Should().NotBeNull();
    }

    [SkipWithoutIntegration]
    public async Task SettingsRoundTripThroughRealServer()
    {
        await using var client = new RemoteClient(_fixture.Endpoint, _fixture.AdminToken);
        var original = await client.GetSettingsAsync();
        var modified = original with
        {
            NotifyDebounceSeconds = (original.NotifyDebounceSeconds % 120) + 31,
        };
        var saved = await client.PutSettingsAsync(modified);
        saved.NotifyDebounceSeconds.Should().Be(modified.NotifyDebounceSeconds);

        var reread = await client.GetSettingsAsync();
        reread.NotifyDebounceSeconds.Should().Be(modified.NotifyDebounceSeconds);

        await client.PutSettingsAsync(original);
    }

    [SkipWithoutIntegration]
    public async Task SamplerPushDeliversSampleOverWebSocket()
    {
        var samplerBinary = IntegrationConfig.LocateSamplerBinary();
        Assert.SkipWhen(samplerBinary is null, "sampler binary not found — run scripts/build.sampler.sh");

        await using var client = new RemoteClient(_fixture.Endpoint, _fixture.AdminToken);

        var enroll = await client.EnrollSamplerAsync(
            new EnrollRequest($"it-{Guid.NewGuid().ToString("N")[..8]}"));
        enroll.NodeId.Should().NotBe(Guid.Empty);
        enroll.SamplerToken.Should().NotBeNullOrEmpty();

        var wsUri = new UriBuilder(_fixture.Endpoint)
        {
            Scheme = _fixture.Endpoint.Scheme == "https" ? "wss" : "ws",
            Path = "/v1/stream",
        }.Uri;
        using var ws = new ClientWebSocket();
        ws.Options.SetRequestHeader("Authorization", $"Bearer {_fixture.AdminToken}");
        using var wsCts = new CancellationTokenSource(TimeSpan.FromSeconds(45));
        await ws.ConnectAsync(wsUri, wsCts.Token);

        var psi = new ProcessStartInfo(samplerBinary!)
        {
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        };
        psi.ArgumentList.Add("push");
        psi.ArgumentList.Add("--endpoint"); psi.ArgumentList.Add(_fixture.Endpoint.ToString().TrimEnd('/'));
        psi.ArgumentList.Add("--token");    psi.ArgumentList.Add(enroll.SamplerToken);
        psi.ArgumentList.Add("--interval"); psi.ArgumentList.Add("1s");
        psi.ArgumentList.Add("--flush");    psi.ArgumentList.Add("500ms");
        psi.ArgumentList.Add("--batch");    psi.ArgumentList.Add("1");
        psi.ArgumentList.Add("--no-proc");
        using var proc = Process.Start(psi)!;
        try
        {
            var deadline = DateTime.UtcNow.AddSeconds(30);
            var buffer = new byte[64 * 1024];
            var seenForOurNode = false;
            while (DateTime.UtcNow < deadline && !seenForOurNode)
            {
                using var ms = new MemoryStream();
                WebSocketReceiveResult result;
                do
                {
                    result = await ws.ReceiveAsync(buffer, wsCts.Token);
                    if (result.MessageType == WebSocketMessageType.Close) break;
                    ms.Write(buffer, 0, result.Count);
                } while (!result.EndOfMessage);

                if (ms.Length == 0) continue;
                var line = Encoding.UTF8.GetString(ms.ToArray());
                try
                {
                    using var doc = JsonDocument.Parse(line);
                    var type = doc.RootElement.TryGetProperty("type", out var t) ? t.GetString() : null;
                    var nodeIdStr = doc.RootElement.TryGetProperty("nodeId", out var n) ? n.GetString() : null;
                    if (type == "sample" && Guid.TryParse(nodeIdStr, out var gotId) && gotId == enroll.NodeId)
                    {
                        seenForOurNode = true;
                    }
                }
                catch (JsonException) { }
            }
            seenForOurNode.Should().BeTrue("a sample frame for the enrolled node must arrive on the admin WS within 30s");
        }
        finally
        {
            if (!proc.HasExited)
            {
                try { proc.Kill(entireProcessTree: true); } catch { }
            }
            try
            {
                await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, null, CancellationToken.None);
            }
            catch { }
        }
    }
}
