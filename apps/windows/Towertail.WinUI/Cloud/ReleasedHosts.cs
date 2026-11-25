namespace Towertail.WinUI.Cloud;

/// <summary>
/// The ids of SSH hosts that a sign-out released while the cloud still held them.
/// The feed links these hosts again at the next sign-in. Mirrors the Mac <c>ReleasedHosts</c>.
/// </summary>
public sealed class ReleasedHosts(CloudPrefs prefs)
{
    private readonly object _gate = new();

    public IReadOnlySet<Guid> Ids => prefs.ReleasedHosts;

    public bool Contains(Guid id) => prefs.ReleasedHosts.Contains(id);

    public void Add(Guid id) => AddRange([id]);

    public void AddRange(IEnumerable<Guid> ids)
    {
        lock (_gate)
        {
            var next = prefs.ReleasedHosts.ToHashSet();
            var before = next.Count;
            next.UnionWith(ids);
            if (next.Count != before) Save(next);
        }
    }

    public void Remove(Guid id)
    {
        lock (_gate)
        {
            var next = prefs.ReleasedHosts.ToHashSet();
            if (next.Remove(id)) Save(next);
        }
    }

    /// <summary>Drops every id that is not in <paramref name="keep"/>.</summary>
    public void Retain(IReadOnlySet<Guid> keep)
    {
        lock (_gate)
        {
            var next = prefs.ReleasedHosts.Where(keep.Contains).ToHashSet();
            if (next.Count != prefs.ReleasedHosts.Count) Save(next);
        }
    }

    private void Save(HashSet<Guid> next)
    {
        prefs.ReleasedHosts = next;
        prefs.Save();
    }
}
