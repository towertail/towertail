import SwiftUI

/// Six one-digit fields for the code of the sign-in email. It calls
/// `onComplete` at the last digit. Give the view a new `.id` to clear it.
struct CodeEntryView: View {
    static let length = 6

    let onComplete: (String) -> Void
    @State private var cells = Array(repeating: "", count: CodeEntryView.length)
    @FocusState private var focus: Int?

    /// The code in a pasted text: "482913" or "482 913". Nil when the text
    /// is not six digits.
    static func digits(from text: String) -> String? {
        let compact = text.filter { !$0.isWhitespace }
        guard compact.count == length, compact.allSatisfy({ $0.isASCII && $0.isNumber }) else { return nil }
        return compact
    }

    var body: some View {
        HStack(spacing: 8) {
            ForEach(0..<Self.length, id: \.self) { i in
                TextField("", text: binding(at: i))
                    .textFieldStyle(.plain)
                    .font(.system(size: 22, weight: .semibold, design: .monospaced))
                    .multilineTextAlignment(.center)
                    .frame(width: 36, height: 44)
                    .background(RoundedRectangle(cornerRadius: 8).fill(Color.secondary.opacity(0.12)))
                    .overlay(
                        RoundedRectangle(cornerRadius: 8)
                            .stroke(focus == i ? Color.accentColor : Color.secondary.opacity(0.3), lineWidth: 1)
                    )
                    .focused($focus, equals: i)
                    .onKeyPress(.delete) { backspace(at: i) }
                    .accessibilityLabel("Digit \(i + 1) of \(Self.length)")
            }
        }
        .onAppear { focus = 0 }
    }

    private func binding(at i: Int) -> Binding<String> {
        Binding(get: { cells[i] }, set: { input(String($0), at: i) })
    }

    private func input(_ raw: String, at i: Int) {
        if let code = Self.digits(from: raw) {
            cells = code.map(String.init)
            focus = Self.length - 1
            onComplete(code)
            return
        }
        guard let digit = raw.last(where: { $0.isASCII && $0.isNumber }) else {
            cells[i] = ""
            return
        }
        cells[i] = String(digit)
        if i + 1 < Self.length {
            focus = i + 1
        } else if cells.allSatisfy({ !$0.isEmpty }) {
            onComplete(cells.joined())
        }
    }

    private func backspace(at i: Int) -> KeyPress.Result {
        guard cells[i].isEmpty, i > 0 else { return .ignored }
        cells[i - 1] = ""
        focus = i - 1
        return .handled
    }
}
