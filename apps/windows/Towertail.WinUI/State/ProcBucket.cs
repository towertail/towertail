namespace Towertail.WinUI.State;

/// <summary>The tier of a process snapshot. The age of the time selects it, as on the server.</summary>
public enum ProcBucketTier { Raw, Minute, Hour }

/// <summary>The bucket of the process snapshot at one time.</summary>
public sealed record ProcBucket(ProcBucketTier Tier, DateTime Start, TimeSpan Span, int Limit)
{
    public string Label => Tier switch
    {
        ProcBucketTier.Raw => "each sample (10 s)",
        ProcBucketTier.Minute => "top 10 of each minute",
        _ => "top 5 of each hour",
    };

    /// <summary>The key of the tier in the cache and in the request.</summary>
    public string TierName => Tier switch { ProcBucketTier.Raw => "raw", ProcBucketTier.Minute => "1m", _ => "1h" };

    /// <summary>The bucket that holds <paramref name="t"/>. Raw has no fixed interval on the server: 10 s is the usual one.</summary>
    public static ProcBucket Of(DateTime t, DateTime now)
    {
        var age = (now - t).TotalSeconds;
        var (tier, span, limit) = age <= 3_600 ? (ProcBucketTier.Raw, 10, 20)
            : age <= 7 * 86_400 ? (ProcBucketTier.Minute, 60, 10)
            : (ProcBucketTier.Hour, 3_600, 5);
        var utc = t.ToUniversalTime();
        var seconds = (long)Math.Floor((utc - DateTime.UnixEpoch).TotalSeconds / span) * span;
        return new ProcBucket(tier, DateTime.UnixEpoch.AddSeconds(seconds), TimeSpan.FromSeconds(span), limit);
    }

    /// <summary>The bucket <paramref name="n"/> spans away, with the same tier.</summary>
    public ProcBucket Shifted(int n) => this with { Start = Start + n * Span };

    public DateTime End => Start + Span;
}
