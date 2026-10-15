using System.ComponentModel;
using System.Runtime.CompilerServices;
using Towertail.WinUI.State;

namespace Towertail.WinUI.Preferences.BulkImport;

/// <summary>
/// One candidate node as it moves through the bulk-import wizard. Mutable and
/// observable so the Review grid can edit fields inline.
/// Mirrors ImportRow.swift on the Mac.
/// </summary>
public sealed class ImportRow : INotifyPropertyChanged
{
    public enum RowStatus
    {
        Pending,
        Testing,
        Ok,
        Failed,
        Duplicate,
    }

    public event PropertyChangedEventHandler? PropertyChanged;

    public Guid Id { get; } = Guid.NewGuid();

    private bool _included = true;
    public bool Included
    {
        get => _included;
        set { if (_included != value) { _included = value; Notify(); Notify(nameof(Opacity)); } }
    }

    private string _displayName = "";
    public string DisplayName
    {
        get => _displayName;
        set { if (_displayName != value) { _displayName = value; Notify(); } }
    }

    private string _sshHost = "";
    public string SshHost
    {
        get => _sshHost;
        set { if (_sshHost != value) { _sshHost = value; Notify(); } }
    }

    private string _sshUser = "";
    public string SshUser
    {
        get => _sshUser;
        set { if (_sshUser != value) { _sshUser = value; Notify(); } }
    }

    private NodeKind _kind = NodeKind.Ssh;
    public NodeKind Kind
    {
        get => _kind;
        set { if (_kind != value) { _kind = value; Notify(); Notify(nameof(KindIndex)); Notify(nameof(IsSsh)); } }
    }

    /// <summary>Index for the ComboBox binding (0=SSH, 1=Local).</summary>
    public int KindIndex
    {
        get => _kind == NodeKind.Local ? 1 : 0;
        set
        {
            var k = value == 1 ? NodeKind.Local : NodeKind.Ssh;
            Kind = k;
        }
    }

    public bool IsSsh => _kind == NodeKind.Ssh;

    private AuthMethod _authMethod = AuthMethod.Key;
    public AuthMethod AuthMethod
    {
        get => _authMethod;
        set { if (_authMethod != value) { _authMethod = value; Notify(); Notify(nameof(AuthMethodIndex)); Notify(nameof(IsPasswordAuth)); } }
    }

    /// <summary>Index for the ComboBox binding (0=Key, 1=Password).</summary>
    public int AuthMethodIndex
    {
        get => _authMethod == AuthMethod.Password ? 1 : 0;
        set => AuthMethod = value == 1 ? AuthMethod.Password : AuthMethod.Key;
    }

    public bool IsPasswordAuth => _authMethod == AuthMethod.Password;

    /// <summary>Plaintext in-memory only; written to DPAPI at deploy time.</summary>
    private string _password = "";
    public string Password
    {
        get => _password;
        set { if (_password != value) { _password = value; Notify(); } }
    }

    private string _tagsText = "";
    public string TagsText
    {
        get => _tagsText;
        set { if (_tagsText != value) { _tagsText = value; Notify(); } }
    }

    private RowStatus _status = RowStatus.Pending;
    public RowStatus Status
    {
        get => _status;
        set { if (_status != value) { _status = value; Notify(); Notify(nameof(StatusLabel)); Notify(nameof(StatusColorKey)); } }
    }

    private string _statusDetail = "";
    public string StatusDetail
    {
        get => _statusDetail;
        set { if (_statusDetail != value) { _statusDetail = value; Notify(); Notify(nameof(StatusLabel)); } }
    }

    public bool IsDuplicate { get; set; }

    /// <summary>Captured during Test so the post-deploy connect skips TOFU.</summary>
    public string? KnownHostFingerprint { get; set; }

    public double Opacity => _included ? 1.0 : 0.55;

    public string StatusLabel => _status switch
    {
        RowStatus.Pending when IsDuplicate => "Already added",
        RowStatus.Pending when !IsValid() => _kind == NodeKind.Ssh ? "Missing SSH user/host" : "Missing name",
        RowStatus.Pending => "—",
        RowStatus.Testing => "testing…",
        RowStatus.Ok => string.IsNullOrEmpty(_statusDetail) ? "ok" : _statusDetail,
        RowStatus.Failed => string.IsNullOrEmpty(_statusDetail) ? "failed" : _statusDetail,
        RowStatus.Duplicate => "Already added",
        _ => "—",
    };

    public string StatusColorKey => _status switch
    {
        RowStatus.Ok => "SystemFillColorSuccessBrush",
        RowStatus.Failed => "SystemFillColorCriticalBrush",
        RowStatus.Duplicate => "SystemFillColorCautionBrush",
        RowStatus.Pending when IsDuplicate => "SystemFillColorCautionBrush",
        RowStatus.Pending when !IsValid() => "SystemFillColorCautionBrush",
        _ => "TextFillColorSecondaryBrush",
    };

    public bool IsValid()
    {
        if (string.IsNullOrWhiteSpace(_displayName)) return false;
        if (_kind == NodeKind.Local) return true;
        return !string.IsNullOrWhiteSpace(_sshHost) && !string.IsNullOrWhiteSpace(_sshUser);
    }

    public Node ToNode(bool enabled = true) => new()
    {
        Id = Id,
        DisplayName = _displayName.Trim(),
        Kind = _kind,
        SshUser = _kind == NodeKind.Ssh ? _sshUser.Trim() : null,
        SshHost = _kind == NodeKind.Ssh ? _sshHost.Trim() : null,
        AuthMethod = _kind == NodeKind.Ssh ? _authMethod : AuthMethod.Key,
        KnownHostFingerprint = _kind == NodeKind.Ssh ? KnownHostFingerprint : null,
        Tags = _tagsText
            .Split(',', StringSplitOptions.RemoveEmptyEntries)
            .Select(s => s.Trim())
            .Where(s => s.Length > 0)
            .ToList(),
        Enabled = enabled,
    };

    public static ImportRow FromNode(Node n) => new()
    {
        _displayName = n.DisplayName,
        _sshHost = n.SshHost ?? "",
        _sshUser = n.SshUser ?? "",
        _kind = n.Kind,
        _tagsText = string.Join(", ", n.Tags ?? Array.Empty<string>()),
    };

    private void Notify([CallerMemberName] string? name = null)
        => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name ?? ""));
}
