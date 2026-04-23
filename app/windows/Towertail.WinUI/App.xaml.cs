using Microsoft.UI.Xaml;
using Microsoft.Windows.AppLifecycle;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.MenuBar;

namespace Towertail.WinUI;

public partial class App : Application
{
    public static new App Current => (App)Application.Current;

    public AppEnvironment Environment { get; private set; } = null!;
    private TrayIconHost? _tray;

    /// <summary>
    /// Set by the <c>--test-pin-popover</c> command line flag. When true, the
    /// tray popover stays open across focus changes so FlaUI (UIA3) can
    /// inspect it. Production builds should never set this.
    /// </summary>
    public static bool TestPinPopover { get; private set; }

    public App()
    {
        InitializeComponent();
        UnhandledException += (_, e) =>
        {
            System.Diagnostics.Debug.WriteLine($"[App.UnhandledException] {e.Exception}");
            e.Handled = true;
        };
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        var cli = System.Environment.GetCommandLineArgs();
        TestPinPopover = Array.Exists(cli, a => string.Equals(a, "--test-pin-popover", StringComparison.OrdinalIgnoreCase));

        // Enforce single-instance: AppInstance.FindOrRegisterForKey + redirect if not primary.
        var keyInstance = AppInstance.FindOrRegisterForKey("towertail-app");
        if (!keyInstance.IsCurrent)
        {
            // A prior instance already exists; hand off activation and exit.
            keyInstance.RedirectActivationToAsync(AppInstance.GetCurrent().GetActivatedEventArgs()).AsTask().Wait();
            System.Environment.Exit(0);
            return;
        }

        Environment = AppEnvironment.Bootstrap();

        _tray = new TrayIconHost(Environment);
        _tray.Show();

        // No main window shown — we live in the tray. A hidden window is created
        // on demand via TrayPopoverWindow when the user clicks the tray icon.
    }
}
