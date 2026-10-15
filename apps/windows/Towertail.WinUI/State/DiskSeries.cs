using System.Collections.ObjectModel;

namespace Towertail.WinUI.State;

/// <summary>
/// Per-mount disk capacity time series and per-device disk I/O series.
/// </summary>
public sealed class DiskSeries
{
    public ObservableCollection<MountRow> Mounts { get; } = new();
    public ObservableCollection<DeviceIoRow> Devices { get; } = new();

    public void AppendCapacity(DateTime t, IReadOnlyList<DiskSample> mounts)
    {
        foreach (var d in mounts)
        {
            var frac = d.Total > 0 ? Math.Clamp((double)d.Used / d.Total, 0, 1) : 0;
            var existing = FindMount(d.Mount);
            if (existing == null)
            {
                existing = new MountRow(d.Mount, d.Fs, d.Used, d.Total, frac);
                Mounts.Add(existing);
            }
            existing.Update(d.Used, d.Total, frac);
        }
    }

    public void AppendIo(DateTime t, IReadOnlyList<DiskIoDevice>? devices)
    {
        if (devices == null) return;
        foreach (var d in devices)
        {
            var existing = FindDevice(d.Name);
            if (existing == null)
            {
                existing = new DeviceIoRow(d.Name, d.ReadBps / 1_048_576.0, d.WriteBps / 1_048_576.0);
                Devices.Add(existing);
            }
            existing.Update(d.ReadBps / 1_048_576.0, d.WriteBps / 1_048_576.0);
        }
    }

    private MountRow? FindMount(string mount)
    {
        for (int i = 0; i < Mounts.Count; i++)
            if (Mounts[i].Mount == mount) return Mounts[i];
        return null;
    }

    private DeviceIoRow? FindDevice(string name)
    {
        for (int i = 0; i < Devices.Count; i++)
            if (Devices[i].Name == name) return Devices[i];
        return null;
    }
}

public sealed class MountRow
{
    public string Mount { get; }
    public string Fs { get; }
    public long Used { get; private set; }
    public long Total { get; private set; }
    public double Frac { get; private set; }

    public MountRow(string mount, string fs, long used, long total, double frac)
    {
        Mount = mount; Fs = fs; Used = used; Total = total; Frac = frac;
    }

    public void Update(long used, long total, double frac)
    {
        Used = used; Total = total; Frac = frac;
    }
}

public sealed class DeviceIoRow
{
    public string Name { get; }
    public double ReadMBps { get; private set; }
    public double WriteMBps { get; private set; }

    public DeviceIoRow(string name, double r, double w) { Name = name; ReadMBps = r; WriteMBps = w; }
    public void Update(double r, double w) { ReadMBps = r; WriteMBps = w; }
}
