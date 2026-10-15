using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Towertail.WinUI.Preferences;

public sealed class AboutPane : UserControl
{
    public AboutPane()
    {
        var panel = new StackPanel { Spacing = 4 };
        panel.Children.Add(new TextBlock
        {
            Text = "Towertail",
            Style = (Style)Application.Current.Resources["TitleTextBlockStyle"],
        });
        panel.Children.Add(new TextBlock
        {
            Text = "Version 0.1.0",
            Style = (Style)Application.Current.Resources["BodyTextBlockStyle"],
            Foreground = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["TextFillColorSecondaryBrush"],
        });
        panel.Children.Add(new TextBlock
        {
            Text = "Monitor a fleet of servers over SSH from your Windows menu bar.",
            Style = (Style)Application.Current.Resources["BodyTextBlockStyle"],
            Margin = new Thickness(0, 12, 0, 0),
            TextWrapping = TextWrapping.Wrap,
        });
        Content = panel;
    }
}
