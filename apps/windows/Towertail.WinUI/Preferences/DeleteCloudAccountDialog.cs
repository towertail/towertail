using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Cloud;

namespace Towertail.WinUI.Preferences;

/// <summary>Shows what an account delete does to each org, then asks for the email. Mirrors the Mac sheet.</summary>
public static class DeleteCloudAccountDialog
{
    public static async Task ShowAsync(XamlRoot root, CloudAccount account)
    {
        CloudDeletion? deletion = null;
        var busy = false;
        var body = new StackPanel { Spacing = 14, Width = 420 };
        var error = new TextBlock
        {
            TextWrapping = TextWrapping.Wrap,
            Foreground = Brush("SystemFillColorCriticalBrush"),
            Visibility = Visibility.Collapsed,
        };
        var typed = new TextBox();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(typed, "Email");
        var dialog = new ContentDialog
        {
            XamlRoot = root,
            Title = "Delete Cloud account",
            Content = body,
            PrimaryButtonText = "Delete account",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
            IsPrimaryButtonEnabled = false,
            PrimaryButtonStyle = CriticalButtonStyle(),
        };

        void Update() => dialog.IsPrimaryButtonEnabled = AccountText.CanDelete(deletion, typed.Text, busy);
        typed.TextChanged += (_, _) => Update();

        void ShowError(string message)
        {
            error.Text = message;
            error.Visibility = Visibility.Visible;
        }

        dialog.Opened += async (_, _) =>
        {
            body.Children.Add(new ProgressRing { IsActive = true, Width = 20, Height = 20, HorizontalAlignment = HorizontalAlignment.Left });
            try { deletion = await account.DeletionAsync(); }
            catch (CloudAuthException ex) { body.Children.Clear(); ShowError(ex.Message); body.Children.Add(error); return; }
            body.Children.Clear();
            if (deletion is null)
            {
                body.Children.Add(Muted("This Cloud cannot delete accounts."));
                return;
            }
            body.Children.Add(Muted("The delete removes your Cloud account and its history. The app moves your hosts back to SSH first."));
            body.Children.Add(OrgList(deletion));
            body.Children.Add(Muted("Cloud blocks the delete when you are the only owner of a shared org, or when an org has an active subscription.", small: true));
            var confirm = new StackPanel { Spacing = 4 };
            confirm.Children.Add(new TextBlock { Text = $"Type {deletion.Email} to confirm" });
            confirm.Children.Add(typed);
            body.Children.Add(confirm);
            body.Children.Add(error);
            Update();
        };

        dialog.PrimaryButtonClick += async (_, args) =>
        {
            var deferral = args.GetDeferral();
            busy = true;
            Update();
            dialog.CloseButtonText = "";
            try
            {
                await account.DeleteAccountAsync(typed.Text.Trim());
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                args.Cancel = true;
                ShowError(ex.Message);
            }
            finally
            {
                busy = false;
                dialog.CloseButtonText = "Cancel";
                Update();
                deferral.Complete();
            }
        };

        await dialog.ShowAsync();
    }

    private static UIElement OrgList(CloudDeletion deletion)
    {
        var list = new StackPanel
        {
            Background = Brush("CardBackgroundFillColorDefaultBrush"),
            BorderBrush = Brush("CardStrokeColorDefaultBrush"),
            BorderThickness = new Thickness(1),
            CornerRadius = new CornerRadius(8),
        };
        foreach (var org in deletion.Orgs)
        {
            var tint = org.Fate == "leave" ? Brush("TextFillColorSecondaryBrush") : Brush("SystemFillColorCriticalBrush");
            var text = new StackPanel { Spacing = 2 };
            text.Children.Add(new TextBlock { Text = org.Name, FontWeight = FontWeights.Medium });
            text.Children.Add(new TextBlock { Text = AccountText.DeleteDetail(org), FontSize = 11, TextWrapping = TextWrapping.Wrap, Foreground = org.Fate == "blocked" ? tint : Brush("TextFillColorSecondaryBrush") });
            var badge = new Border
            {
                CornerRadius = new CornerRadius(9),
                BorderThickness = new Thickness(1),
                BorderBrush = tint,
                Padding = new Thickness(6, 1, 6, 1),
                VerticalAlignment = VerticalAlignment.Top,
                Child = new TextBlock { Text = AccountText.DeleteBadge(org), FontSize = 11, Foreground = tint },
            };
            var row = new Grid { Padding = new Thickness(12, 8, 12, 8), ColumnSpacing = 10 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.Children.Add(text);
            Grid.SetColumn(badge, 1);
            row.Children.Add(badge);
            list.Children.Add(row);
        }
        return list;
    }

    private static TextBlock Muted(string text, bool small = false) => new()
    {
        Text = text,
        TextWrapping = TextWrapping.Wrap,
        FontSize = small ? 12 : 14,
        Foreground = Brush("TextFillColorSecondaryBrush"),
    };

    private static Brush Brush(string key) => (Brush)Application.Current.Resources[key];

    private static Style CriticalButtonStyle()
    {
        var style = new Style(typeof(Button)) { BasedOn = (Style)Application.Current.Resources["DefaultButtonStyle"] };
        style.Setters.Add(new Setter(Control.BackgroundProperty, Brush("SystemFillColorCriticalBrush")));
        style.Setters.Add(new Setter(Control.ForegroundProperty, Brush("TextOnAccentFillColorPrimaryBrush")));
        return style;
    }
}
