using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Towertail.WinUI.Collectors;
using Towertail.WinUI.State;

namespace Towertail.WinUI.FullView;

public sealed partial class ProcessTable : UserControl
{
    private ServerViewModel? _vm;
    private bool _killInFlight;

    public ProcessTable() { InitializeComponent(); }

    public async void Bind(ServerViewModel vm)
    {
        _vm = vm;
        var env = Towertail.WinUI.App.Current.Environment;
        await env.Servers.EnsureProcsHydratedAsync(vm.Node.Id);
        Refresh();
        // Redraw on every new VM property event — Procs append happens inside Ingest which raises
        // ObservableObject.PropertyChanged for LastSeen, so using that as a cheap tick works.
        vm.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(ServerViewModel.LastSeen))
                DispatcherQueue.TryEnqueue(Refresh);
        };
    }

    private void Refresh()
    {
        if (_vm is null) return;
        var latest = _vm.Procs.Latest;
        var canKill = _vm.Node is not null;
        Items.ItemsSource = (latest?.Items ?? Array.Empty<ProcSample>())
            .Select(p => new ProcessRow(
                Pid: p.Pid,
                Ppid: p.Ppid ?? 0,
                Name: p.Name ?? "—",
                User: p.User ?? "—",
                CpuPct: p.CpuPct,
                Rss: p.Rss,
                Threads: p.Threads ?? 0,
                CanKill: canKill))
            .ToList();
    }

    private async void OnKillRequested(object sender, PidKillEventArgs e)
    {
        if (_vm is null || _killInFlight) return;
        var node = _vm.Node;
        await KillFlow.RunAsync(this, node, e.Pid, e.Name,
            inFlight => _killInFlight = inFlight);
    }
}

/// <summary>
/// Display projection over <see cref="ProcSample"/>. Public so the XAML
/// compiler's <c>x:Bind</c> generator can resolve property accessors.
/// </summary>
public sealed record ProcessRow(
    int Pid,
    int Ppid,
    string Name,
    string User,
    double CpuPct,
    long Rss,
    int Threads,
    bool CanKill);
