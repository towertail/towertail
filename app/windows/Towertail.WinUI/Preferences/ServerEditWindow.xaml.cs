using Microsoft.UI.Xaml;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences;

public sealed partial class ServerEditWindow : Window
{
    private Node? _existing;
    private Action<Node?>? _onDone;

    public ServerEditWindow() { InitializeComponent(); Title = "Server"; }

    public void Bind(Node? n, Action<Node?> onDone)
    {
        _existing = n;
        _onDone = onDone;
        DisplayNameBox.Text = n?.DisplayName ?? "";
        KindBox.SelectedIndex = n?.Kind == NodeKind.Ssh ? 1 : 0;
        UserBox.Text = n?.SshUser ?? "";
        HostBox.Text = n?.SshHost ?? "";
        EnabledCheck.IsChecked = n?.Enabled ?? true;
        FavoriteCheck.IsChecked = n?.Favorite ?? false;
    }

    private void OnCancel(object sender, RoutedEventArgs e) { _onDone?.Invoke(null); Close(); }

    private void OnOk(object sender, RoutedEventArgs e)
    {
        var node = new Node
        {
            Id = _existing?.Id ?? Guid.NewGuid(),
            DisplayName = DisplayNameBox.Text,
            Kind = KindBox.SelectedIndex == 1 ? NodeKind.Ssh : NodeKind.Local,
            SshUser = UserBox.Text,
            SshHost = HostBox.Text,
            Enabled = EnabledCheck.IsChecked == true,
            Favorite = FavoriteCheck.IsChecked == true,
        };
        _onDone?.Invoke(node);
        Close();
    }
}
