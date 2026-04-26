using CommunityToolkit.Mvvm.ComponentModel;
using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.State;

/// <summary>
/// Settings owned by the Towertail server in Remote mode (thresholds, polling intervals,
/// notification policy, sampler auto-update, post-wake grace). In Local mode they live
/// on disk alongside ClientSettings and are mutated by the Preferences panes.
/// </summary>
public sealed partial class ServerSettings : ObservableObject
{
    [ObservableProperty] private MetricThresholds _thresholds = MetricThresholds.Defaults;
    [ObservableProperty] private int _localPollingIntervalSeconds = 2;
    [ObservableProperty] private int _sshPollingIntervalSeconds = 10;
    [ObservableProperty] private bool _notificationsEnabled;
    [ObservableProperty] private bool _notifyWarn = true;
    [ObservableProperty] private bool _notifyCritical = true;
    [ObservableProperty] private int _notifyDebounceSeconds = 60;
    [ObservableProperty] private bool _autoUpdateSamplersEnabled;
    [ObservableProperty] private int _postWakeGraceSeconds = 15;

    private readonly string _path;

    public ServerSettings(string path)
    {
        _path = path;
        var p = SettingsPersistence.Load(path);
        ApplyFrom(p);
    }

    public void ReloadFromDisk()
    {
        var p = SettingsPersistence.Load(_path);
        ApplyFrom(p);
    }

    public int PollingInterval(NodeKind kind) => kind switch
    {
        NodeKind.Local => LocalPollingIntervalSeconds,
        NodeKind.Ssh => SshPollingIntervalSeconds,
        _ => LocalPollingIntervalSeconds,
    };

    public void Persist()
    {
        var p = SettingsPersistence.Load(_path);
        p.Thresholds = new PersistedThresholds
        {
            CpuWarn = Thresholds.CpuWarn,
            CpuCritical = Thresholds.CpuCritical,
            MemWarn = Thresholds.MemWarn,
            MemCritical = Thresholds.MemCritical,
            DiskWarn = Thresholds.DiskWarn,
            DiskCritical = Thresholds.DiskCritical,
            CpuSustainSamples = Thresholds.CpuSustainSamples,
            MemSustainSamples = Thresholds.MemSustainSamples,
            DiskSustainSamples = Thresholds.DiskSustainSamples,
        };
        p.LocalPollingIntervalSeconds = LocalPollingIntervalSeconds;
        p.SshPollingIntervalSeconds = SshPollingIntervalSeconds;
        p.NotificationsEnabled = NotificationsEnabled;
        p.NotifyWarn = NotifyWarn;
        p.NotifyCritical = NotifyCritical;
        p.NotifyDebounceSeconds = NotifyDebounceSeconds;
        p.AutoUpdateSamplersEnabled = AutoUpdateSamplersEnabled;
        p.PostWakeGraceSeconds = PostWakeGraceSeconds;
        SettingsPersistence.Save(p, _path);
    }

    private void ApplyFrom(PersistedSettings p)
    {
        Thresholds = new MetricThresholds(
            p.Thresholds.CpuWarn, p.Thresholds.CpuCritical,
            p.Thresholds.MemWarn, p.Thresholds.MemCritical,
            p.Thresholds.DiskWarn, p.Thresholds.DiskCritical,
            Math.Max(1, p.Thresholds.CpuSustainSamples),
            Math.Max(1, p.Thresholds.MemSustainSamples),
            Math.Max(1, p.Thresholds.DiskSustainSamples));
        LocalPollingIntervalSeconds = Math.Clamp(p.LocalPollingIntervalSeconds, 1, 300);
        SshPollingIntervalSeconds = Math.Clamp(p.SshPollingIntervalSeconds, 1, 300);
        NotificationsEnabled = p.NotificationsEnabled;
        NotifyWarn = p.NotifyWarn;
        NotifyCritical = p.NotifyCritical;
        NotifyDebounceSeconds = p.NotifyDebounceSeconds;
        AutoUpdateSamplersEnabled = p.AutoUpdateSamplersEnabled;
        PostWakeGraceSeconds = Math.Clamp(p.PostWakeGraceSeconds, 0, 300);
    }
}
