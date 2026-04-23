using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Preferences.BulkImport;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ServersPane : UserControl
{
    private readonly AppEnvironment _env;
    public ServersPane(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        Items.ItemsSource = env.Nodes.Nodes;
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
        if (Items.SelectedItem is not Node n) return;
        var win = new ServerEditWindow();
        win.Bind(n, node =>
        {
            if (node != null) _env.Nodes.Update(node);
        });
        win.Activate();
    }

    private void OnRemoveClick(object sender, RoutedEventArgs e)
    {
        if (Items.SelectedItem is not Node n) return;
        _env.Nodes.Remove(n.Id);
    }

    private void OnBulkClick(object sender, RoutedEventArgs e)
    {
        var win = new BulkImportWizard();
        win.Bind(_env);
        win.Activate();
    }
}
