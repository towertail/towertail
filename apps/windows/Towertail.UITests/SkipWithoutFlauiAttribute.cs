namespace Towertail.UITests;

/// <summary>Skips the fact unless <c>scripts/test.ps1 --flaui</c> sets <c>TOWERTAIL_FLAUI=1</c>.</summary>
public sealed class SkipWithoutFlauiAttribute : Xunit.FactAttribute
{
    public SkipWithoutFlauiAttribute()
    {
        if (Environment.GetEnvironmentVariable("TOWERTAIL_FLAUI") != "1")
        {
            Skip = "set TOWERTAIL_FLAUI=1 — use scripts/test.ps1 --flaui";
        }
    }
}
