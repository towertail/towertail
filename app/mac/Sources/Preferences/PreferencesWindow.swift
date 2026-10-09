import SwiftUI

struct PreferencesWindow: View {
    enum Tab: String, CaseIterable, Hashable {
        case servers, alerts, general, logs

        var title: String {
            switch self {
            case .servers: return "Servers"
            case .alerts: return "Alerts"
            case .general: return "General"
            case .logs: return "Logs"
            }
        }

        var icon: String {
            switch self {
            case .servers: return "server.rack"
            case .alerts: return "gauge"
            case .general: return "gear"
            case .logs: return "doc.text.magnifyingglass"
            }
        }
    }

    @State private var selection: Tab = .servers
    @State private var serverSelection: Set<Node.ID> = []

    var body: some View {
        TabView(selection: $selection) {
            ForEach(Tab.allCases, id: \.self) { tab in
                paneContent(for: tab)
                    .tabItem { Label(tab.title, systemImage: tab.icon) }
                    .tag(tab)
            }
        }
        .frame(minWidth: 780, idealWidth: 840, minHeight: 560, idealHeight: 660)
        .navigationTitle(selection.title)
    }

    /// Only the active tab gets its real pane. Inactive panes stay torn down,
    /// which avoids an `AGGraphGetAttributeSubgraph` crash on tab switch
    /// (macOS 15 / SwiftUI 6) and keeps TabView's native toolbar chrome.
    @ViewBuilder
    private func paneContent(for tab: Tab) -> some View {
        if tab == selection {
            switch tab {
            case .servers:
                ServersPane(selection: $serverSelection) { selection = .alerts }
            case .alerts:
                AlertsPane { id in
                    serverSelection = [id]
                    selection = .servers
                }
            case .general:
                GeneralPane()
            case .logs:
                LogsPane().padding()
            }
        } else {
            Color.clear
        }
    }
}
