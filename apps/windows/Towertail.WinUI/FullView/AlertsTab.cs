using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Shapes;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>The Alerts tab: why the host is at its level now. Cloud hosts keep their alerts in the cloud.</summary>
public sealed class AlertsTab : UserControl
{
    private readonly StackPanel _rows = new() { Spacing = 10, Padding = new Thickness(16, 12, 16, 12) };
    private ServerViewModel? _vm;
    private bool _queued;

    public AlertsTab()
    {
        Content = WB.Card(new ScrollViewer { Content = _rows, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
    }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        vm.PropertyChanged += (_, a) =>
        {
            if (a.PropertyName is nameof(ServerViewModel.LastSeen) or nameof(ServerViewModel.Health) or nameof(ServerViewModel.HealthScore))
                Queue();
        };
        Loaded += (_, _) => Refresh();
        Refresh();
    }

    private void Queue()
    {
        if (_queued) return;
        _queued = true;
        DispatcherQueue.TryEnqueue(() => { _queued = false; Refresh(); });
    }

    private void Refresh()
    {
        if (_vm is null) return;
        if (!IsLoaded) return;
        _rows.Children.Clear();
        if (_vm.Node.IsCloud)
        {
            _rows.Children.Add(WB.Text("Alerts of this host come from the cloud. Open the cloud dashboard to see them.", 13, false, WB.Muted));
            return;
        }
        var dark = WB.IsDark(this);
        var count = 0;
        void Add(HealthLevel level, string title, string text)
        {
            count++;
            var row = new Grid { ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(160) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            var dot = new Ellipse { Width = 10, Height = 10, Fill = WB.Brush(WB.Level(level, dark)), VerticalAlignment = VerticalAlignment.Center };
            var t = WB.Text(title, 13, false, null, true);
            var d = WB.Text(text, 12, true, WB.Muted);
            d.TextWrapping = TextWrapping.Wrap;
            Grid.SetColumn(t, 1);
            Grid.SetColumn(d, 2);
            row.Children.Add(dot);
            row.Children.Add(t);
            row.Children.Add(d);
            _rows.Children.Add(row);
        }

        var limits = _vm.Thresholds;
        foreach (var (m, tm, value) in new[]
        {
            (AlertMetric.Cpu, ThresholdMetric.Cpu, _vm.CpuPct), (AlertMetric.Mem, ThresholdMetric.Mem, _vm.MemPct), (AlertMetric.Disk, ThresholdMetric.Disk, _vm.DiskMaxPct),
        })
        {
            var raw = _vm.RawLevel(m);
            if (raw == HealthLevel.Nominal || _vm.Muted.Mutes(m)) continue;
            var pair = limits.Get(tm);
            var sustained = _vm.AlertLevel(m);
            Add(raw, tm.Title(), $"{value:0}% now · warn {pair.Warn:P0} · critical {pair.Critical:P0}"
                + (sustained == HealthLevel.Nominal ? $" · waiting {_vm.AlertRules.For(m).SustainSeconds} s before it alerts" : ""));
        }
        foreach (var r in _vm.Health.Unmuted(_vm.Muted).Where(r => r.Signal != HealthSignal.LongProc))
            Add(r.Level, r.Signal.ToString(), r.Text);
        foreach (var lp in _vm.LongProcs)
            Add(HealthLevel.Warn, "Long-running process", $"{lp.ShortName} pid {lp.Pid} · {lp.CpuPct:0}% CPU for {ShortDuration.Text(lp.HighFor(DateTime.UtcNow))}");
        if (_vm.Node.SnoozedUntil is { } until && until > DateTime.UtcNow)
            Add(HealthLevel.Nominal, "Snoozed", $"Notifications are off until {until.ToLocalTime():HH:mm}");
        if (count == 0) Add(HealthLevel.Nominal, "All clear", "No threshold is above its warn level.");
    }
}
