using Serilog;
using Serilog.Events;

namespace Towertail.WinUI.SystemServices;

/// <summary>
/// Thin wrapper over Serilog so callers don't take a direct dep. Writes to
/// <c>%LOCALAPPDATA%\Towertail\logs\towertail-.log</c> with daily rollover.
/// </summary>
public sealed class Logger
{
    private readonly Serilog.ILogger _log;

    private Logger(Serilog.ILogger log) { _log = log; }

    public static Logger Create()
    {
        var dir = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "Towertail", "logs");
        Directory.CreateDirectory(dir);
        var path = Path.Combine(dir, "towertail-.log");
        var log = new LoggerConfiguration()
            .MinimumLevel.Information()
            .WriteTo.File(path,
                rollingInterval: RollingInterval.Day,
                retainedFileCountLimit: 7,
                outputTemplate: "{Timestamp:yyyy-MM-dd HH:mm:ss.fff} [{Level:u3}] {Message:lj}{NewLine}{Exception}")
            .CreateLogger();
        return new Logger(log);
    }

    public void Info(string message) => _log.Information(message);
    public void Warning(string message) => _log.Warning(message);
    public void Error(string message, Exception? ex = null)
    {
        if (ex != null) _log.Error(ex, message);
        else _log.Error(message);
    }
}
