using System.Text.Json.Serialization;
using System.Text.RegularExpressions;

namespace Towertail.WinUI.State;

/// <summary>
/// Refresh of one folder: the request for the folder scan, and the merge of its result
/// into the stored scan. Same rules as the Mac <c>DiskScanMerge.swift</c>.
/// </summary>
public sealed partial record DiskScanResult
{
    [JsonIgnore]
    public string Separator => IsWindows ? "\\" : "/";

    /// <summary>Absolute path of the node at <paramref name="names"/> (names from the root).</summary>
    public string AbsolutePath(IReadOnlyList<string> names)
    {
        if (names.Count == 0) return Root;
        var rest = string.Join(Separator, names);
        return Root.EndsWith(Separator, StringComparison.Ordinal) ? Root + rest : Root + Separator + rest;
    }

    /// <summary>The path as the recs show it: <c>~</c> (or <c>%USERPROFILE%</c>) below home.</summary>
    public string DisplayPath(string path)
    {
        if (string.IsNullOrEmpty(Home)) return path;
        var tilde = IsWindows ? "%USERPROFILE%" : "~";
        if (path == Home) return tilde;
        if (path.StartsWith(Home + Separator, StringComparison.Ordinal)) return tilde + path[Home.Length..];
        return path;
    }

    /// <summary>
    /// Names from the root to the node at the absolute <paramref name="path"/>, or null when
    /// the tree has no such node. A name can be a chain such as <c>a/b</c>.
    /// </summary>
    public IReadOnlyList<string>? NamesOf(string path)
    {
        if (path == Root) return Array.Empty<string>();
        var sep = Separator[0];
        var prefix = Root.EndsWith(sep) ? Root : Root + sep;
        if (!path.StartsWith(prefix, StringComparison.Ordinal)) return null;
        var segs = path[prefix.Length..].Split(sep, StringSplitOptions.RemoveEmptyEntries);
        var node = Tree;
        var output = new List<string>();
        var i = 0;
        while (i < segs.Length)
        {
            var at = i;
            var hit = node.Children.FirstOrDefault(k =>
            {
                if (k.O) return false;
                var ks = k.N.Split(sep);
                return ks.Length <= segs.Length - at && ks.SequenceEqual(segs.Skip(at).Take(ks.Length));
            });
            if (hit is null) return null;
            output.Add(hit.N);
            i += hit.N.Split(sep).Length;
            node = hit;
        }
        return output;
    }

    /// <summary>The scan request for the folder at <paramref name="names"/>, with the limits of this scan.</summary>
    public DiskScanRequest RefreshRequest(IReadOnlyList<string> names)
    {
        var depth = names.Sum(n => n.Split(Separator[0]).Length);
        var maxDepth = Limits?.MaxDepth ?? 6;
        var minBytes = Limits?.MinBytes ?? Math.Max(1, Math.Min(50_000_000, (long)(0.002 * Tree.B)));
        return new DiskScanRequest(AbsolutePath(names), Math.Max(1, maxDepth - depth), minBytes, MinFrac: 0);
    }

    /// <summary>
    /// Puts <paramref name="sub"/>, the scan of the folder at <paramref name="names"/>, in place
    /// of that folder. The size change goes to each parent. Returns null when the tree has no
    /// such folder. Files and dirs stay the same, because the counts of the old folder are not known.
    /// </summary>
    public DiskScanResult? Merging(DiskScanResult sub, IReadOnlyList<string> names, DateTime now)
    {
        if (Tree.NodeAt(names) is not { } old) return null;
        // A count of the refreshed folders can repeat, because a refresh drops the entries below it.
        var used = Tree.SubtreeIds.Select(id => RefreshTag().Match(id))
            .Where(m => m.Success).Select(m => int.Parse(m.Groups[1].Value)).DefaultIfEmpty(0).Max();
        var tag = $"r{used + 1}-";
        var delta = sub.Tree.B - old.B;
        var kids = sub.Tree.C?.Select(k => k.Retagged(tag, old.Cat)).ToList();
        var target = DisplayPath(AbsolutePath(names));
        var below = old.SubtreeIds;
        if (old.Id != null) below.Remove(old.Id);
        var above = Tree.IdsAlong(names);
        if (old.Id != null) above.Add(old.Id);
        var sizes = old.SizesById();

        var recs = new List<DiskRec>();
        foreach (var r in Recommendations)
        {
            var ids = r.NodeIds ?? Array.Empty<string>();
            if (ids.Count == 0)
            {
                if (!IsIn(r.Path, target, Separator)) recs.Add(r);
                continue;
            }
            var gone = ids.Where(below.Contains).ToHashSet();
            if (gone.Count == ids.Count) continue;
            var rec = r;
            if (gone.Count > 0)
                rec = rec with
                {
                    NodeIds = ids.Where(id => !gone.Contains(id)).ToList(),
                    Bytes = Math.Max(0, rec.Bytes - gone.Sum(id => sizes.GetValueOrDefault(id))),
                };
            if (rec.NodeIds!.Any(above.Contains)) rec = rec with { Bytes = Math.Max(0, rec.Bytes + delta) };
            recs.Add(rec);
        }
        var taken = recs.Select(r => r.Id).ToHashSet();
        foreach (var r in sub.Recommendations)
        {
            if (recs.Any(k => k.RuleId == r.RuleId && k.Path == r.Path)) continue;
            var id = r.Id;
            for (var n = 2; taken.Contains(id); n++) id = $"{r.Id}-{n}";
            taken.Add(id);
            recs.Add(r with { Id = id, NodeIds = (r.NodeIds ?? Array.Empty<string>()).Select(x => tag + x).ToList() });
        }

        var errors = (Errors ?? Array.Empty<string>()).ToList();
        errors.AddRange((sub.Errors ?? Array.Empty<string>()).Where(e => !errors.Contains(e)));
        var abs = AbsolutePath(names);
        var refreshed = RefreshedFolders.Where(f => !IsIn(f.Path, abs, Separator)).ToList();
        refreshed.Add(new DiskScanRefresh(abs, now));

        return this with
        {
            Tree = Tree.Replace(names, 0, kids, delta),
            ScannedBytes = Math.Max(0, ScannedBytes + delta),
            Recs = recs,
            Errors = errors,
            Refreshed = refreshed,
        };
    }

    /// <summary>True when <paramref name="path"/> is <paramref name="folder"/> or below it. A trailing <c>*</c> is ignored.</summary>
    public static bool IsIn(string path, string folder, string separator)
    {
        var p = path.EndsWith(separator + "*", StringComparison.Ordinal) ? path[..^2] : path;
        var prefix = folder.EndsWith(separator, StringComparison.Ordinal) ? folder : folder + separator;
        return p == folder || p.StartsWith(prefix, StringComparison.Ordinal);
    }

    [GeneratedRegex(@"^r(\d+)-")]
    private static partial Regex RefreshTag();
}

public sealed partial record DiskRec
{
    /// <summary>The rule id without the number that the sampler adds to repeats.</summary>
    [JsonIgnore]
    public string RuleId => RepeatSuffix().Replace(Id, "");

    [GeneratedRegex(@"-\d+$")]
    private static partial Regex RepeatSuffix();
}

public sealed partial record DiskNode
{
    public DiskNode? NodeAt(IReadOnlyList<string> names, int from = 0)
    {
        if (from >= names.Count) return this;
        return Children.FirstOrDefault(c => c.N == names[from] && !c.O)?.NodeAt(names, from + 1);
    }

    /// <summary>Ids of the nodes from here to the node at <paramref name="names"/>, without that node.</summary>
    public HashSet<string> IdsAlong(IReadOnlyList<string> names, int from = 0)
    {
        if (from >= names.Count || Children.FirstOrDefault(c => c.N == names[from] && !c.O) is not { } k)
            return new HashSet<string>();
        var output = k.IdsAlong(names, from + 1);
        if (Id != null) output.Add(Id);
        return output;
    }

    /// <summary>The ids of this node and of each node below it.</summary>
    [JsonIgnore]
    public HashSet<string> SubtreeIds
    {
        get
        {
            var output = new HashSet<string>();
            if (Id != null) output.Add(Id);
            foreach (var c in Children) output.UnionWith(c.SubtreeIds);
            return output;
        }
    }

    public Dictionary<string, long> SizesById()
    {
        var output = new Dictionary<string, long>();
        if (Id != null) output[Id] = B;
        foreach (var c in Children)
            foreach (var (k, v) in c.SizesById()) output.TryAdd(k, v);
        return output;
    }

    /// <summary>
    /// A copy that has <paramref name="kids"/> at the node at <paramref name="names"/>, and
    /// <paramref name="delta"/> added to that node and to each node above it. The changed
    /// siblings are sorted again, with "other" last.
    /// </summary>
    public DiskNode Replace(IReadOnlyList<string> names, int from, IReadOnlyList<DiskNode>? kids, long delta)
    {
        var b = Math.Max(0, B + delta);
        if (from >= names.Count) return this with { B = b, C = kids };
        var list = Children.ToList();
        var i = list.FindIndex(c => c.N == names[from] && !c.O);
        if (i < 0) return this;
        list[i] = list[i].Replace(names, from + 1, kids, delta);
        return this with { B = b, C = list.OrderBy(c => c.O ? 1 : 0).ThenByDescending(c => c.B).ToList() };
    }

    /// <summary>A copy with <paramref name="prefix"/> on each id. Below a folder with a known category, "other" takes that category.</summary>
    public DiskNode Retagged(string prefix, string cat) => this with
    {
        Id = Id is null ? null : prefix + Id,
        Cat = Cat == "other" && cat != "none" ? cat : Cat,
        C = C?.Select(c => c.Retagged(prefix, cat)).ToList(),
    };
}
