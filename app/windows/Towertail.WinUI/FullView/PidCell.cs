using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI;
using Windows.UI;

namespace Towertail.WinUI.FullView;

/// <summary>
/// Event args for <see cref="PidCell.Killed"/>. Carries enough context for
/// the parent table to drive a confirmation dialog without doing its own
/// row lookup.
/// </summary>
public sealed class PidKillEventArgs : EventArgs
{
    public int Pid { get; }
    public string Name { get; }
    public PidKillEventArgs(int pid, string name) { Pid = pid; Name = name; }
}

/// <summary>
/// PID cell with a hover-revealed kill button. Mirrors the Mac
/// <c>PIDCell</c>: PID text always visible, a small X icon fades in on
/// pointer enter and disappears on exit. Kept as a code-only
/// <see cref="UserControl"/> so both <see cref="ProcessTable"/> and
/// <see cref="PortsTable"/> can drop it into their <c>x:Bind</c>
/// templates with one line.
/// </summary>
public sealed class PidCell : UserControl
{
    public static readonly DependencyProperty PidProperty =
        DependencyProperty.Register(nameof(Pid), typeof(int), typeof(PidCell),
            new PropertyMetadata(0, (d, _) => ((PidCell)d).RefreshPid()));

    // `Name` overlaps FrameworkElement.Name (string-keyed lookup), but the
    // semantics here are "process display name", and DPs are looked up by
    // type so the overlap is harmless. `new` suppresses the warning.
    public static new readonly DependencyProperty NameProperty =
        DependencyProperty.Register(nameof(Name), typeof(string), typeof(PidCell),
            new PropertyMetadata("", (d, _) => ((PidCell)d).RefreshTooltip()));

    public static readonly DependencyProperty CanKillProperty =
        DependencyProperty.Register(nameof(CanKill), typeof(bool), typeof(PidCell),
            new PropertyMetadata(true, (d, _) => ((PidCell)d).RefreshKillVisibility()));

    public int Pid
    {
        get => (int)GetValue(PidProperty);
        set => SetValue(PidProperty, value);
    }

    public new string Name
    {
        get => (string)GetValue(NameProperty);
        set => SetValue(NameProperty, value);
    }

    public bool CanKill
    {
        get => (bool)GetValue(CanKillProperty);
        set => SetValue(CanKillProperty, value);
    }

    /// <summary>Raised when the user clicks the kill button. The parent
    /// table is responsible for showing the confirmation dialog.</summary>
    public event EventHandler<PidKillEventArgs>? Killed;

    private readonly TextBlock _pidText;
    private readonly Button _killButton;
    private bool _hovering;

    public PidCell()
    {
        var stack = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4 };
        _pidText = new TextBlock
        {
            FontFamily = (FontFamily)Microsoft.UI.Xaml.Application.Current.Resources["MonoFont"],
            VerticalAlignment = VerticalAlignment.Center,
        };
        _killButton = new Button
        {
            Content = new FontIcon
            {
                Glyph = "", // Cancel/X glyph from Segoe Fluent Icons
                FontSize = 12,
                Foreground = new SolidColorBrush(Color.FromArgb(0xFF, 0xE8, 0x11, 0x23)),
            },
            Padding = new Thickness(4, 0, 4, 0),
            MinWidth = 0,
            MinHeight = 0,
            Background = new SolidColorBrush(Colors.Transparent),
            BorderBrush = new SolidColorBrush(Colors.Transparent),
            Opacity = 0,
            IsHitTestVisible = false,
        };
        ToolTipService.SetToolTip(_killButton, "Kill process");
        _killButton.Click += (_, _) =>
        {
            Killed?.Invoke(this, new PidKillEventArgs(Pid, Name));
        };

        stack.Children.Add(_pidText);
        stack.Children.Add(_killButton);
        Content = stack;

        PointerEntered += (_, _) => { _hovering = true; RefreshKillVisibility(); };
        PointerExited += (_, _) => { _hovering = false; RefreshKillVisibility(); };

        RefreshPid();
        RefreshTooltip();
        RefreshKillVisibility();
    }

    private void RefreshPid() => _pidText.Text = Pid.ToString();

    private void RefreshTooltip()
        => ToolTipService.SetToolTip(_killButton, $"Kill PID {Pid} ({Name})");

    private void RefreshKillVisibility()
    {
        var show = CanKill && _hovering;
        _killButton.Opacity = show ? 1.0 : 0.0;
        _killButton.IsHitTestVisible = show;
    }
}
