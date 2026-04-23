using Microsoft.Win32;
using Windows.Networking.Connectivity;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Watches network reachability + sleep/wake/session-switch events so the collector can
/// pause polling during suspend and apply a post-wake grace window before re-notifying.
/// </summary>
public sealed class SystemReachabilityMonitor : IDisposable
{
    public bool ShouldPoll { get; private set; } = true;
    public DateTime? LastResumeAt { get; private set; }

    public event EventHandler? Changed;

    public SystemReachabilityMonitor()
    {
        NetworkInformation.NetworkStatusChanged += OnNetworkChanged;
        SystemEvents.PowerModeChanged += OnPowerChanged;
        SystemEvents.SessionSwitch += OnSessionSwitch;
        Refresh();
    }

    private void OnNetworkChanged(object sender) => Refresh();
    private void OnPowerChanged(object? sender, PowerModeChangedEventArgs e)
    {
        switch (e.Mode)
        {
            case PowerModes.Suspend: ShouldPoll = false; break;
            case PowerModes.Resume: ShouldPoll = true; LastResumeAt = DateTime.UtcNow; break;
        }
        Changed?.Invoke(this, EventArgs.Empty);
    }

    private void OnSessionSwitch(object? sender, SessionSwitchEventArgs e)
    {
        switch (e.Reason)
        {
            case SessionSwitchReason.SessionLock: ShouldPoll = false; break;
            case SessionSwitchReason.SessionUnlock: ShouldPoll = true; break;
        }
        Changed?.Invoke(this, EventArgs.Empty);
    }

    private void Refresh()
    {
        try
        {
            var profile = NetworkInformation.GetInternetConnectionProfile();
            var level = profile?.GetNetworkConnectivityLevel() ?? NetworkConnectivityLevel.None;
            ShouldPoll = level != NetworkConnectivityLevel.None;
            Changed?.Invoke(this, EventArgs.Empty);
        }
        catch { }
    }

    /// <summary>
    /// True while we're still inside the post-wake suppression window. ThresholdNotifier
    /// uses this to skip firing toasts on the first failed post-wake poll.
    /// </summary>
    public bool InPostWakeGrace(int graceSeconds)
    {
        if (LastResumeAt is not DateTime t) return false;
        return (DateTime.UtcNow - t).TotalSeconds < graceSeconds;
    }

    public void Dispose()
    {
        NetworkInformation.NetworkStatusChanged -= OnNetworkChanged;
        SystemEvents.PowerModeChanged -= OnPowerChanged;
        SystemEvents.SessionSwitch -= OnSessionSwitch;
    }
}
