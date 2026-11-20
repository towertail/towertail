import Foundation
import Observation

/// Owns the Cloud parts of the app: the account now, the data path later.
@MainActor
@Observable
final class CloudSession {
    let account: CloudAccount

    init(account: CloudAccount) {
        self.account = account
    }
}
