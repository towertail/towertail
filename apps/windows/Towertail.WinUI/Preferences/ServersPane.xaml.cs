using System.Collections.ObjectModel;
using System.Collections.Specialized;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Preferences.BulkImport;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ServersPane : UserControl
{
    private readonly AppEnvironment _env;
    private readonly ObservableCollection<ServerListRow> _rows = new();
    private bool _attached;

    public ServersPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        Items.ItemsSource = _rows;
        AutoUpdateCheck.IsChecked = env.ServerSettings.AutoUpdateSamplersEnabled;
        Loaded += (_, _) => Attach();
        Unloaded += (_, _) => { if (!IsLoaded) Detach(); };
    }

    // The stores live as long as the app, so the pane listens only while it is in the tree.
    private void Attach()
    {
        if (_attached) return;
        _env.Nodes.Nodes.CollectionChanged += OnNodesChanged;
        _env.Nodes.NodeUpdated += OnNodeUpdated;
        _env.Cloud.StateChanged += OnCloudChanged;
        _attached = true;
        SyncRows();
        LeaseCloudHosts();
    }

    private void Detach()
    {
        if (!_attached) return;
        _env.Nodes.Nodes.CollectionChanged -= OnNodesChanged;
        _env.Nodes.NodeUpdated -= OnNodeUpdated;
        _env.Cloud.StateChanged -= OnCloudChanged;
        _env.Cloud.Lease.SetListed([]);
        foreach (var row in _rows) row.Dispose();
        _attached = false;
    }

    /// <summary>The cloud sends the data of a host only while a view holds a lease for it.</summary>
    private void LeaseCloudHosts() => _env.Cloud.Lease.SetListed(_env.Nodes.Nodes.Where(n => n.IsCloud).Select(n => n.Id));

    private void OnCloudChanged() => DispatcherQueue.TryEnqueue(() =>
    {
        foreach (var row in _rows) row.RefreshTag();
    });

    private void SyncRows()
    {
        foreach (var row in _rows) row.Dispose();
        _rows.Clear();
        foreach (var n in _env.Nodes.Nodes)
        {
            _rows.Add(new ServerListRow(n, _env.Servers.Find(n.Id), _env.Cloud.TagFor, _env.Cloud.StatusFor));
        }
        if (IsLoaded) LeaseCloudHosts();
    }

    private void OnNodesChanged(object? sender, NotifyCollectionChangedEventArgs e)
    {
        // NodeStore resets on bulk ops; easier to re-sync than diff.
        SyncRows();
    }

    private void OnNodeUpdated(object? sender, Guid id) => UpdateRow(id);

    private void UpdateRow(Guid id)
    {
        var row = _rows.FirstOrDefault(r => r.Node.Id == id);
        var node = _env.Nodes.ById(id);
        if (row is not null && node is not null) row.UpdateNode(node);
    }

    private void OnAutoUpdateChanged(object sender, RoutedEventArgs e)
    {
        _env.ServerSettings.AutoUpdateSamplersEnabled = AutoUpdateCheck.IsChecked == true;
        _env.ServerSettings.Persist();
    }

    private void OnAddClick(object sender, RoutedEventArgs e)
    {
        var win = new ServerEditWindow();
        win.Bind(null, node =>
        {
            if (node != null) _env.Nodes.Add(node);
        });
        win.Activate();
    }

    private void OnEditClick(object sender, RoutedEventArgs e)
    {
        if (Items.SelectedItem is not ServerListRow row) return;
        var win = new ServerEditWindow();
        win.Bind(row.Node, node =>
        {
            if (node != null) _env.Nodes.Update(node);
        });
        win.Activate();
    }

    private void OnRemoveClick(object sender, RoutedEventArgs e)
    {
        if (Items.SelectedItem is not ServerListRow row) return;
        _env.Nodes.Remove(row.Node.Id);
    }

    private void OnBulkClick(object sender, RoutedEventArgs e)
    {
        var win = new BulkImportWizard();
        win.Bind(_env);
        win.Activate();
    }
}
