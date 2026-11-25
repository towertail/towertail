namespace Towertail.WinUI.State;

/// <summary>A process across time. PIDs change, so the key joins name and command.</summary>
public sealed record ProcessRef(string Key, int? Pid, string Name, string Cmd)
{
    public static ProcessRef Local(ProcSample p) => new(KeyOf(p.Name, p.Cmd), p.Pid, p.Name, p.Cmd ?? "");

    public static string KeyOf(string name, string? cmd) => name + "\0" + (cmd ?? "");
}
