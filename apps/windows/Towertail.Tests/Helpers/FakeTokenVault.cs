using Towertail.WinUI.Cloud;

namespace Towertail.Tests.Helpers;

internal sealed class FakeTokenVault : ITokenVault
{
    public Dictionary<string, string> Tokens { get; } = new();

    public string? Get(string key) => Tokens.GetValueOrDefault(key);
    public void Set(string key, string token) => Tokens[key] = token;
    public void Delete(string key) => Tokens.Remove(key);
}
