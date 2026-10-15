using System.Globalization;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Design;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Details panel for the HEALTH tab: one row per signal the sampler sent, tinted by the
/// level of its signal. Rows for signals the host does not report are left out.
/// </summary>
public sealed class HealthDetails : UserControl
{
    private readonly StackPanel _rows = new() { Spacing = 6, Padding = new Thickness(16, 12, 16, 12) };
    private ServerViewModel? _vm;

    public HealthDetails()
    {
        Content = new Border
        {
            Background = (Brush)Application.Current.Resources["CardBackgroundFillColorDefaultBrush"],
            BorderBrush = (Brush)Application.Current.Resources["CardStrokeColorDefaultBrush"],
            BorderThickness = new Thickness(1),
            CornerRadius = new CornerRadius(8),
            Child = new ScrollViewer { Content = _rows },
        };
    }

    public void Bind(ServerViewModel vm)
    {
        _vm = vm;
        Refresh();
        vm.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(ServerViewModel.Health))
                DispatcherQueue.TryEnqueue(Refresh);
        };
    }

    private void Refresh()
    {
        if (_vm is null) return;
        _rows.Children.Clear();
        var status = _vm.Health;
        var h = status.Info;
        if (h is null)
        {
            _rows.Children.Add(new TextBlock { Text = "No health data from this host yet.", Opacity = 0.7 });
            return;
        }

        AddRow("Processes", N(h.Procs), status.TintOf(HealthSignal.Procs));
        if (h.Zombies is int z)
        {
            var value = N(z);
            if (h.ZombieParents is { Count: > 0 } ps)
                value += "  —  " + string.Join(", ", ps.Select(p => $"{p.Name} (pid {p.Pid}): {N(p.Count)}"));
            AddRow("Zombies", value, status.TintOf(HealthSignal.Zombies));
        }
        if (h.PidsUsed is long pu && h.PidsMax is long pm)
            AddRow("PIDs", $"{N(pu)} / {N(pm)}", status.TintOf(HealthSignal.Pids));
        if (h.FilesUsed is long fu && h.FilesMax is long fm)
            AddRow("Open files", $"{N(fu)} / {N(fm)}", status.TintOf(HealthSignal.Files));
        if (h.Psi is { } psi)
        {
            AddRow("Memory pressure", $"some {psi.MemSome:0.0}%  ·  full {psi.MemFull:0.0}%", status.TintOf(HealthSignal.MemPressure));
            AddRow("I/O pressure", $"some {psi.IoSome:0.0}%  ·  full {psi.IoFull:0.0}%", status.TintOf(HealthSignal.IoPressure));
            AddRow("CPU pressure", $"some {psi.CpuSome:0.0}%", HealthLevel.Nominal);
        }
        if (h.MemPressure is int mp)
        {
            var label = mp switch { 4 => "critical", 2 => "warn", _ => "normal" };
            AddRow("Memory pressure level", label, status.TintOf(HealthSignal.MemPressure));
        }
        foreach (var d in status.Disks ?? Array.Empty<DiskSample>())
        {
            if (d.InodesUsed is long iu && d.InodesTotal is long it && it > 0)
                AddRow($"Inodes {d.Mount}", $"{N(iu)} / {N(it)} ({(double)iu / it:P0})", HealthStatus.InodeLevel((double)iu / it));
        }
    }

    private void AddRow(string label, string value, HealthLevel level)
    {
        var grid = new Grid { ColumnSpacing = 12 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(180) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(new TextBlock
        {
            Text = label,
            Foreground = (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
        });
        var valueText = new TextBlock { Text = value, TextWrapping = TextWrapping.Wrap };
        if (level != HealthLevel.Nominal)
        {
            valueText.Foreground = ThresholdTint.Brush(
                level == HealthLevel.Critical ? ThresholdTint.Critical : ThresholdTint.Warn);
            valueText.FontWeight = Microsoft.UI.Text.FontWeights.SemiBold;
        }
        Grid.SetColumn(valueText, 1);
        grid.Children.Add(valueText);
        _rows.Children.Add(grid);
    }

    private static string N(long v) => v.ToString("N0", CultureInfo.InvariantCulture);
}
