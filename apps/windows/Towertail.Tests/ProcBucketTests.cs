using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class ProcBucketTests
{
    private static readonly DateTime Now = new(2026, 4, 20, 12, 0, 0, DateTimeKind.Utc);

    [Theory]
    [InlineData(59 * 60, ProcBucketTier.Raw)]
    [InlineData(61 * 60, ProcBucketTier.Minute)]
    [InlineData(7 * 86_400, ProcBucketTier.Minute)]
    [InlineData(7 * 86_400 + 1, ProcBucketTier.Hour)]
    public void TierByAge(int ageSeconds, ProcBucketTier tier)
        => ProcBucket.Of(Now.AddSeconds(-ageSeconds), Now).Tier.Should().Be(tier);

    [Fact]
    public void LimitAndSpanFollowTier()
    {
        ProcBucket.Of(Now, Now).Should().Match<ProcBucket>(b => b.Limit == 20 && b.Span == TimeSpan.FromSeconds(10));
        ProcBucket.Of(Now.AddHours(-2), Now).Should().Match<ProcBucket>(b => b.Limit == 10 && b.Span == TimeSpan.FromSeconds(60));
        ProcBucket.Of(Now.AddDays(-8), Now).Should().Match<ProcBucket>(b => b.Limit == 5 && b.Span == TimeSpan.FromHours(1));
    }

    [Fact]
    public void StartRoundsDown()
    {
        var t = new DateTime(2026, 4, 20, 12, 0, 37, DateTimeKind.Utc);
        ProcBucket.Of(t, t).Start.Should().Be(new DateTime(2026, 4, 20, 12, 0, 30, DateTimeKind.Utc));
        ProcBucket.Of(t.AddHours(-2), t).Start.Should().Be(new DateTime(2026, 4, 20, 10, 0, 0, DateTimeKind.Utc));
    }

    [Fact]
    public void ShiftedKeepsTier()
    {
        var b = ProcBucket.Of(Now.AddHours(-2), Now);
        var s = b.Shifted(-2);
        s.Tier.Should().Be(b.Tier);
        s.Start.Should().Be(b.Start.AddMinutes(-2));
        b.Shifted(1).Start.Should().Be(b.End);
    }

    [Fact]
    public void TierNames()
    {
        ProcBucket.Of(Now, Now).TierName.Should().Be("raw");
        ProcBucket.Of(Now.AddHours(-2), Now).TierName.Should().Be("1m");
        ProcBucket.Of(Now.AddDays(-8), Now).TierName.Should().Be("1h");
    }
}
