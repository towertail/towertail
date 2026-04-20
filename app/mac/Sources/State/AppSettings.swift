import Foundation
import SwiftUI

enum CardDensity: String, CaseIterable, Sendable {
    case a, b
}

@Observable
@MainActor
final class AppSettings {
    var cardDensity: CardDensity = .a
    var thresholds: MetricThresholds = .defaults

    init() {
        if let raw = UserDefaults.standard.string(forKey: "cardDensity"),
           let d = CardDensity(rawValue: raw) {
            cardDensity = d
        }
    }
}
