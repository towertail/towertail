import SwiftUI

/// Makes the flow from the environment and closes the window at its end.
struct WelcomeWindow: View {
    @Environment(NodeStore.self) private var nodes
    @Environment(ClientSettings.self) private var settings
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @Environment(CloudInstaller.self) private var installer: CloudInstaller?
    @Environment(\.dismiss) private var dismiss
    @State private var flow: WelcomeFlow?

    var body: some View {
        Group {
            if let flow { WelcomeContent(flow: flow) }
        }
        .frame(width: WelcomeContent.size.width, height: WelcomeContent.size.height)
        .onAppear {
            ActivationPolicyCoordinator.shared.acquire()
            guard flow == nil else { return }
            let made = WelcomeFlow(nodes: nodes, settings: settings, cloud: cloud, installer: installer)
            made.close = { dismiss() }
            flow = made
        }
        .onDisappear {
            flow?.markDone()
            ActivationPolicyCoordinator.shared.release()
        }
    }
}

struct WelcomeContent: View {
    static let size = CGSize(width: 760, height: 540)

    let flow: WelcomeFlow

    var body: some View {
        VStack(spacing: 0) {
            switch flow.step {
            case .welcome: WelcomeIntroView(flow: flow)
            case .servers: WelcomeServersView(flow: flow)
            case .cloud: WelcomeCloudView(flow: flow)
            case .signIn: WelcomeSignInView(flow: flow)
            case .install: WelcomeInstallView(flow: flow)
            }
        }
        .frame(width: Self.size.width, height: Self.size.height)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}

/// The five step dots and the two buttons.
struct WelcomeFooter: View {
    struct Action {
        let title: String
        var enabled = true
        let run: () -> Void
    }

    let step: WelcomeFlow.Step
    var secondary: Action?
    var primary: Action?

    var body: some View {
        ZStack {
            HStack(spacing: 6) {
                ForEach(WelcomeFlow.Step.allCases, id: \.rawValue) { s in
                    Circle()
                        .fill(s == step ? Color.accentColor : Color.secondary.opacity(0.3))
                        .frame(width: 7, height: 7)
                }
            }
            .accessibilityLabel("Step \(step.rawValue + 1) of \(WelcomeFlow.Step.allCases.count)")
            HStack {
                if let secondary {
                    Button(secondary.title, action: secondary.run)
                        .disabled(!secondary.enabled)
                }
                Spacer()
                if let primary {
                    Button(primary.title, action: primary.run)
                        .keyboardShortcut(.defaultAction)
                        .buttonStyle(.borderedProminent)
                        .disabled(!primary.enabled)
                }
            }
        }
        .padding(.horizontal, 32)
        .padding(.vertical, 18)
        .background(Color("Card/Background"))
    }
}

/// Title and subtitle at the top of a step.
struct WelcomeHeading: View {
    let title: String
    let subtitle: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.system(size: 26, weight: .semibold))
            Text(subtitle).font(.system(size: 13)).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
