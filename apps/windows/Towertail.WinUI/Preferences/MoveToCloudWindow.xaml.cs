using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Cloud;
using Towertail.WinUI.State;
using Windows.Graphics;
using WinRT.Interop;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// Picks SSH hosts and moves them to the cloud, then shows each host while the install runs.
/// <see cref="CloudMove"/> drives it. It closes when the move closes.
/// </summary>
public sealed partial class MoveToCloudWindow : Window
{
    private static MoveToCloudWindow? s_open;

    private readonly AppEnvironment _env;
    private CloudMove Move => _env.Move;

    /// <summary>Opens the window while the move is presented. The static field keeps it alive.</summary>
    public static void OpenWhenPresented(AppEnvironment env)
    {
        if (!env.Move.IsPresented) return;
        if (s_open is { } open) { open.Activate(); return; }
        s_open = new MoveToCloudWindow(env);
        s_open.Activate();
    }

    private MoveToCloudWindow(AppEnvironment env)
    {
        _env = env;
        InitializeComponent();
        Title = "Move hosts to Cloud";
        try
        {
            var id = Win32Interop.GetWindowIdFromWindow(WindowNative.GetWindowHandle(this));
            AppWindow.GetFromWindowId(id)?.Resize(new SizeInt32(760, 600));
        }
        catch { }
        Move.Changed += OnChanged;
        Move.Installer.Changed += OnChanged;
        Closed += (_, _) =>
        {
            Move.Changed -= OnChanged;
            Move.Installer.Changed -= OnChanged;
            s_open = null;
            Move.Close();
        };
        Render();
    }

    private void OnChanged() => DispatcherQueue.TryEnqueue(() =>
    {
        if (!Move.IsPresented) { Close(); return; }
        Render();
    });

    private void OnNotNow(object sender, RoutedEventArgs e) => Move.Close();

    private async void OnMoveClick(object sender, RoutedEventArgs e)
    {
        if (Move.Phase == MovePhase.Install) { Move.Close(); return; }
        await Move.StartAsync();
    }

    private void Render()
    {
        Rows.Children.Clear();
        HeaderRow.Children.Clear();
        HeaderRow.ColumnDefinitions.Clear();
        var picking = Move.Phase == MovePhase.Pick;
        if (picking) RenderPick(); else RenderInstall();
    }

    private void RenderPick()
    {
        var room = Move.Picks.Room;
        NoteText.Text = AccountText.MovePickText(_env.Cloud.Account.Billing, room, Move.Installer.PlanLabel);
        Columns(36, 0, 0, 0);
        Header("", "Host", "Address", "OS");
        foreach (var node in Move.Hosts)
        {
            var on = Move.Picks.Ids.Contains(node.Id);
            var box = new CheckBox { IsChecked = on, MinWidth = 0, IsEnabled = on || !Move.Picks.IsFull };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(box, $"Move {node.DisplayName}");
            box.Click += (_, _) => Move.Toggle(node.Id);
            AddRow([box, Bold(node.DisplayName), Muted(node.SshHost ?? ""), Muted(_env.Servers.Find(node.Id)?.HostOs ?? "—")]);
        }
        FootText.Text = AccountText.MovePickFoot;
        NotNowBtn.Visibility = Visibility.Visible;
        var n = Move.Picked.Count;
        MoveBtn.Content = AccountText.MoveButton(n);
        MoveBtn.IsEnabled = n > 0;
    }

    private void RenderInstall()
    {
        NoteText.Text = AccountText.MoveInstallNote;
        Columns(0, 0, 0);
        Header("Host", "Address", "State");
        foreach (var node in Move.Picked)
            AddRow([Bold(node.DisplayName), Muted(node.SshHost ?? ""), State(Move.Installer.LineOf(node.Id))]);
        FootText.Text = AccountText.MoveStayText(Move.NotPicked);
        FootText.Visibility = Move.NotPicked > 0 ? Visibility.Visible : Visibility.Collapsed;
        NotNowBtn.Visibility = Visibility.Collapsed;
        MoveBtn.Content = "Done";
        MoveBtn.IsEnabled = true;
    }

    /// <summary>A width of 0 is a star column. The first column of the pick table is fixed.</summary>
    private void Columns(params double[] widths)
    {
        foreach (var w in widths)
            HeaderRow.ColumnDefinitions.Add(new ColumnDefinition { Width = w == 0 ? new GridLength(1, GridUnitType.Star) : new GridLength(w) });
    }

    private void Header(params string[] titles)
    {
        for (var i = 0; i < titles.Length; i++)
        {
            var cell = new TextBlock
            {
                Text = titles[i],
                FontSize = 12,
                FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                Foreground = (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
            };
            Grid.SetColumn(cell, i);
            HeaderRow.Children.Add(cell);
        }
    }

    private void AddRow(UIElement[] cells)
    {
        var grid = new Grid { ColumnSpacing = 10, Padding = new Thickness(10, 9, 10, 9) };
        foreach (var def in HeaderRow.ColumnDefinitions)
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = def.Width });
        for (var i = 0; i < cells.Length; i++)
        {
            cells[i].SetValue(FrameworkElement.VerticalAlignmentProperty, VerticalAlignment.Center);
            Grid.SetColumn((FrameworkElement)cells[i], i);
            grid.Children.Add(cells[i]);
        }
        Rows.Children.Add(grid);
        Rows.Children.Add(new Border { Height = 1, Background = (Brush)Application.Current.Resources["DividerStrokeColorDefaultBrush"] });
    }

    private static TextBlock Bold(string text) => new() { Text = text, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextTrimming = TextTrimming.CharacterEllipsis };

    private static TextBlock Muted(string text) => new()
    {
        Text = text,
        TextTrimming = TextTrimming.CharacterEllipsis,
        Foreground = (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
    };

    private static UIElement State(InstallLine line)
    {
        var panel = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        var tint = line.Kind switch
        {
            InstallLineKind.Done => "SystemFillColorSuccessBrush",
            InstallLineKind.Failed => "SystemFillColorCriticalBrush",
            _ => "TextFillColorSecondaryBrush",
        };
        if (line.Kind == InstallLineKind.Running)
            panel.Children.Add(new ProgressRing { Width = 14, Height = 14, IsActive = true, VerticalAlignment = VerticalAlignment.Center });
        else
            panel.Children.Add(new Microsoft.UI.Xaml.Shapes.Ellipse
            {
                Width = 8,
                Height = 8,
                VerticalAlignment = VerticalAlignment.Center,
                Fill = (Brush)Application.Current.Resources[line.Kind == InstallLineKind.Waiting || line.Kind == InstallLineKind.Stayed ? "TextFillColorTertiaryBrush" : tint],
            });
        panel.Children.Add(new TextBlock
        {
            Text = line.Text,
            TextWrapping = TextWrapping.Wrap,
            Foreground = line.Kind == InstallLineKind.Failed ? (Brush)Application.Current.Resources[tint] : (Brush)Application.Current.Resources["TextFillColorPrimaryBrush"],
        });
        return panel;
    }
}
