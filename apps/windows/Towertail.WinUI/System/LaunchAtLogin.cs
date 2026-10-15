using Microsoft.Win32;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Toggle "launch at Windows sign-in" for the unpackaged build via the per-user
/// <c>HKCU\Software\Microsoft\Windows\CurrentVersion\Run</c> key. The packaged MSIX path
/// uses <c>StartupTask.RequestEnableAsync</c> — we fall back to registry when running unpackaged.
/// </summary>
public static class LaunchAtLogin
{
    private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string ValueName = "Towertail";

    public static void Apply(bool enable)
    {
        try
        {
            using var key = Registry.CurrentUser.CreateSubKey(RunKey, writable: true);
            if (key is null) return;
            if (enable)
            {
                var exe = Environment.ProcessPath ?? "";
                if (!string.IsNullOrEmpty(exe)) key.SetValue(ValueName, $"\"{exe}\"");
            }
            else
            {
                key.DeleteValue(ValueName, throwOnMissingValue: false);
            }
        }
        catch { /* best-effort */ }
    }

    public static bool IsEnabled()
    {
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(RunKey);
            return key?.GetValue(ValueName) is not null;
        }
        catch
        {
            return false;
        }
    }
}
