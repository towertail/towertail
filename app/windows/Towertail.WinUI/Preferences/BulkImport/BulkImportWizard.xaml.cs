using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Preferences.BulkImport;

public sealed partial class BulkImportWizard : Window
{
    private AppEnvironment? _env;
    private int _step;
    private BulkImportState _state = new();

    public BulkImportWizard()
    {
        InitializeComponent();
        Title = "Bulk import servers";
        SetStep(0);
    }

    public void Bind(AppEnvironment env)
    {
        _env = env;
    }

    private void OnBack(object sender, RoutedEventArgs e) => SetStep(_step - 1);
    private void OnNext(object sender, RoutedEventArgs e) => SetStep(_step + 1);

    private void SetStep(int next)
    {
        if (_env is null) return;
        _step = Math.Clamp(next, 0, 4);
        Host.Content = _step switch
        {
            0 => new SourcePickerPage(_state, () => SetStep(1)),
            1 => _state.Source switch
            {
                BulkImportSource.Tailscale => (object)new TailscalePickerPage(_state),
                _ => new PastePage(_state),
            },
            2 => new ReviewGridPage(_state),
            3 => new DeployProgressPage(_state, _env),
            _ => new TextBlock { Text = "Done." },
        };
        BackBtn.IsEnabled = _step > 0;
        NextBtn.IsEnabled = _step < 3;
    }
}
