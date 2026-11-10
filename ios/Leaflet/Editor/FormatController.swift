import UIKit

@MainActor
final class FormatController {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    var typing: [NSAttributedString.Key: Any] = [:]
    var typed: (range: NSRange, attributes: [NSAttributedString.Key: Any])?
    var plainAt: Int?

    static let formatKeys: [NSAttributedString.Key] = [
        .leafletBold, .leafletItalic, .leafletCode, .leafletUnderline, .leafletStrike,
        .leafletColor, .leafletBackground,
    ]
    private static let shownKeys: [NSAttributedString.Key] = [
        .underlineStyle, .underlineColor, .strikethroughStyle, .strikethroughColor, .link,
        .backgroundColor,
    ]

    init(_ editor: EditorController) { self.editor = editor }

    func updateTypingAttributes() {
        let sel = textView.selectedRange
        let (range, info) = text.paragraph(at: sel.location)
        typing = [.leafletBlock: info]
        let plain = sel.location == plainAt
        if !plain { plainAt = nil }
        if sel.location > range.location, !plain {
            let before = storage.attributes(at: sel.location - 1, effectiveRange: nil)
            for k in Self.formatKeys { typing[k] = before[k] }
        }
        var attrs = textView.typingAttributes
        for k in Self.formatKeys + Self.shownKeys + [.leafletHref, .leafletMention, .leafletTinted]
        {
            attrs[k] = nil
        }
        attrs.merge(typing) { $1 }
        let style = editor.style
        attrs[.paragraphStyle] = style.paragraphStyle(info)
        attrs[.font] = style.baseFont(info)
        attrs[.foregroundColor] = UIColor(style.palette.text)
        textView.typingAttributes = attrs
    }

    func replacing(_ range: NSRange) -> [NSAttributedString.Key: Any] {
        var out = typing
        let replaced = storage.attributes(at: range.location, effectiveRange: nil)
        for k in Self.formatKeys + [.leafletHref, .leafletMention] { out[k] = replaced[k] }
        var run = NSRange()
        if storage.attribute(
            .leafletMention, at: range.location, longestEffectiveRange: &run,
            in: NSRange(location: 0, length: storage.length)) != nil,
            NSIntersectionRange(run, range) == run
        {
            out[.leafletMention] = nil
            out[.leafletHref] = nil
        }
        return out
    }

    func takeTyping(_ r: NSRange, _ attributes: [NSAttributedString.Key: Any]) {
        guard NSMaxRange(r) <= storage.length else { return }
        for k in Self.formatKeys where attributes[k] == nil { storage.removeAttribute(k, range: r) }
        storage.removeAttribute(.leafletHref, range: r)
        storage.removeAttribute(.leafletMention, range: r)
        storage.addAttributes(attributes, range: r)
    }

    func toggle(_ key: NSAttributedString.Key) {
        guard textView.isEditable else { return }
        let key: NSAttributedString.Key =
            switch key {
            case .underlineStyle: .leafletUnderline
            case .strikethroughStyle: .leafletStrike
            default: key
            }
        var sel = textView.selectedRange
        if sel.length == 0 {
            typing[key] = typing[key] == nil ? true : nil
            return
        }
        sel = text.widenToMentions(sel)
        var all = true
        storage.enumerateAttribute(key, in: sel) { v, _, stop in
            if v == nil {
                all = false
                stop.pointee = true
            }
        }
        let undo = textView.undoManager
        let before = storage.attributedSubstring(from: sel)
        if all {
            storage.removeAttribute(key, range: sel)
        } else {
            storage.addAttribute(key, value: true, range: sel)
        }
        editor.edits.registerUndo(undo, range: sel, before: before, trailing: text.trailing)
        editor.edited(sel)
    }

    func set(_ key: NSAttributedString.Key, _ value: Any?) {
        guard textView.isEditable else { return }
        let sel = text.widenToMentions(textView.selectedRange)
        guard sel.length > 0 else { return }
        let before = storage.attributedSubstring(from: sel)
        if let value {
            storage.addAttribute(key, value: value, range: sel)
        } else {
            storage.removeAttribute(key, range: sel)
        }
        if key == .leafletHref {
            storage.enumerateAttribute(.leafletMention, in: sel) { v, r, _ in
                if let m = v as? Mention, m.kind == "page",
                    value as? String != "leaflet://page/\(m.id)"
                {
                    storage.removeAttribute(.leafletMention, range: r)
                }
            }
        }
        editor.edits.registerUndo(
            textView.undoManager, range: sel, before: before, trailing: text.trailing)
        editor.edited(sel)
    }

    func editLink() {
        guard textView.isEditable else { return }
        let sel = textView.selectedRange
        guard sel.length > 0 else { return }
        let current =
            storage.attribute(.leafletHref, at: sel.location, effectiveRange: nil) as? String
        let alert = UIAlertController(title: L.editorLink, message: nil, preferredStyle: .alert)
        alert.addTextField { f in
            f.text = current
            f.placeholder = L.editorPasteLink
            f.keyboardType = .URL
            f.autocapitalizationType = .none
        }
        alert.addAction(UIAlertAction(title: L.commonCancel, style: .cancel))
        if current != nil {
            alert.addAction(
                UIAlertAction(title: LI.removeLink, style: .destructive) { [weak self] _ in
                    self?.set(.leafletHref, nil)
                })
        }
        alert.addAction(
            UIAlertAction(title: L.commonSave, style: .default) { [weak self, weak alert] _ in
                let link =
                    alert?.textFields?.first?.text?.trimmingCharacters(in: .whitespaces) ?? ""
                self?.set(.leafletHref, link.isEmpty ? nil : link)
            })
        textView.window?.rootViewController?.topmost.present(alert, animated: true)
    }
}
