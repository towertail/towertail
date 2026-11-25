using Towertail.WinUI.State;

namespace Towertail.WinUI.Cloud;

/// <summary>The hosts picked for Cloud, capped by the room of the plan.</summary>
public sealed class CloudPicks
{
    private readonly HashSet<Guid> _ids;

    /// <summary>The default picks are the first hosts that fit in the room.</summary>
    public CloudPicks(IEnumerable<Node>? hosts = null, int? room = null)
    {
        var list = (hosts ?? []).ToList();
        Room = room;
        _ids = list.Take(room ?? list.Count).Select(n => n.Id).ToHashSet();
    }

    /// <summary>The hosts that the plan can still take. Null means no cap.</summary>
    public int? Room { get; }

    public IReadOnlySet<Guid> Ids => _ids;

    public bool IsFull => _ids.Count >= (Room ?? int.MaxValue);

    public void Toggle(Guid id)
    {
        if (!_ids.Remove(id) && !IsFull) _ids.Add(id);
    }

    public void Clear() => _ids.Clear();

    public string Counter
    {
        get
        {
            var n = _ids.Count;
            return Room is { } room ? $"{n} of {room} Cloud {(room == 1 ? "host" : "hosts")} selected" : $"{n} Cloud {(n == 1 ? "host" : "hosts")} selected";
        }
    }

    /// <summary>The text above the hosts.</summary>
    public static string Note(int? room, string planLabel, bool used)
    {
        const string intro = "Cloud hosts get 24/7 alerts, long history and the web and phone apps.";
        if (room is not { } r) return $"{intro} SSH hosts have no limit.";
        var name = planLabel == "Plan" ? "Your plan" : $"The {planLabel}";
        if (r == 0) return $"{name} is full. Turn off 24/7 monitoring on another host first.";
        var hosts = r == 1 ? "Cloud host" : "Cloud hosts";
        var plan = used ? $"{name} has room for {r} more {hosts}." : $"{name} takes {r} {hosts}.";
        return $"{intro} {plan} SSH hosts have no limit.";
    }
}
