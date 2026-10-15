using System.Collections.ObjectModel;
using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.State;

/// <summary>
/// Mutable, UI-observable collection of <see cref="Node"/>s that persists to settings.json
/// on every mutation. Mirrors NodeStore.swift on the Mac side.
/// </summary>
public sealed partial class NodeStore : ObservableObject
{
    public ObservableCollection<Node> Nodes { get; } = new();
    private readonly string _path;

    public event EventHandler<Guid>? NodeAdded;
    public event EventHandler<Guid>? NodeRemoved;
    public event EventHandler<Guid>? NodeUpdated;

    public NodeStore(string path)
    {
        _path = path;
        ReloadFromDisk();
    }

    public void ReloadFromDisk()
    {
        var p = SettingsPersistence.Load(_path);
        Nodes.Clear();
        foreach (var n in p.Nodes) Nodes.Add(n);
    }

    public Node? ById(Guid id) => Nodes.FirstOrDefault(n => n.Id == id);

    public void Add(Node node)
    {
        Nodes.Add(node);
        PersistAll();
        NodeAdded?.Invoke(this, node.Id);
    }

    public void AddMany(IEnumerable<Node> nodes)
    {
        var added = new List<Guid>();
        foreach (var n in nodes) { Nodes.Add(n); added.Add(n.Id); }
        PersistAll();
        foreach (var id in added) NodeAdded?.Invoke(this, id);
    }

    public void Update(Node updated)
    {
        for (int i = 0; i < Nodes.Count; i++)
        {
            if (Nodes[i].Id == updated.Id)
            {
                Nodes[i] = updated;
                PersistAll();
                NodeUpdated?.Invoke(this, updated.Id);
                return;
            }
        }
    }

    public void SetEnabled(Guid id, bool enabled)
    {
        var n = ById(id);
        if (n == null) return;
        Update(n with { Enabled = enabled });
    }

    public void SetFavorite(Guid id, bool favorite)
    {
        var n = ById(id);
        if (n == null) return;
        Update(n with { Favorite = favorite });
    }

    public void SetSnooze(Guid id, DateTime? until)
    {
        var n = ById(id);
        if (n == null) return;
        Update(n with { SnoozedUntil = until });
    }

    public void Remove(Guid id)
    {
        for (int i = 0; i < Nodes.Count; i++)
        {
            if (Nodes[i].Id == id)
            {
                Nodes.RemoveAt(i);
                PersistAll();
                NodeRemoved?.Invoke(this, id);
                return;
            }
        }
    }

    private void PersistAll()
    {
        var p = SettingsPersistence.Load(_path);
        p.Nodes = Nodes.ToList();
        SettingsPersistence.Save(p, _path);
    }
}
