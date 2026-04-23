import Foundation

extension String {
    /// Lowercased + whitespace-trimmed for use as a search needle. Centralised so
    /// every search input applies the same rule — divergence here means a query
    /// matches in one pane but not another.
    func normalizedForSearch() -> String {
        trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}
