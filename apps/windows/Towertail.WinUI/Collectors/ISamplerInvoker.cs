using Towertail.WinUI.State;

namespace Towertail.WinUI.Collectors;

/// <summary>
/// Abstraction over the "run a sampler and return a sample" contract. Local and SSH invokers
/// both implement this; the collector consumes the abstraction so it can swap transports
/// without knowing the details.
/// </summary>
public interface ISamplerInvoker
{
    /// <summary>
    /// Run <c>towertail-sampler --once</c> and return the decoded sample.
    /// </summary>
    Task<Sample> RunOnceAsync(CancellationToken ct = default);

    /// <summary>
    /// Run <c>towertail-sampler --interval &lt;dur&gt;</c> and yield samples as they arrive.
    /// </summary>
    IAsyncEnumerable<Sample> StreamAsync(TimeSpan interval, CancellationToken ct = default);

    /// <summary>
    /// Return the sampler's self-reported <c>--version</c> string, e.g. <c>towertail-sampler 0.1.0+abc</c>.
    /// Used by the bootstrap handshake.
    /// </summary>
    Task<string> VersionAsync(CancellationToken ct = default);

    /// <summary>
    /// Run <c>towertail-sampler --self-check</c> — returns true iff the binary is runnable on the target.
    /// </summary>
    Task<bool> SelfCheckAsync(CancellationToken ct = default);
}

public interface ISamplerInvokerFactory
{
    ISamplerInvoker Create(Node node);
}

public sealed class SamplerInvokerFactory : ISamplerInvokerFactory
{
    private readonly IProcessRunner _runner;
    private readonly HostKeyPrompt? _hostKeyPrompt;
    private readonly Action<Guid, string>? _onTrust;

    public SamplerInvokerFactory(
        IProcessRunner? runner = null,
        HostKeyPrompt? hostKeyPrompt = null,
        Action<Guid, string>? onTrust = null)
    {
        _runner = runner ?? new DefaultProcessRunner();
        _hostKeyPrompt = hostKeyPrompt;
        _onTrust = onTrust;
    }

    public ISamplerInvoker Create(Node node) => node.Kind switch
    {
        NodeKind.Local => new LocalSamplerInvoker(_runner),
        NodeKind.Ssh => new SshSamplerInvoker(node, hostKeyPrompt: _hostKeyPrompt, onTrust: _onTrust),
        _ => throw new NotSupportedException($"unknown node kind {node.Kind}"),
    };
}
