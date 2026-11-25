using FluentAssertions;
using Towertail.WinUI.Cloud;
using Xunit;

namespace Towertail.Tests;

public sealed class AccountTextTests
{
    private static CloudMe Me(string plan = "free") => new(new CloudUser("a@b.co"), new CloudOrg("Acme", plan), "owner");

    [Theory]
    [InlineData("482913", "482913")]
    [InlineData("482 913", "482913")]
    [InlineData("12ab34", "1234")]
    [InlineData("1234567", "123456")]
    [InlineData("", "")]
    public void DigitsKeepsSixAsciiDigits(string text, string expected)
        => AccountText.Digits(text).Should().Be(expected);

    [Fact]
    public void ClockFormatsMinutesAndSeconds()
    {
        AccountText.Clock(TimeSpan.FromSeconds(42)).Should().Be("0:42");
        AccountText.Clock(TimeSpan.FromSeconds(60)).Should().Be("1:00");
        AccountText.Clock(TimeSpan.FromMilliseconds(41_200)).Should().Be("0:42");
    }

    [Fact]
    public void ResendTextShowsTheWaitOnlyWhileItRuns()
    {
        AccountText.ResendText(TimeSpan.FromSeconds(42)).Should().Be("Send a new code (0:42)");
        AccountText.ResendText(TimeSpan.Zero).Should().Be("Send a new code");
    }

    [Fact]
    public void PlanTextUsesBillingWhenItIsKnown()
    {
        var billing = new CloudBilling(new CloudPlan("solo", 5), 3, 5);
        AccountText.PlanText(Me(), billing).Should().Be("Solo plan, 5 Cloud hosts");
        AccountText.PlanText(Me("team"), null).Should().Be("Team plan");
        AccountText.HostsText(billing).Should().Be("3 of 5");
        AccountText.HostsText(billing with { MaxNodes = 8 }).Should().Be("3 of 8");
    }

    [Fact]
    public void SignOutMessageFollowsTheMacText()
    {
        AccountText.SignOutMessage(0, 0).Should().Be("No host on this PC reports to Cloud.");
        AccountText.SignOutMessage(1, 0).Should().Be(
            "1 host now reports to Cloud. The app removes its sampler and deletes it from Cloud. "
            + "This PC then watches it over SSH again. Cloud alerts for this host stop.");
        AccountText.SignOutMessage(3, 2).Should().Be(
            "3 hosts now report to Cloud. The app removes their samplers and deletes them from Cloud. "
            + "This PC then watches them over SSH again. Cloud alerts for these hosts stop. "
            + "2 hosts exist only in Cloud. They stay in Cloud and leave this PC.");
    }

    [Fact]
    public void DeleteNeedsTheEmailAndNoBlock()
    {
        var org = new CloudOrgDeletion(Guid.NewGuid(), "Acme", "owner", "delete", 1, 3);
        var open = new CloudDeletion("a@b.co", [org], false);
        AccountText.CanDelete(open, "A@B.co ", busy: false).Should().BeTrue();
        AccountText.CanDelete(open, "x@b.co", busy: false).Should().BeFalse();
        AccountText.CanDelete(open, "a@b.co", busy: true).Should().BeFalse();
        AccountText.CanDelete(open with { Blocked = true }, "a@b.co", busy: false).Should().BeFalse();
        AccountText.CanDelete(null, "a@b.co", busy: false).Should().BeFalse();
        AccountText.DeleteDetail(org).Should().Be("Deleted with 3 hosts");
        AccountText.DeleteBadge(org with { Fate = "blocked" }).Should().Be("Blocked");
    }
}
