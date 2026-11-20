import SwiftUI

struct WelcomeIntroView: View {
    let flow: WelcomeFlow

    private static let cards: [(icon: String, title: String, text: String)] = [
        ("terminal", "Live over SSH", "See CPU, memory, disk and network of your servers in the menu bar. No agent to install by hand."),
        ("shippingbox", "One small binary", "Towertail copies one small file to each server. It runs as your user. It needs no sudo."),
        ("bell.badge", "Alerts when you sleep", "Cloud watches your servers when this Mac is asleep. It sends an email when a server is down."),
    ]

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 28) {
                WelcomeHeading(
                    title: "Welcome to Towertail",
                    subtitle: "Watch your servers from the menu bar. Setup takes about two minutes."
                )
                HStack(alignment: .top, spacing: 16) {
                    ForEach(Self.cards, id: \.title) { card in
                        VStack(alignment: .leading, spacing: 10) {
                            Image(systemName: card.icon)
                                .font(.system(size: 20))
                                .foregroundStyle(Color.accentColor)
                            Text(card.title).font(Typography.headerTitle)
                            Text(card.text)
                                .font(.system(size: 12))
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .padding(16)
                        .frame(maxWidth: .infinity, minHeight: 160, alignment: .topLeading)
                        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
                    }
                }
                Spacer()
            }
            .padding(32)
            WelcomeFooter(
                step: .welcome,
                secondary: .init(title: "Skip setup", run: flow.skip),
                primary: .init(title: "Get started", run: flow.next)
            )
        }
    }
}
