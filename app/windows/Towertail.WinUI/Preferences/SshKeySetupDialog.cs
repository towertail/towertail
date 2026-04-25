using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Windows.ApplicationModel.DataTransfer;

namespace Towertail.WinUI.Preferences;

/// <summary>
/// "Need help setting up SSH keys?" popover. Pure copy-paste guide — the app
/// never runs anything. Detects whether <c>%USERPROFILE%\.ssh\id_ed25519</c>
/// already exists and skips the generation step when it does. Commands target
/// PowerShell since there's no <c>ssh-copy-id</c> on stock Windows.
/// </summary>
public static class SshKeySetupDialog
{
    public static async Task ShowAsync(XamlRoot root, string? user = null, string? host = null)
    {
        var dialog = Build(root, user, host);
        await dialog.ShowAsync();
    }

    private static ContentDialog Build(XamlRoot root, string? user, string? host)
    {
        var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
        var ed25519Path = Path.Combine(home, ".ssh", "id_ed25519");
        var hasKey = File.Exists(ed25519Path);

        var userAtHost = !string.IsNullOrEmpty(user) && !string.IsNullOrEmpty(host)
            ? $"{user}@{host}"
            : "USER@HOST";

        var panel = new StackPanel { Spacing = 14 };

        var intro = new TextBlock
        {
            TextWrapping = TextWrapping.Wrap,
            Text = hasKey
                ? "You already have an ed25519 key. Use the command below to install it on the remote host."
                : "Towertail couldn't find an ed25519 key. Run these commands in PowerShell — Towertail will pick up the key automatically next time it connects.",
        };
        panel.Children.Add(intro);

        int step = 1;
        if (!hasKey)
        {
            panel.Children.Add(StepBlock(
                step++,
                "Generate a new ed25519 key (press Enter to accept defaults).",
                "ssh-keygen -t ed25519"));
        }

        panel.Children.Add(StepBlock(
            step++,
            "Copy the public key to the remote host's authorized_keys. " +
            "Windows has no ssh-copy-id, so we pipe it through ssh.",
            $"Get-Content $env:USERPROFILE\\.ssh\\id_ed25519.pub | " +
            $"ssh {userAtHost} \"mkdir -p ~/.ssh && " +
            $"cat >> ~/.ssh/authorized_keys && " +
            $"chmod 600 ~/.ssh/authorized_keys\""));

        panel.Children.Add(StepBlock(
            step++,
            "Verify: this should log in without a password prompt.",
            $"ssh {userAtHost} \"echo ok\""));

        if (hasKey)
        {
            var note = new TextBlock
            {
                TextWrapping = TextWrapping.Wrap,
                Opacity = 0.7,
                Text = $"Your existing key is at {ed25519Path}. " +
                       "If that host still rejects the key, verify the user has write access to ~/.ssh and that sshd_config allows pubkey auth.",
            };
            panel.Children.Add(note);
        }

        return new ContentDialog
        {
            Title = "SSH key setup",
            Content = new ScrollViewer { Content = panel, VerticalScrollBarVisibility = ScrollBarVisibility.Auto },
            CloseButtonText = "Done",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = root,
            MinWidth = 560,
        };
    }

    private static StackPanel StepBlock(int num, string description, string command)
    {
        var sp = new StackPanel { Spacing = 4 };

        sp.Children.Add(new TextBlock
        {
            Text = $"Step {num}. {description}",
            TextWrapping = TextWrapping.Wrap,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
        });

        var cmdGrid = new Grid { ColumnSpacing = 8 };
        cmdGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        cmdGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

        var box = new TextBox
        {
            Text = command,
            IsReadOnly = true,
            TextWrapping = TextWrapping.Wrap,
            FontFamily = new FontFamily("Consolas"),
            FontSize = 12,
        };
        Grid.SetColumn(box, 0);
        cmdGrid.Children.Add(box);

        var copy = new Button { Content = "Copy" };
        copy.Click += (_, _) =>
        {
            var dp = new DataPackage();
            dp.SetText(command);
            Clipboard.SetContent(dp);
            copy.Content = "Copied";
        };
        Grid.SetColumn(copy, 1);
        cmdGrid.Children.Add(copy);

        sp.Children.Add(cmdGrid);
        return sp;
    }
}
