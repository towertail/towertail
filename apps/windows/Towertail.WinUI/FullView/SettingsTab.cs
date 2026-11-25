using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Design;
using Towertail.WinUI.Preferences;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// The Settings tab: the alert settings of one host (thresholds and alert rules). It saves
/// to the same node record as the server edit window. Cloud hosts take their rules from the cloud.
/// </summary>
public sealed class SettingsTab : UserControl
{
    private readonly List<ThresholdOverrideRow> _rows = new();
    private readonly CheckBox _custom = new() { Content = "Customize alerting for this server" };
    private readonly AlertRulesEditor _editor = new() { IsEnabled = false };
    private readonly TextBlock _status = WB.Text("", 12, false, WB.Muted);
    private ServerViewModel? _vm;

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        if (vm.Node.IsCloud)
        {
            Content = WB.Card(WithPadding(WB.Text("Rules of this host come from the cloud. Edit them in the cloud dashboard.", 13, false, WB.Muted)));
            return;
        }
        var env = Towertail.WinUI.App.Current.Environment;
        var panel = new StackPanel { Spacing = 10, MaxWidth = 620, HorizontalAlignment = HorizontalAlignment.Left };
        panel.Children.Add(WB.Text("Thresholds", 14, false, null, true));
        panel.Children.Add(WB.Text("Turn off Use global to set a value for this server only.", 12, false, WB.Muted));
        var header = new Grid { ColumnSpacing = 8 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(120) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(112) });
        var warn = WB.Text("Warn", 12, false, WB.Muted);
        var crit = WB.Text("Critical", 12, false, WB.Muted);
        Grid.SetColumn(warn, 1);
        Grid.SetColumn(crit, 2);
        header.Children.Add(warn);
        header.Children.Add(crit);
        panel.Children.Add(header);
        var global = env.ServerSettings.Thresholds;
        foreach (var m in ThresholdMetrics.Usage)
            AddRow(panel, m, global, vm.Node);
        panel.Children.Add(WB.Text("Health checks", 12, false, WB.Muted));
        foreach (var m in ThresholdMetrics.Health)
            AddRow(panel, m, global, vm.Node);

        panel.Children.Add(WB.Text("Alerting", 14, false, null, true));
        _custom.IsChecked = vm.Node.CustomAlerts != null;
        _custom.Checked += (_, _) => _editor.IsEnabled = true;
        _custom.Unchecked += (_, _) => _editor.IsEnabled = false;
        _editor.Rules = vm.Node.CustomAlerts ?? env.ServerSettings.Alerts;
        _editor.IsEnabled = vm.Node.CustomAlerts != null;
        panel.Children.Add(_custom);
        panel.Children.Add(_editor);

        var save = new Button { Content = "Save", Style = (Style)Application.Current.Resources["AccentButtonStyle"] };
        save.Click += async (_, _) => await SaveAsync();
        var bottom = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 12 };
        bottom.Children.Add(save);
        bottom.Children.Add(_status);
        panel.Children.Add(bottom);

        Content = WB.Card(new ScrollViewer { Content = WithPadding(panel), VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
    }

    private static FrameworkElement WithPadding(UIElement child) => new Grid { Padding = new Thickness(20, 16, 20, 16), Children = { child } };

    private void AddRow(StackPanel panel, ThresholdMetric m, MetricThresholds global, Node node)
    {
        var row = new ThresholdOverrideRow(m);
        row.Bind(global.Get(m), node.ThresholdOverrides?.Get(m));
        _rows.Add(row);
        panel.Children.Add(row);
    }

    private async Task SaveAsync()
    {
        if (_vm is null) return;
        var env = Towertail.WinUI.App.Current.Environment;
        var overrides = _rows.Aggregate(new ThresholdOverrides(), (o, row) => o.With(row.Metric, row.Value));
        var node = env.Nodes.ById(_vm.Node.Id) ?? _vm.Node;
        try
        {
            await env.Backend.UpdateNodeAsync(node with
            {
                ThresholdOverrides = overrides.IsEmpty ? null : overrides,
                CustomAlerts = _custom.IsChecked == true ? _editor.Rules : null,
            });
            _status.Text = "Saved";
        }
        catch (Exception e)
        {
            _status.Text = "Could not save: " + e.Message;
        }
    }
}
