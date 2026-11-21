import SwiftUI

/// Six digit cells for the code of the sign-in email. It calls `onComplete`
/// at the last digit. Give the view a new `.id` to clear it.
///
/// One hidden field takes the keys and the paste. The cells are `Text`, so
/// the digits keep their font: a focused `TextField` on macOS draws a value
/// that changes in code with the system font of the field editor.
struct CodeEntryView: View {
    static let length = 6

    let onComplete: (String) -> Void
    @State private var code = ""
    @FocusState private var focused: Bool

    /// The digits of a typed or pasted text, at most six: "482 913" gives
    /// "482913".
    static func digits(from text: String) -> String {
        String(text.filter { $0.isASCII && $0.isNumber }.prefix(length))
    }

    var body: some View {
        ZStack {
            TextField("", text: $code)
                .textFieldStyle(.plain)
                .focused($focused)
                .opacity(0.01)
                .frame(width: 1, height: 1)
                .accessibilityLabel("Sign-in code")
                .onChange(of: code) { _, new in accept(new) }
            HStack(spacing: 8) {
                ForEach(0..<Self.length, id: \.self) { cell($0) }
            }
            .contentShape(Rectangle())
            .onTapGesture { focused = true }
            .accessibilityHidden(true)
        }
        .onAppear { focused = true }
    }

    private func cell(_ i: Int) -> some View {
        let chars = Array(code)
        let active = focused && i == min(chars.count, Self.length - 1)
        return Text(i < chars.count ? String(chars[i]) : "")
            .font(.system(size: 22, weight: .semibold, design: .monospaced))
            .frame(width: 36, height: 44)
            .background(RoundedRectangle(cornerRadius: 8).fill(Color.secondary.opacity(0.12)))
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(active ? Color.accentColor : Color.secondary.opacity(0.3), lineWidth: 1)
            )
    }

    private func accept(_ text: String) {
        let clean = Self.digits(from: text)
        guard clean == text else {
            code = clean
            return
        }
        if clean.count == Self.length { onComplete(clean) }
    }
}
