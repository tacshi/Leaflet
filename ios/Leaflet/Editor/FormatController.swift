import LeafletCore
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
        .leafletBold, .leafletItalic, .leafletCode, .leafletMath, .leafletUnderline,
        .leafletStrike, .leafletColor, .leafletBackground,
    ]
    private static let shownKeys: [NSAttributedString.Key] = [
        .underlineStyle, .underlineColor, .strikethroughStyle, .strikethroughColor, .link,
        .backgroundColor, .kern, .leafletMathPicture,
    ]

    init(_ editor: EditorController) { self.editor = editor }

    func updateTypingAttributes() {
        let sel = textView.selectedRange
        let (range, info) = text.paragraph(at: sel.location)
        typing = [.leafletBlock: info]
        let plain = sel.location == plainAt
        if !plain { plainAt = nil }
        if sel.location > range.location, !plain, info.type != "code" {
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

    /// The parts of range formatting goes on: the text of its text blocks,
    /// as a code block holds its code as plain text.
    func formattable(_ range: NSRange) -> [NSRange] {
        text.paragraphs(touching: range).compactMap { para, info in
            guard info.isText, info.type != "code" else { return nil }
            let r = NSIntersectionRange(para, range)
            return r.length > 0 ? r : nil
        }
    }

    /// Whether formatting takes in range: some of its text is out of code,
    /// or, for a caret, the caret is.
    func formats(_ range: NSRange) -> Bool {
        if range.length == 0 { return text.paragraph(at: range.location).info.type != "code" }
        var at = range.location
        while at < NSMaxRange(range), at <= text.length {
            let (para, info) = text.paragraph(at: at)
            if info.isText, info.type != "code", NSIntersectionRange(para, range).length > 0 {
                return true
            }
            at = NSMaxRange(para) + 1
        }
        return false
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
            guard formats(sel) else { return }
            typing[key] = typing[key] == nil ? true : nil
            return
        }
        sel = text.widenToMentions(sel)
        let parts = formattable(sel)
        guard !parts.isEmpty else { return }
        var all = true
        for r in parts where all {
            storage.enumerateAttribute(key, in: r) { v, _, stop in
                if v == nil {
                    all = false
                    stop.pointee = true
                }
            }
        }
        let undo = textView.undoManager
        let before = storage.attributedSubstring(from: sel)
        storage.beginEditing()
        for r in parts {
            if all {
                storage.removeAttribute(key, range: r)
            } else {
                storage.addAttribute(key, value: true, range: r)
            }
        }
        storage.endEditing()
        editor.edits.registerUndo(undo, range: sel, before: before, trailing: text.trailing)
        editor.edited(sel)
    }

    /// Sets an inline attribute on the selection, out of code: false when
    /// none of it takes one.
    @discardableResult
    func set(_ key: NSAttributedString.Key, _ value: Any?) -> Bool {
        guard textView.isEditable else { return false }
        let sel = text.widenToMentions(textView.selectedRange)
        let parts = formattable(sel)
        guard !parts.isEmpty else { return false }
        let before = storage.attributedSubstring(from: sel)
        storage.beginEditing()
        for r in parts {
            if let value {
                storage.addAttribute(key, value: value, range: r)
            } else {
                storage.removeAttribute(key, range: r)
            }
            guard key == .leafletHref else { continue }
            storage.enumerateAttribute(.leafletMention, in: r) { v, m, _ in
                if let mention = v as? Mention, mention.kind == "page",
                    value as? String != "leaflet://page/\(mention.id)"
                {
                    storage.removeAttribute(.leafletMention, range: m)
                }
            }
        }
        storage.endEditing()
        editor.edits.registerUndo(
            textView.undoManager, range: sel, before: before, trailing: text.trailing)
        editor.edited(sel)
        return true
    }

    func editLink() {
        guard textView.isEditable,
            let first = formattable(text.widenToMentions(textView.selectedRange)).first
        else { return }
        let current =
            storage.attribute(.leafletHref, at: first.location, effectiveRange: nil) as? String
        let alert = UIAlertController(title: L.editorLink, message: nil, preferredStyle: .alert)
        let save = UIAlertAction(title: L.commonSave, style: .default) {
            [weak self, weak alert] _ in
            let href = Self.href(alert?.textFields?.first?.text ?? "")
            guard href != "" else { return }
            self?.set(.leafletHref, href)
        }
        save.isEnabled = Self.href(current ?? "") != ""
        alert.addTextField { [weak save] f in
            f.text = current
            f.placeholder = L.editorPasteLink
            f.keyboardType = .URL
            f.autocapitalizationType = .none
            f.addAction(
                UIAction { [weak f] _ in save?.isEnabled = Self.href(f?.text ?? "") != "" },
                for: .editingChanged)
        }
        alert.addAction(UIAlertAction(title: L.commonCancel, style: .cancel))
        if current != nil {
            alert.addAction(
                UIAlertAction(title: LI.removeLink, style: .destructive) { [weak self] _ in
                    self?.set(.leafletHref, nil)
                })
        }
        alert.addAction(save)
        textView.window?.rootViewController?.topmost.present(alert, animated: true)
    }

    /// Where a link goes for the address typed in the link dialog, as the
    /// desktop has it: nil, for nothing typed, takes the link away, and ""
    /// is for what makes no link that opens, which can't be saved.
    static func href(_ typed: String) -> String? {
        let s = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        return s.isEmpty ? nil : MobileLinkHref(s)
    }
}
