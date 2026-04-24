using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.State;
using Windows.Graphics;
using WinRT.Interop;

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
        ResizeInitial(1040, 620);
        _state.CandidatesChanged += OnCandidatesChanged;
        SetStep(0);
    }

    public void Bind(AppEnvironment env)
    {
        _env = env;
        SetStep(0);
    }

    private void ResizeInitial(int w, int h)
    {
        try
        {
            var hwnd = WindowNative.GetWindowHandle(this);
            var id = Win32Interop.GetWindowIdFromWindow(hwnd);
            AppWindow.GetFromWindowId(id)?.Resize(new SizeInt32(w, h));
        }
        catch { }
    }

    private void OnBack(object sender, RoutedEventArgs e) => SetStep(_step - 1);
    private void OnNext(object sender, RoutedEventArgs e)
    {
        if (_step == 3) { Close(); return; }
        SetStep(_step + 1);
    }
    private void OnCancel(object sender, RoutedEventArgs e) => Close();

    private void OnCandidatesChanged()
    {
        // Only step 1 (fetch) gates Next on candidates being non-empty.
        if (_step == 1) NextBtn.IsEnabled = _state.Candidates.Count > 0;
    }

    private IntPtr WindowHwnd()
    {
        try { return WindowNative.GetWindowHandle(this); }
        catch { return IntPtr.Zero; }
    }

    private void SetStep(int next)
    {
        if (_env is null) return;
        _step = Math.Clamp(next, 0, 3);
        Host.Content = _step switch
        {
            0 => new SourcePickerPage(_state, () => SetStep(1)),
            1 => _state.Source switch
            {
                BulkImportSource.Tailscale => (object)new TailscalePickerPage(_state),
                BulkImportSource.Csv => new CsvPage(_state, WindowHwnd()),
                _ => new PastePage(_state),
            },
            2 => new ReviewGridPage(_state, _env),
            _ => new DeployProgressPage(_state, _env),
        };
        BackBtn.IsEnabled = _step > 0 && _step < 3;
        NextBtn.Content = _step switch
        {
            2 => "Deploy",
            3 => "Done",
            _ => "Next",
        };
        NextBtn.IsEnabled = _step switch
        {
            0 => false,                              // advance via tile click
            1 => _state.Candidates.Count > 0,        // require at least one candidate
            _ => true,
        };
        UpdateStepper();
    }

    private void UpdateStepper()
    {
        var dots = new[] { Step1Dot, Step2Dot, Step3Dot, Step4Dot };
        var labels = new[] { Step1Label, Step2Label, Step3Label, Step4Label };
        var accent = (Brush)Application.Current.Resources["AccentFillColorDefaultBrush"];
        var inactive = (Brush)Application.Current.Resources["ControlFillColorSecondaryBrush"];
        var primaryText = (Brush)Application.Current.Resources["TextFillColorPrimaryBrush"];
        var secondaryText = (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"];
        for (int i = 0; i < dots.Length; i++)
        {
            var active = i == _step;
            dots[i].Background = active ? accent : inactive;
            if (dots[i].Child is TextBlock tb)
            {
                tb.Foreground = active ? new SolidColorBrush(Microsoft.UI.Colors.White) : secondaryText;
            }
            labels[i].Foreground = active ? primaryText : secondaryText;
        }
        Step2Label.Text = _state.Source switch
        {
            BulkImportSource.Tailscale => "Tailscale",
            BulkImportSource.Csv => "CSV / TSV",
            BulkImportSource.Paste => "Paste",
            _ => "Fetch",
        };
    }
}
