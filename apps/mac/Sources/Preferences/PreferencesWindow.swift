import SwiftUI

struct PreferencesWindow: View {
    enum Tab: String, CaseIterable, Hashable {
        case servers, alerts, account, general, logs

        var title: String {
            switch self {
            case .servers: return "Servers"
            case .alerts: return "Alerts"
            case .account: return "Cloud"
            case .general: return "General"
            case .logs: return "Logs"
            }
        }

        var icon: String {
            switch self {
            case .servers: return "server.rack"
            case .alerts: return "gauge"
            case .account: return "cloud"
            case .general: return "gear"
            case .logs: return "doc.text.magnifyingglass"
            }
        }
    }

    @State private var selection: Tab = .servers
    @State private var serverSelection: Set<Node.ID> = []
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(CloudMove.self) private var move: CloudMove?
    @Environment(CloudRulesMove.self) private var rulesMove: CloudRulesMove?

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
        .sheet(isPresented: Binding(get: { move?.isPresented == true }, set: { if !$0 { move?.close() } })) {
            if let move { MoveToCloudSheet(move: move) }
        }
        .sheet(isPresented: Binding(get: { rulesMove?.isPresented == true }, set: { if !$0 { rulesMove?.close() } })) {
            if let rulesMove { MoveRulesToCloudSheet(move: rulesMove) }
        }
        .overlay(alignment: .bottom) {
            if let text = rulesMove?.toast {
                RulesMovedToast(text: text)
                    .padding(.bottom, 20)
                    .transition(.opacity)
                    .task(id: text) {
                        try? await Task.sleep(for: .seconds(6))
                        rulesMove?.clearToast()
                    }
            }
        }
        .task(id: cloud?.isSignedIn) {
            await move?.offerOnce()
            await offerRules()
        }
        .onChange(of: move?.isPresented) { _, shown in
            if shown == false { Task { await offerRules() } }
        }
    }

    /// The rules sheet comes after the host sheet.
    private func offerRules() async {
        guard move?.isPresented != true else { return }
        await rulesMove?.offerOnce()
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
            case .account:
                AccountPane()
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
