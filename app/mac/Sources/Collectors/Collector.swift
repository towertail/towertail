import Foundation

protocol Collector: Sendable {
    func run(sink: ServerStore) async
}
