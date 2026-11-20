import Foundation
@testable import Towertail

final class FakeTokenKeychain: TokenKeychain {
    private(set) var items: [String: String] = [:]

    func set(_ token: String, account: String) throws { items[account] = token }
    func get(account: String) throws -> String? { items[account] }
    func delete(account: String) throws { items[account] = nil }
}
