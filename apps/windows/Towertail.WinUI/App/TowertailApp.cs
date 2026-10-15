using Microsoft.UI.Dispatching;

namespace Towertail.WinUI.Bootstrap;

/// <summary>
/// Process-wide helpers that don't belong on the WinUI <see cref="Microsoft.UI.Xaml.Application"/> instance.
/// Mirrors TowertailApp.swift in the Mac port — a thin wrapper around global state.
/// </summary>
public static class TowertailApp
{
    /// <summary>
    /// UI-thread dispatcher. Captured in <see cref="AppEnvironment.Bootstrap"/> and used by
    /// any component that needs to marshal a change into the UI without taking a parameter
    /// dependency on WinUI. Equivalent to the Mac app's @MainActor capture.
    /// </summary>
    public static DispatcherQueue? MainDispatcher { get; internal set; }

    public const string AppDisplayName = "Towertail";
    public const string SettingsSubdirectory = "Towertail";
    public const string HistoryFileName = "history.sqlite";
    public const string SettingsFileName = "settings.json";
    public const string LogsSubdirectory = "logs";

    /// <summary>
    /// Build the absolute path to a file under the user's <c>%APPDATA%\Towertail</c> directory,
    /// creating the directory lazily.
    /// </summary>
    public static string AppDataPath(string fileName)
    {
        var dir = Path.Combine(
            System.Environment.GetFolderPath(System.Environment.SpecialFolder.ApplicationData),
            SettingsSubdirectory);
        Directory.CreateDirectory(dir);
        return Path.Combine(dir, fileName);
    }

    public static string LocalAppDataPath(string fileName)
    {
        var dir = Path.Combine(
            System.Environment.GetFolderPath(System.Environment.SpecialFolder.LocalApplicationData),
            SettingsSubdirectory);
        Directory.CreateDirectory(dir);
        return Path.Combine(dir, fileName);
    }
}
