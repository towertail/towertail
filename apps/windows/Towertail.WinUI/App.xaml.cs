using Microsoft.UI.Xaml;
using Microsoft.Windows.AppLifecycle;
using Towertail.WinUI.Bootstrap;
using Towertail.WinUI.MenuBar;
using Towertail.WinUI.Preferences;
using Towertail.WinUI.State;

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
            var msg = $"[App.UnhandledException] {e.Exception}";
            System.Diagnostics.Debug.WriteLine(msg);
            try
            {
                var path = System.Environment.GetFolderPath(System.Environment.SpecialFolder.LocalApplicationData);
                var file = System.IO.Path.Combine(path, "Towertail", "fatal.log");
                System.IO.Directory.CreateDirectory(System.IO.Path.GetDirectoryName(file)!);
                System.IO.File.AppendAllText(file, $"{DateTime.UtcNow:o} {msg}\n");
            }
            catch { }
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
            // Run the async redirect on the thread pool and block this (non-UI)
            // launch thread until it completes — sync-over-async is fine here
            // because we exit immediately after.
            Task.Run(async () =>
                await keyInstance.RedirectActivationToAsync(AppInstance.GetCurrent().GetActivatedEventArgs())
            ).GetAwaiter().GetResult();
            System.Environment.Exit(0);
            return;
        }

        Environment = AppEnvironment.Bootstrap();
        Environment.Move.Changed += OpenMoveWindow;

        _tray = new TrayIconHost(Environment);
        _tray.Show();
#if DEBUG
        OpenForDebug();
#endif

        // No main window shown — we live in the tray. A hidden window is created
        // on demand via TrayPopoverWindow when the user clicks the tray icon.
    }

    private bool _moveWindowQueued;

    /// <summary>Opens the move window when the move says so. A change while the window is open gives no new window.</summary>
    private void OpenMoveWindow()
    {
        if (_moveWindowQueued || !Environment.Move.IsPresented) return;
        _moveWindowQueued = true;
        TowertailApp.MainDispatcher?.TryEnqueue(() =>
        {
            _moveWindowQueued = false;
            MoveToCloudWindow.OpenWhenPresented(Environment);
        });
    }

#if DEBUG
    private PreferencesWindow? _debugPrefs;

    /// <summary>Asks for a code. <c>TOWERTAIL_DEBUG_CODE</c> then signs in with it.</summary>
    private async Task SignInForDebugAsync(string email)
    {
        await Environment.Cloud.Account.SendCodeAsync(email);
        if (System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_CODE") is { Length: > 0 } code)
            await Environment.Cloud.Account.VerifyAsync(code);
    }

    private async Task OpenMoveForDebugAsync(bool start)
    {
        for (var i = 0; i < 100 && !Environment.Cloud.Account.IsSignedIn; i++) await Task.Delay(100);
        await Environment.Move.OpenAsync();
        if (start) await Environment.Move.StartAsync();
    }

    /// <summary>Opens a window at launch, for renders: <c>TOWERTAIL_DEBUG_OPEN</c> is <c>account</c>, <c>servers</c>, <c>thresholds</c>, <c>serveredit</c> or <c>popover</c>. <c>TOWERTAIL_DEBUG_EMAIL</c> asks for a sign-in code.</summary>
    private void OpenForDebug()
    {
        if (System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_EMAIL") is { Length: > 0 } email)
            _ = SignInForDebugAsync(email);
        switch (System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_OPEN"))
        {
            case "account":
            case "servers":
            case "thresholds":
                _debugPrefs = new PreferencesWindow();
                _debugPrefs.SelectPane(System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_OPEN")!);
                _debugPrefs.Activate();
                break;
            case "serveredit":
                var edit = new ServerEditWindow();
                edit.Bind(Environment.Nodes.Nodes[0] with
                {
                    ThresholdOverrides = new ThresholdOverrides(Mem: new ThresholdPair(0.6, 0.8), Zombies: new ThresholdPair(50, 500), MemPressure: new ThresholdPair(30, 50)),
                }, _ => { });
                edit.Activate();
                break;
            case "popover":
                TestPinPopover = true;
                _tray?.ShowPopover();
                break;
            case "move":
                // Waits for the sign-in, then opens the move window. "install" also starts the move.
                _ = OpenMoveForDebugAsync(System.Environment.GetEnvironmentVariable("TOWERTAIL_DEBUG_MOVE") == "install");
                break;
        }
    }
#endif
}
