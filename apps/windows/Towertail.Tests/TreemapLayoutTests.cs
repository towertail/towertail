using FluentAssertions;
using Towertail.WinUI.State;
using Xunit;

namespace Towertail.Tests;

public sealed class TreemapLayoutTests
{
    private static readonly TreemapRect Frame = new(10, 20, 600, 400);

    [Theory]
    [InlineData(new double[] { 50, 30, 10, 5, 3, 2 })]
    [InlineData(new double[] { 1, 1, 1, 1, 1, 1, 1, 1, 1, 1 })]
    [InlineData(new double[] { 1000, 1, 1, 1 })]
    [InlineData(new double[] { 7, 300, 12, 45, 45, 2, 90, 31 })]
    public void AreasMatchSizesInsideFrameWithoutOverlap(double[] sizes)
    {
        var placed = TreemapLayout.Squarify(sizes, s => s, Frame);
        placed.Should().HaveCount(sizes.Length);

        var total = sizes.Sum();
        foreach (var p in placed)
        {
            var expected = p.Item / total * Frame.Area;
            p.Rect.Area.Should().BeApproximately(expected, expected * 0.01 + 1e-6);
            p.Rect.X.Should().BeGreaterThanOrEqualTo(Frame.X - 1e-6);
            p.Rect.Y.Should().BeGreaterThanOrEqualTo(Frame.Y - 1e-6);
            p.Rect.Right.Should().BeLessThanOrEqualTo(Frame.Right + 1e-6);
            p.Rect.Bottom.Should().BeLessThanOrEqualTo(Frame.Bottom + 1e-6);
        }

        for (var i = 0; i < placed.Count; i++)
        for (var j = i + 1; j < placed.Count; j++)
        {
            var a = placed[i].Rect;
            var b = placed[j].Rect;
            var w = Math.Min(a.Right, b.Right) - Math.Max(a.X, b.X);
            var h = Math.Min(a.Bottom, b.Bottom) - Math.Max(a.Y, b.Y);
            (w > 1e-6 && h > 1e-6).Should().BeFalse($"tiles {i} and {j} overlap");
        }

        placed.Sum(p => p.Rect.Area).Should().BeApproximately(Frame.Area, Frame.Area * 0.001);
    }

    [Fact]
    public void EmptyListGivesNoTiles()
        => TreemapLayout.Squarify(Array.Empty<double>(), s => s, Frame).Should().BeEmpty();

    [Fact]
    public void OneItemFillsTheFrame()
    {
        var placed = TreemapLayout.Squarify(new[] { 42.0 }, s => s, Frame);
        var r = placed.Should().ContainSingle().Which.Rect;
        r.X.Should().BeApproximately(Frame.X, 1e-9);
        r.Y.Should().BeApproximately(Frame.Y, 1e-9);
        r.Width.Should().BeApproximately(Frame.Width, 1e-9);
        r.Height.Should().BeApproximately(Frame.Height, 1e-9);
    }

    [Fact]
    public void ZeroSizesAndEmptyFrameGiveNoTiles()
    {
        TreemapLayout.Squarify(new[] { 0.0, -1 }, s => s, Frame).Should().BeEmpty();
        TreemapLayout.Squarify(new[] { 3.0, 0 }, s => s, Frame).Should().ContainSingle();
        TreemapLayout.Squarify(new[] { 3.0 }, s => s, new TreemapRect(0, 0, 0, 10)).Should().BeEmpty();
    }

    [Fact]
    public void LaysOutTheFixtureTree()
    {
        var tree = Helpers.ScanFixture.Result("darwin").Tree;
        var placed = TreemapLayout.Squarify(tree.Children, n => n.B, Frame);
        placed.Should().HaveCount(tree.Children.Count(c => c.B > 0));
        placed[0].Item.B.Should().Be(tree.Children.Max(c => c.B));
    }
}
