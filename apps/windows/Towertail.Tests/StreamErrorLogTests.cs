using System.Net.WebSockets;
using FluentAssertions;
using Towertail.WinUI.Backend;
using Xunit;

namespace Towertail.Tests;

public sealed class StreamErrorLogTests
{
    private static Exception Error(string message) => new WebSocketException(WebSocketError.ConnectionClosedPrematurely, message);

    [Fact]
    public void LogsSameErrorOnceUntilConnect()
    {
        var log = new StreamErrorLog();
        log.ShouldLog(Error("a")).Should().BeTrue();
        log.ShouldLog(Error("b")).Should().BeFalse();
        log.ShouldLog(Error("c")).Should().BeFalse();
        log.Reset();
        log.ShouldLog(Error("d")).Should().BeTrue();
    }

    [Fact]
    public void NewErrorLogsAtOnce()
    {
        var log = new StreamErrorLog();
        log.ShouldLog(Error("a")).Should().BeTrue();
        log.ShouldLog(new HttpRequestException("refused")).Should().BeTrue();
        log.ShouldLog(Error("a")).Should().BeTrue();
    }

    [Fact]
    public void TextHasTheType()
        => StreamErrorLog.Text(Error("x")).Should().StartWith("WebSocketException ").And.EndWith(": x");
}
