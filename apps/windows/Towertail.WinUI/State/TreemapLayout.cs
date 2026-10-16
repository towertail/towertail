namespace Towertail.WinUI.State;

public readonly record struct TreemapRect(double X, double Y, double Width, double Height)
{
    public double Area => Width * Height;
    public double Right => X + Width;
    public double Bottom => Y + Height;

    public TreemapRect Inset(double left, double top, double right, double bottom)
        => new(X + left, Y + top, Math.Max(0, Width - left - right), Math.Max(0, Height - top - bottom));
}

public readonly record struct TreemapPlaced<T>(T Item, TreemapRect Rect);

/// <summary>
/// Squarified treemap (Bruls, Huizing, van Wijk). Pure: the same input gives the same output.
/// Same algorithm as the Mac TreemapLayout and the approved mockup.
/// </summary>
public static class TreemapLayout
{
    /// <summary>
    /// Place the items in <paramref name="frame"/>, the largest first. Items with a size of
    /// zero or less get no tile. The tile areas are in proportion to the sizes.
    /// </summary>
    public static IReadOnlyList<TreemapPlaced<T>> Squarify<T>(
        IReadOnlyList<T> items, Func<T, double> size, TreemapRect frame)
    {
        var output = new List<TreemapPlaced<T>>();
        if (frame.Width <= 0 || frame.Height <= 0) return output;
        var sized = items.Select(i => (Item: i, Size: size(i))).Where(p => p.Size > 0).ToList();
        var total = sized.Sum(p => p.Size);
        if (total <= 0) return output;

        var scale = frame.Area / total;
        var queue = sized
            .Select((p, index) => (p.Item, Area: p.Size * scale, index))
            .OrderByDescending(p => p.Area).ThenBy(p => p.index)
            .Select(p => (p.Item, p.Area))
            .ToList();

        var row = new List<(T Item, double Area)>();
        double rx = frame.X, ry = frame.Y, rw = frame.Width, rh = frame.Height;

        void Flush()
        {
            var s = row.Sum(e => e.Area);
            if (rw >= rh)
            {
                var cw = s / rh;
                var cy = ry;
                foreach (var e in row)
                {
                    var ch = e.Area / cw;
                    output.Add(new(e.Item, new TreemapRect(rx, cy, cw, ch)));
                    cy += ch;
                }
                rx += cw; rw = Math.Max(0, rw - cw);
            }
            else
            {
                var ch = s / rw;
                var cx = rx;
                foreach (var e in row)
                {
                    var cw = e.Area / ch;
                    output.Add(new(e.Item, new TreemapRect(cx, ry, cw, ch)));
                    cx += cw;
                }
                ry += ch; rh = Math.Max(0, rh - ch);
            }
            row.Clear();
        }

        foreach (var it in queue)
        {
            var side = Math.Min(rw, rh);
            if (row.Count == 0 || Worst(row, it.Area, side) <= Worst(row, null, side)) row.Add(it);
            else { Flush(); row.Add(it); }
        }
        if (row.Count > 0) Flush();
        return output;
    }

    // Worst aspect ratio of the row, with an optional extra item.
    private static double Worst<T>(List<(T Item, double Area)> row, double? extra, double side)
    {
        var s = row.Sum(e => e.Area) + (extra ?? 0);
        if (s <= 0 || side <= 0) return double.MaxValue;
        double worst = 0;
        foreach (var a in row.Select(e => e.Area).Concat(extra is double x ? new[] { x } : Array.Empty<double>()))
            worst = Math.Max(worst, Math.Max(side * side * a / (s * s), s * s / (side * side * a)));
        return worst;
    }
}
