using Towertail.WinUI.SystemServices;

namespace Towertail.WinUI.Cloud;

/// <summary>Keeps the cloud user token. A key is the endpoint host[:port]. Tests use a fake.</summary>
public interface ITokenVault
{
    string? Get(string key);
    void Set(string key, string token);
    void Delete(string key);
}

public abstract record CloudStatus
{
    public sealed record SignedOut : CloudStatus;
    public sealed record CodeSent(string Email, DateTime At) : CloudStatus;
    public sealed record SignedIn(CloudMe Me) : CloudStatus;
    public sealed record Failed(string Message) : CloudStatus;
}

/// <summary>
/// The cloud account of the user: the sign-in with an email code, the user token and the plan.
/// Mirrors the Mac <c>CloudAccount</c>. <see cref="StatusChanged"/> runs on the thread that made the change.
/// </summary>
public sealed class CloudAccount
{
    public static readonly TimeSpan ResendEvery = TimeSpan.FromSeconds(60);
    public static readonly TimeSpan RetryEvery = TimeSpan.FromSeconds(30);

    private readonly Uri _endpoint;
    private readonly ITokenVault _vault;
    private readonly CloudAuthClient _client;
    private readonly CloudPrefs _prefs;
    private readonly Func<DateTime> _now;
    private readonly TimeSpan _retryDelay;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly Logger? _log;
    private readonly object _gate = new();
    private DateTime? _lastSentAt;
    private CancellationTokenSource? _retry;

    public CloudAccount(
        Uri endpoint, ITokenVault vault, CloudAuthClient client, CloudPrefs prefs,
        Func<DateTime>? now = null, TimeSpan? retryDelay = null,
        Func<TimeSpan, CancellationToken, Task>? delay = null, Logger? log = null)
    {
        _endpoint = endpoint;
        _vault = vault;
        _client = client;
        _prefs = prefs;
        _now = now ?? (() => DateTime.UtcNow);
        _retryDelay = retryDelay ?? RetryEvery;
        _delay = delay ?? Task.Delay;
        _log = log;
    }

    public event Action<CloudStatus>? StatusChanged;

    public Uri Endpoint => _endpoint;
    public CloudStatus Status { get; private set; } = new CloudStatus.SignedOut();
    public CloudMe? Me => (Status as CloudStatus.SignedIn)?.Me;
    public CloudBilling? Billing { get; private set; }
    public string? Token { get; private set; }

    /// <summary>The email that got the last code. The code field is for this email.</summary>
    public string? PendingEmail { get; private set; }

    public bool IsSignedIn => Status is CloudStatus.SignedIn;

    /// <summary>The vault key: the API host, with the port when it is not the default.</summary>
    public string TokenKey => _endpoint.IsDefaultPort ? _endpoint.Host : $"{_endpoint.Host}:{_endpoint.Port}";

    /// <summary>The web dashboard: <c>api.</c> in the host becomes <c>app.</c>.</summary>
    public Uri DashboardUri
    {
        get
        {
            if (!_endpoint.Host.StartsWith("api.", StringComparison.OrdinalIgnoreCase)) return _endpoint;
            return new UriBuilder(_endpoint) { Host = "app." + _endpoint.Host[4..] }.Uri;
        }
    }

    /// <summary>Time until the user can ask for a new code. Zero means now.</summary>
    public TimeSpan ResendWait
    {
        get
        {
            if (_lastSentAt is not { } sent) return TimeSpan.Zero;
            var left = ResendEvery - (_now() - sent);
            return left > TimeSpan.Zero ? left : TimeSpan.Zero;
        }
    }

    public async Task SendCodeAsync(string email, CancellationToken ct = default)
    {
        email = email.Trim();
        if (email == PendingEmail && ResendWait > TimeSpan.Zero) return;
        try
        {
            await _client.SendCodeAsync(email, ct).ConfigureAwait(false);
        }
        catch (CloudAuthException ex) { Set(new CloudStatus.Failed(ex.Message)); return; }
        PendingEmail = email;
        _lastSentAt = _now();
        _prefs.Email = email;
        _prefs.Save();
        Set(new CloudStatus.CodeSent(email, _now()));
    }

    public async Task VerifyAsync(string code, CancellationToken ct = default)
    {
        if (PendingEmail is not { } email) return;
        string token;
        try
        {
            token = await _client.VerifyAsync(email, code.Trim(), ct).ConfigureAwait(false);
            _vault.Set(TokenKey, token);
        }
        catch (Exception ex) when (ex is not OperationCanceledException) { Set(new CloudStatus.Failed(ex.Message)); return; }
        Token = token;
        PendingEmail = null;
        _lastSentAt = null;
        await LoadAsync(token, ct).ConfigureAwait(false);
    }

    /// <summary>
    /// Runs at launch. A 401 from the cloud signs out. Other errors keep the token and
    /// try again after 30 s.
    /// </summary>
    public Task RestoreAsync(CancellationToken ct = default)
    {
        CancelRetry();
        return AttemptRestoreAsync(ct);
    }

    private async Task AttemptRestoreAsync(CancellationToken ct)
    {
        string? stored;
        try { stored = _vault.Get(TokenKey); }
        catch (Exception ex) { _log?.Warning($"cloud: cannot read the token: {ex.Message}"); return; }
        if (string.IsNullOrEmpty(stored)) return;
        Token = stored;
        await LoadAsync(stored, ct).ConfigureAwait(false);
    }

    public Task SignOutAsync()
    {
        SignOut();
        return Task.CompletedTask;
    }

    /// <summary>Any 401 from the cloud ends the sign-in.</summary>
    public void HandleUnauthorized()
    {
        if (Token is null && Status is CloudStatus.SignedOut) return;
        SignOut();
    }

    /// <summary>Reads the plan again. It keeps the old value on an error.</summary>
    public async Task<CloudBilling?> RefreshAsync(CancellationToken ct = default)
    {
        if (Token is not { } token) return Billing;
        try { if (await _client.BillingAsync(token, ct).ConfigureAwait(false) is { } fresh) Billing = fresh; }
        catch (CloudAuthException ex) when (ex.Kind == CloudAuthErrorKind.Unauthorized) { SignOut(); }
        catch (CloudAuthException) { }
        return Billing;
    }

    /// <summary>
    /// The dashboard link for <paramref name="path"/>. With a token it is a one-time link that
    /// signs the browser in. On any error it is the plain URL.
    /// </summary>
    public async Task<Uri> OpenDashboardUriAsync(string path = "", CancellationToken ct = default)
    {
        var plain = path.Length == 0 ? DashboardUri : new Uri(DashboardUri, path.TrimStart('/'));
        if (Token is not { } token) return plain;
        try
        {
            return await _client.HandoffAsync(token, path.Length == 0 ? "" : "/" + path.TrimStart('/'), ct).ConfigureAwait(false);
        }
        catch (CloudAuthException ex) when (ex.Kind == CloudAuthErrorKind.Unauthorized) { SignOut(); return plain; }
        catch (CloudAuthException) { return plain; }
    }

    /// <summary>What an account delete does to each org. Null when this cloud cannot delete accounts.</summary>
    public async Task<CloudDeletion?> DeletionAsync(CancellationToken ct = default)
    {
        try { return await _client.DeletionAsync(Token ?? throw NotSignedIn(), ct).ConfigureAwait(false); }
        catch (CloudAuthException ex) when (ex.Kind == CloudAuthErrorKind.Unauthorized) { SignOut(); throw; }
    }

    /// <summary>Deletes the account in the cloud, then signs out. An error keeps the sign-in.</summary>
    public async Task DeleteAccountAsync(string email, CancellationToken ct = default)
    {
        try { await _client.DeleteAccountAsync(Token ?? throw NotSignedIn(), email, ct).ConfigureAwait(false); }
        catch (CloudAuthException ex) when (ex.Kind == CloudAuthErrorKind.Unauthorized) { SignOut(); throw; }
        SignOut();
    }

    private static InvalidOperationException NotSignedIn() => new("Not signed in to Towertail Cloud.");

    private async Task LoadAsync(string token, CancellationToken ct)
    {
        try
        {
            var me = await _client.MeAsync(token, ct).ConfigureAwait(false);
            try { Billing = await _client.BillingAsync(token, ct).ConfigureAwait(false); }
            catch (CloudAuthException) { Billing = null; }
            Set(new CloudStatus.SignedIn(me));
        }
        catch (CloudAuthException ex) when (ex.Kind == CloudAuthErrorKind.Unauthorized)
        {
            SignOut();
        }
        catch (CloudAuthException ex)
        {
            Set(new CloudStatus.Failed(ex.Message));
            ScheduleRetry();
        }
    }

    private void SignOut()
    {
        CancelRetry();
        try { _vault.Delete(TokenKey); }
        catch (Exception ex) { _log?.Warning($"cloud: cannot delete the token: {ex.Message}"); }
        Token = null;
        Billing = null;
        PendingEmail = null;
        _lastSentAt = null;
        Set(new CloudStatus.SignedOut());
    }

    private void ScheduleRetry()
    {
        var cts = new CancellationTokenSource();
        lock (_gate) { _retry?.Cancel(); _retry = cts; }
        _ = RetryAsync(cts.Token);
    }

    private async Task RetryAsync(CancellationToken ct)
    {
        try { await _delay(_retryDelay, ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { return; }
        if (ct.IsCancellationRequested) return;
        try { await AttemptRestoreAsync(ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { }
        catch (Exception ex) { _log?.Warning($"cloud: restore failed: {ex.Message}"); }
    }

    private void CancelRetry()
    {
        lock (_gate) { _retry?.Cancel(); _retry = null; }
    }

    private void Set(CloudStatus next)
    {
        Status = next;
        StatusChanged?.Invoke(next);
    }
}
