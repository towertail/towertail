using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Text;
using Towertail.WinUI.Cloud;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// Six digit cells for the code of the sign-in email. <see cref="Completed"/> runs at the last digit.
/// Call <see cref="Clear"/> after a wrong code.
/// </summary>
public sealed partial class CodeEntryBox : UserControl
{
    private readonly Microsoft.UI.Xaml.Shapes.Rectangle[] _underlines = new Microsoft.UI.Xaml.Shapes.Rectangle[AccountText.CodeLength];
    private readonly TextBlock[] _digits = new TextBlock[AccountText.CodeLength];

    public event Action<string>? Completed;

    public CodeEntryBox()
    {
        InitializeComponent();
        for (var i = 0; i < _underlines.Length; i++)
        {
            _digits[i] = new TextBlock
            {
                FontFamily = new FontFamily("Cascadia Mono, Consolas"),
                FontSize = 22,
                FontWeight = FontWeights.SemiBold,
                HorizontalAlignment = HorizontalAlignment.Center,
                VerticalAlignment = VerticalAlignment.Center,
            };
            _underlines[i] = new Microsoft.UI.Xaml.Shapes.Rectangle
            {
                Height = 2,
                VerticalAlignment = VerticalAlignment.Bottom,
                Margin = new Thickness(1, 0, 1, 0),
            };
            var cell = new Grid();
            cell.Children.Add(_digits[i]);
            cell.Children.Add(_underlines[i]);
            Cells.Children.Add(new Border
            {
                Width = 40,
                Height = 48,
                CornerRadius = new CornerRadius(4),
                BorderThickness = new Thickness(1),
                BorderBrush = (Brush)Application.Current.Resources["ControlStrokeColorDefaultBrush"],
                Background = (Brush)Application.Current.Resources["ControlFillColorDefaultBrush"],
                Child = cell,
                Opacity = 1,
            });
        }
        Loaded += (_, _) => { Field.Focus(FocusState.Programmatic); Draw(); };
        IsEnabledChanged += (_, _) => Draw();
        Draw();
    }

    public void Clear()
    {
        Field.Text = "";
        Field.Focus(FocusState.Programmatic);
    }

    private void OnCellsTapped(object sender, TappedRoutedEventArgs e) => Field.Focus(FocusState.Programmatic);

    private void OnFocusChanged(object sender, RoutedEventArgs e) => Draw();

    private void OnTextChanged(object sender, TextChangedEventArgs e)
    {
        var clean = AccountText.Digits(Field.Text);
        if (clean != Field.Text)
        {
            Field.Text = clean;
            Field.SelectionStart = clean.Length;
            return;
        }
        Draw();
        if (clean.Length == AccountText.CodeLength) Completed?.Invoke(clean);
    }

    private void Draw()
    {
        var text = Field.Text;
        var focused = Field.FocusState != FocusState.Unfocused;
        var accent = (Brush)Application.Current.Resources["AccentFillColorDefaultBrush"];
        var faint = (Brush)Application.Current.Resources["TextFillColorTertiaryBrush"];
        var primary = (Brush)Application.Current.Resources["TextFillColorPrimaryBrush"];
        var active = Math.Min(text.Length, AccountText.CodeLength - 1);
        Cells.Opacity = IsEnabled ? 1 : 0.5;
        for (var i = 0; i < _underlines.Length; i++)
        {
            var filled = i < text.Length;
            _digits[i].Text = filled ? text[i].ToString() : "·";
            _digits[i].Foreground = filled ? primary : faint;
            _underlines[i].Fill = filled || (focused && i == active) ? accent : faint;
        }
    }
}
