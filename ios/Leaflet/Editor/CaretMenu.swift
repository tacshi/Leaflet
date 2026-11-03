import LeafletCore
import SwiftUI
import UIKit

/// MenuItem is one choice of the menu at the caret, as the core offers
/// it (internal/menu): a block or a template for "/"; an agent, a page or
/// a person for "@". Its key says what it is.
struct MenuItem: Decodable, Identifiable {
    let key: String
    let label: String
    let hint: String
    /// icon is a Lucide icon's name, as the desktop shows it.
    let icon: String
    let group: String

    var id: String { key }
    var symbol: String { Lucide.symbol(icon) }
}

/// CaretMenu is the list the editor shows under the caret while "/" or "@"
/// is being typed: what is typed after it filters it, and it is picked
/// from with the arrow keys and Return or Tab, or a tap.
@MainActor
final class CaretMenu {
    /// anchor is where the "/" or "@" is in the text, which a change from
    /// outside moves.
    var anchor: Int
    let trigger: Character
    private(set) var items: [MenuItem] = []
    private(set) var selected = 0
    /// all is everything the menu offers, as the core's JSON, to filter,
    /// and query what was typed after the trigger last.
    private var all: String?
    private var query = ""
    private let host: UIHostingController<CaretMenuView>
    var onPick: ((MenuItem) -> Void)?

    init(anchor: Int, trigger: Character, palette: Palette) {
        self.anchor = anchor
        self.trigger = trigger
        host = UIHostingController(
            rootView: CaretMenuView(items: [], selected: 0, palette: palette, pick: { _ in }))
        host.view.backgroundColor = .clear
    }

    var view: UIView { host.view }
    /// loaded says the core has said what the menu offers.
    var loaded: Bool { all != nil }

    /// load gives the menu what it offers, once the core has said.
    func load(_ items: String) {
        all = items
        filter(query)
    }

    /// filter keeps what query finds. The item chosen stays chosen while
    /// what is typed does, as a change from outside filters again: the
    /// first is chosen again only as the query changes, or the item goes.
    func filter(_ query: String) {
        let kept = query == self.query ? current?.key : nil
        self.query = query
        guard let all else { return }
        var error: NSError?
        let raw = MobileFilterMenu(all, query, &error)
        items = (try? JSONDecoder().decode([MenuItem].self, from: Data(raw.utf8))) ?? []
        selected = kept.flatMap { key in items.firstIndex { $0.key == key } } ?? 0
        render()
    }

    func move(_ step: Int) {
        guard !items.isEmpty else { return }
        selected = (selected + step + items.count) % items.count
        render()
    }

    var current: MenuItem? { items.indices.contains(selected) ? items[selected] : nil }

    private func render() {
        let palette = host.rootView.palette
        host.rootView = CaretMenuView(
            items: items, selected: selected, palette: palette,
            pick: { [weak self] i in
                guard let self, self.items.indices.contains(i) else { return }
                self.onPick?(self.items[i])
            })
    }

    /// place puts the menu under the caret, or over it when there is no
    /// room below, within the part of the text view that shows.
    func place(in textView: UITextView, caret: CGRect) {
        let width = min(320, textView.bounds.width - 24)
        let rows = max(items.count, 1)
        let headers = Set(items.map(\.group)).count
        let height = min(CGFloat(rows) * 48 + CGFloat(headers) * 28 + 12, 340)
        let visible = textView.bounds.inset(by: textView.adjustedContentInset)
        var x = caret.minX - 12
        x = min(max(x, visible.minX + 12), visible.maxX - width - 12)
        var y = caret.maxY + 6
        if y + height > visible.maxY, caret.minY - height - 6 > visible.minY {
            y = caret.minY - height - 6
        }
        view.frame = CGRect(x: x, y: y, width: width, height: height)
        if view.superview !== textView { textView.addSubview(view) }
    }

    func close() { view.removeFromSuperview() }
}

/// CaretMenuView draws the menu at the caret.
struct CaretMenuView: View {
    let items: [MenuItem]
    let selected: Int
    let palette: Palette
    let pick: (Int) -> Void

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if items.isEmpty {
                        Text(L.editorNoMatches)
                            .foregroundStyle(palette.textMuted)
                            .padding(14)
                    }
                    ForEach(Array(items.enumerated()), id: \.element.id) { i, item in
                        if i == 0 || items[i - 1].group != item.group {
                            Text(item.group)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(palette.textMuted)
                                .padding(.horizontal, 14)
                                .padding(.top, 8)
                                .padding(.bottom, 4)
                        }
                        Button {
                            pick(i)
                        } label: {
                            HStack(spacing: 12) {
                                Image(systemName: item.symbol)
                                    .frame(width: 28, height: 28)
                                    .background(palette.panel, in: .rect(cornerRadius: 6))
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(item.label).font(.subheadline.weight(.medium))
                                    if !item.hint.isEmpty {
                                        Text(item.hint).font(.caption).foregroundStyle(
                                            palette.textMuted
                                        )
                                        .lineLimit(1)
                                    }
                                }
                                Spacer(minLength: 0)
                            }
                            .foregroundStyle(palette.text)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 6)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(
                                i == selected ? palette.accentSoft : .clear,
                                in: .rect(cornerRadius: 8)
                            )
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .hoverEffect(.highlight)
                        .padding(.horizontal, 4)
                        .id(item.id)
                        .accessibilityAddTraits(i == selected ? .isSelected : [])
                    }
                }
                .padding(.vertical, 4)
            }
            .onChange(of: selected) {
                if items.indices.contains(selected) { proxy.scrollTo(items[selected].id) }
            }
        }
        .background(palette.surface, in: .rect(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(palette.border, lineWidth: 0.5))
        .shadow(color: .black.opacity(0.15), radius: 16, y: 6)
    }
}
