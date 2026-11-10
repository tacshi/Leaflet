import LeafletCore
import UIKit

@MainActor
final class CaretMenuController {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private(set) var menu: CaretMenu?
    var opening: (Int, Character)?

    init(_ editor: EditorController) { self.editor = editor }

    func trigger(_ typed: String, at location: Int, in para: NSRange) -> Character? {
        guard typed.count == 1 else { return nil }
        let ns = storage.string as NSString
        let before =
            location > para.location
            ? ns.substring(with: ns.rangeOfComposedCharacterSequence(at: location - 1)) : ""
        return MobileMenu(typed, before).first
    }

    func openOrUpdate() {
        if let (at, trigger) = opening {
            opening = nil
            open(at: at, trigger: trigger)
        } else {
            update()
        }
    }

    private func open(at anchor: Int, trigger: Character) {
        close()
        let m = CaretMenu(anchor: anchor, trigger: trigger, palette: editor.style.palette)
        m.onPick = { [weak self] item in self?.pick(item) }
        menu = m
        textView.reloadInputViews()
        editor.menuItems?(trigger) { [weak self, weak m] items in
            guard let self, let m, self.menu === m else { return }
            m.load(items)
            self.update()
        }
        update()
    }

    func update() {
        guard let menu else { return }
        let ns = storage.string as NSString
        let caret = textView.selectedRange
        guard anchored(menu) else { return close() }
        let query = ns.substring(
            with: NSRange(location: menu.anchor + 1, length: caret.location - menu.anchor - 1))
        if query.contains("\n") || query.contains(String(lineBreak)) || query.count > 30
            || query.hasPrefix(" ")
        {
            return close()
        }
        menu.filter(query)
        if menu.items.isEmpty, query.hasSuffix(" ") { return close() }
        guard let end = textView.selectedTextRange?.end else { return }
        menu.place(in: textView, caret: textView.caretRect(for: end))
    }

    private func anchored(_ menu: CaretMenu) -> Bool {
        let ns = storage.string as NSString
        let caret = textView.selectedRange
        guard caret.length == 0, menu.anchor < ns.length, caret.location > menu.anchor else {
            return false
        }
        let char = ns.substring(with: ns.rangeOfComposedCharacterSequence(at: menu.anchor))
        return MobileMenuKind(char).first == menu.trigger
    }

    func close() {
        guard let menu else { return }
        menu.close()
        self.menu = nil
        textView.reloadInputViews()
    }

    func move(_ step: Int) { menu?.move(step) }

    func pick(_ item: MenuItem) {
        guard let menu else { return }
        guard anchored(menu) else { return close() }
        let caret = textView.selectedRange.location
        close()
        editor.edits.delete(NSRange(location: menu.anchor, length: max(caret - menu.anchor, 1)))
        textView.selectedRange = NSRange(location: menu.anchor, length: 0)
        defer { editor.edits.tellKeyboard() }
        if menu.trigger == "@" {
            mention(item, at: menu.anchor)
            return
        }
        make(item, at: menu.anchor)
    }

    private func make(_ item: MenuItem, at location: Int) {
        var error: NSError?
        let raw = MobileMenuBlock(item.key, editor.author?() ?? "", &error)
        let made = try? JSONDecoder().decode(MenuBlock.self, from: Data(raw.utf8))
        if let type = made?.type {
            let (para, info) = text.paragraph(at: location)
            let data = made?.data ?? [:]
            if para.length == 0 {
                editor.edits.setBlock(info.with(type: type, data: data), at: para.location)
            } else {
                editor.edits.insertParagraph(
                    after: para,
                    info: BlockInfo(
                        id: BlockInfo.newID(), type: type, data: data, depth: info.depth),
                    text: "")
            }
        } else if let node = made?.node {
            editor.commands.insertBlock(node)
            if ["chart", "math_equation", "diagram"].contains(node.type) {
                editor.onEditBlock?(node)
            }
        } else if item.key == "subpage" {
            let block = text.paragraph(at: location).info.id
            Task { [weak self] in
                guard let id = await self?.editor.createSubpage?(), let self else { return }
                let card = DocNode(
                    id: BlockInfo.newID(), type: "subpage", data: ["page_id": .string(id)],
                    children: nil)
                let at = self.text.blocks().first { $0.info.id == block }?.range.location
                self.editor.commands.insertBlock(card, at: at ?? self.storage.length)
            }
        } else {
            editor.onMenuItem?(item.key, text.paragraph(at: location).info.id)
        }
    }

    func insertMenu(_ done: @escaping ([UIMenuElement]) -> Void) {
        guard let menuItems = editor.menuItems else { return done([]) }
        menuItems("/") { [weak self] all in
            var error: NSError?
            let raw = MobileFilterMenu(all, "", &error)
            let items = (try? JSONDecoder().decode([MenuItem].self, from: Data(raw.utf8))) ?? []
            var groups: [(name: String, actions: [UIMenuElement])] = []
            for item in items {
                let action = UIAction(title: item.label, image: UIImage(systemName: item.symbol)) {
                    [weak self] _ in self?.insertItem(item)
                }
                if groups.last?.name == item.group {
                    groups[groups.count - 1].actions.append(action)
                } else {
                    groups.append((item.group, [action]))
                }
            }
            guard self != nil else { return done([]) }
            done(
                groups.map { UIMenu(title: $0.name, options: .displayInline, children: $0.actions) }
            )
        }
    }

    func insertItem(_ item: MenuItem) {
        guard textView.isEditable else { return }
        let at = textView.selectedRange.location
        textView.selectedRange = NSRange(location: at, length: 0)
        make(item, at: at)
        editor.edits.tellKeyboard()
    }

    struct MenuBlock: Decodable {
        var type: String?
        var data: [String: JSONValue]?
        var node: DocNode?
    }

    private func mention(_ item: MenuItem, at anchor: Int) {
        let parts = item.key.split(separator: ":", maxSplits: 1).map(String.init)
        let kind = parts.first ?? ""
        let id = parts.count > 1 ? parts[1] : ""
        switch kind {
        case "connect":
            editor.onAddAgent?()
        case "agent":
            editor.onAsk?(
                editor.focusBlockIDs(), "", editor.agents?().first { $0.providerID == id })
        case "page", "user":
            let page = kind == "page"
            let info = text.paragraph(at: anchor).info
            var attributes: [NSAttributedString.Key: Any] = [
                .leafletBlock: info,
                .leafletMention: Mention(kind: kind, id: id, name: item.label),
            ]
            if page { attributes[.leafletHref] = "leaflet://page/\(id)" }
            let mention = NSMutableAttributedString(
                string: (page ? "" : "@") + item.label, attributes: attributes)
            mention.append(NSAttributedString(string: " ", attributes: [.leafletBlock: info]))
            editor.edits.replace(NSRange(location: anchor, length: 0), with: mention)
            textView.selectedRange = NSRange(location: anchor + mention.length, length: 0)
            editor.edited(NSRange(location: anchor, length: mention.length))
        default:
            break
        }
    }
}
