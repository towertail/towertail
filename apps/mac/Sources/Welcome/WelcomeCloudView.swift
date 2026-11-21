import SwiftUI

struct WelcomeCloudView: View {
    let flow: WelcomeFlow
    @Environment(CloudSession.self) private var cloud: CloudSession?
    @State private var busy = false

    private static let rows: [(String, Bool, Bool)] = [
        ("Live view on this Mac", true, true),
        ("Alerts while this Mac sleeps", false, true),
        ("History for 1 to 365 days", false, true),
        ("Phone and email alerts, web dashboard", false, true),
    ]

    private static let plans: [(name: String, price: String, hosts: String, detail: String)] = [
        ("Free", "$0", "3 Cloud hosts", "1 day of history · email alerts"),
        ("Solo", "$5/mo", "25 Cloud hosts", "30 days of history · push alerts"),
        ("Team", "$10/user/mo", "100 Cloud hosts", "365 days of history · 3 or more seats"),
    ]

    var body: some View {
        @Bindable var flow = flow
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 18) {
                WelcomeHeading(
                    title: "Keep watching while you sleep",
                    subtitle: "Towertail Cloud adds 24/7 monitoring to the hosts that you pick. The other hosts stay on this Mac over SSH, with no limit."
                )
                comparison
                HStack(spacing: 12) {
                    ForEach(Self.plans, id: \.name) { plan in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(alignment: .firstTextBaseline) {
                                Text(plan.name).font(Typography.headerTitle)
                                Spacer()
                                Text(plan.price).font(.system(size: 11)).foregroundStyle(.secondary)
                            }
                            Text(plan.hosts).font(Typography.rowName)
                            Text(plan.detail).font(.system(size: 11)).foregroundStyle(.secondary)
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
                    }
                }
                Text("All plans: SSH hosts on this Mac have no limit.")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                HStack {
                    TextField("Email", text: $flow.email)
                        .textFieldStyle(.roundedBorder)
                        .textContentType(.emailAddress)
                        .onSubmit(send)
                        .frame(maxWidth: 320)
                    Spacer()
                }
                if case .failed(let message) = cloud?.account.status {
                    Text(message).font(.system(size: 12)).foregroundStyle(ThresholdTint.critical.color)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 32)
            .padding(.top, 32)
            .padding(.bottom, 12)
            WelcomeFooter(
                step: .cloud,
                secondary: .init(title: "Not now", run: flow.finish),
                primary: .init(
                    title: flow.isSignedIn ? "Continue" : "Email me a code",
                    enabled: !busy && (flow.isSignedIn || !flow.email.trimmingCharacters(in: .whitespaces).isEmpty),
                    run: flow.isSignedIn ? flow.next : send
                )
            )
        }
    }

    private var comparison: some View {
        VStack(spacing: 0) {
            HStack {
                Text("").frame(maxWidth: .infinity, alignment: .leading)
                Text("SSH host").frame(width: 90)
                Text("Cloud host").frame(width: 90)
            }
            .font(Typography.rowColumn)
            .foregroundStyle(.secondary)
            .padding(.vertical, 6)
            Divider()
            HStack {
                Text("Number of hosts").font(.system(size: 12)).frame(maxWidth: .infinity, alignment: .leading)
                Text("No limit")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(ThresholdTint.nominal.color)
                    .frame(width: 90)
                Text("By plan").font(.system(size: 12)).frame(width: 90)
            }
            .padding(.vertical, 5)
            ForEach(Self.rows, id: \.0) { row in
                Divider()
                HStack {
                    Text(row.0).font(.system(size: 12)).frame(maxWidth: .infinity, alignment: .leading)
                    mark(row.1).frame(width: 90)
                    mark(row.2).frame(width: 90)
                }
                .padding(.vertical, 5)
            }
        }
        .padding(.horizontal, 12)
        .background(RoundedRectangle(cornerRadius: 10).fill(Color("Card/Background")))
    }

    private func mark(_ on: Bool) -> some View {
        Image(systemName: on ? "checkmark" : "minus")
            .foregroundStyle(on ? ThresholdTint.nominal.color : Color.secondary)
            .accessibilityLabel(on ? "Yes" : "No")
    }

    private func send() {
        guard !busy else { return }
        busy = true
        Task {
            await flow.sendCode()
            busy = false
        }
    }
}
