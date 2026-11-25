using System.Text.Json;
using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class HealthScoreTests
{
    private sealed record Row(double Value, double Warn, double Critical, double Score, string Tint);

    private static readonly MetricThresholds T = MetricThresholds.Defaults;

    private static HealthScore Score(HealthInfo? info = null, double? cpu = null, double? mem = null, double? disk = null,
        IReadOnlyList<LongProc>? longProcs = null, MutedSignals? muted = null)
        => HealthScore.Evaluate(info, null, cpu, mem, disk, longProcs ?? Array.Empty<LongProc>(), T, muted ?? MutedSignals.None);

    [Fact]
    public void BandsMatchFixtureTable()
    {
        var path = Path.Combine(AppContext.BaseDirectory, "TestData", "health-score-cases.json");
        var rows = JsonSerializer.Deserialize<List<Row>>(File.ReadAllText(path), new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
        rows.Should().NotBeEmpty();
        foreach (var r in rows)
        {
            var s = HealthScore.ScoreOf(r.Value, r.Warn, r.Critical);
            s.Should().BeApproximately(r.Score, 0.01, because: r.ToString());
            HealthScore.LevelOf(s).ToString().ToLowerInvariant().Should().Be(r.Tint switch { "nominal" => "nominal", "warn" => "warn", _ => "critical" });
        }
    }

    [Fact]
    public void ScoreIsWorstPart()
    {
        var s = Score(cpu: 0.5, mem: 0.8, disk: 0.1);
        s.Worst!.Check.Should().Be(HealthCheck.Mem);
        s.Score.Should().Be(s.Parts.Min(p => p.Score));
        s.Parts.Select(p => p.Score).Should().BeInAscendingOrder();
    }

    [Fact]
    public void MutedCheckIsSkipped()
    {
        Score(cpu: 0.95).Level.Should().Be(HealthLevel.Critical);
        var quiet = Score(cpu: 0.95, muted: new MutedSignals(new[] { AlertSignal.Cpu }));
        quiet.Parts.Should().BeEmpty();
        quiet.Score.Should().Be(100);
    }

    [Fact]
    public void LongProcScores70()
    {
        var p = new ProcSample(7, 1, "node", null, "u", 90, 1, 1, null, null, null, null);
        var lp = new LongProc(LongProcKey.Of(p), 7, "node", null, 90, DateTime.UtcNow, null);
        var s = Score(longProcs: new[] { lp });
        s.Score.Should().Be(70);
        s.Level.Should().Be(HealthLevel.Warn);
    }

    [Fact]
    public void NoPartsScores100()
    {
        var s = Score();
        s.Score.Should().Be(100);
        s.Level.Should().Be(HealthLevel.Nominal);
        s.Worst.Should().BeNull();
    }

    [Fact]
    public void LevelAgreesWithHealthStatusLevel()
    {
        foreach (var procs in new[] { 1_000, 4_999, 5_000, 6_000, 19_999, 20_000, 25_000 })
        {
            var info = new HealthInfo(procs, Zombies: procs / 10);
            var status = HealthStatus.Evaluate(info, null, T);
            Score(info).Level.Should().Be(status.Level, because: $"procs {procs}");
        }
    }
}
