using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.Cloud;
using Windows.Graphics;
using WinRT.Interop;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// Shows the local rules that differ from the cloud and moves the picked ones.
/// <see cref="CloudRulesMove"/> drives it. It closes when the move closes.
/// </summary>
public sealed partial class MoveRulesToCloudWindow : Window
{
    private static MoveRulesToCloudWindow? s_open;

    private readonly CloudRulesMove _move;

    /// <summary>Opens the window while the move is presented. The static field keeps it alive.</summary>
    public static void OpenWhenPresented(AppEnvironment env)
    {
        if (!env.RulesMove.IsPresented) return;
        if (s_open is { } open) { open.Activate(); return; }
        s_open = new MoveRulesToCloudWindow(env.RulesMove);
        s_open.Activate();
    }

    private MoveRulesToCloudWindow(CloudRulesMove move)
    {
        _move = move;
        InitializeComponent();
        Title = CloudRulesMove.Title;
        TitleText.Text = CloudRulesMove.Title;
        LoadingText.Text = CloudRulesMove.LoadingText;
        LeadText.Text = CloudRulesMove.Lead;
        FootText.Text = CloudRulesMove.Foot;
        try
        {
            var id = Win32Interop.GetWindowIdFromWindow(WindowNative.GetWindowHandle(this));
            AppWindow.GetFromWindowId(id)?.Resize(new SizeInt32(760, 560));
        }
        catch { }
        _move.Changed += OnChanged;
        Closed += (_, _) =>
        {
            _move.Changed -= OnChanged;
            s_open = null;
            _move.Close();
        };
        Render();
    }

    private void OnChanged() => DispatcherQueue.TryEnqueue(() =>
    {
        if (!_move.IsPresented) { Close(); return; }
        Render();
    });

    private void OnNotNow(object sender, RoutedEventArgs e) => _move.Close();

    private async void OnMoveClick(object sender, RoutedEventArgs e)
    {
        if (_move.Rows.Count == 0) await _move.OpenAsync();
        else await _move.StartAsync();
    }

    private void Render()
    {
        var loading = _move.Phase == RulesMovePhase.Loading;
        LoadingPanel.Visibility = loading ? Visibility.Visible : Visibility.Collapsed;
        ContentPanel.Visibility = loading ? Visibility.Collapsed : Visibility.Visible;
        var sending = _move.Phase == RulesMovePhase.Sending;
        var failedEmpty = _move.Rows.Count == 0 && _move.Phase == RulesMovePhase.Failed;
        LeadText.Visibility = FootText.Visibility = failedEmpty ? Visibility.Collapsed : Visibility.Visible;
        ErrorRow.Visibility = _move.Phase == RulesMovePhase.Failed ? Visibility.Visible : Visibility.Collapsed;
        ErrorText.Text = _move.Error ?? "";
        SendingRow.Visibility = sending ? Visibility.Visible : Visibility.Collapsed;
        SendingText.Text = _move.SendingText;
        NotNowBtn.IsEnabled = !sending;
        if (_move.Rows.Count == 0)
        {
            MoveBtn.Content = "Try again";
            MoveBtn.IsEnabled = !loading;
        }
        else
        {
            MoveBtn.Content = _move.MoveButtonText;
            MoveBtn.IsEnabled = _move.PickedCount > 0 && !sending;
        }
        RenderTable(sending);
    }

    private void RenderTable(bool sending)
    {
        HeaderRow.Children.Clear();
        HeaderRow.ColumnDefinitions.Clear();
        Rows.Children.Clear();
        Rows.Opacity = sending ? 0.6 : 1;
        if (_move.Rows.Count == 0) return;
        Widths(HeaderRow);
        Header("", "Scope", "Rule", "Cloud now", "From this PC");
        foreach (var row in _move.Rows)
        {
            var box = new CheckBox { IsChecked = _move.Picks.Contains(row.Id), MinWidth = 0, IsEnabled = !sending };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(box, $"Move {row.Rule}");
            var id = row.Id;
            box.Click += (_, _) => _move.Toggle(id);
            var cloud = new TextBlock
            {
                Text = row.CloudNow, FontSize = 12, TextTrimming = TextTrimming.CharacterEllipsis,
                TextDecorations = Windows.UI.Text.TextDecorations.Strikethrough,
                Foreground = Res("TextFillColorSecondaryBrush"),
            };
            var pc = new TextBlock
            {
                Text = row.FromPc, FontSize = 12, FontWeight = Microsoft.UI.Text.FontWeights.Medium,
                TextTrimming = TextTrimming.CharacterEllipsis, Foreground = Res("AccentTextFillColorPrimaryBrush"),
            };
            AddRow([box, Cell(row.Scope.Title, true), Cell(row.Rule, false), cloud, pc]);
        }
    }

    /// <summary>The checkbox column is fixed. The others are star columns.</summary>
    private static void Widths(Grid grid)
    {
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(32) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(0.9, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1.6, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1.5, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1.5, GridUnitType.Star) });
    }

    private void Header(params string[] titles)
    {
        for (var i = 0; i < titles.Length; i++)
        {
            var cell = new TextBlock { Text = titles[i], FontSize = 11, FontWeight = Microsoft.UI.Text.FontWeights.Medium, Foreground = Res("TextFillColorSecondaryBrush") };
            Grid.SetColumn(cell, i);
            HeaderRow.Children.Add(cell);
        }
    }

    private void AddRow(FrameworkElement[] cells)
    {
        var grid = new Grid { ColumnSpacing = 14, Padding = new Thickness(12, 7, 12, 7) };
        Widths(grid);
        for (var i = 0; i < cells.Length; i++)
        {
            cells[i].VerticalAlignment = VerticalAlignment.Center;
            Grid.SetColumn(cells[i], i);
            grid.Children.Add(cells[i]);
        }
        Rows.Children.Add(grid);
        Rows.Children.Add(new Border { Height = 1, Background = Res("DividerStrokeColorDefaultBrush") });
    }

    private static TextBlock Cell(string text, bool bold) => new()
    {
        Text = text, FontSize = 12, TextTrimming = TextTrimming.CharacterEllipsis,
        FontWeight = bold ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal,
    };

    private static Brush Res(string key) => (Brush)Application.Current.Resources[key];
}
