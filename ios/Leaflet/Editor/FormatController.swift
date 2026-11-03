import UIKit

/// FormatController keeps the inline formats: those typing carries on, as
/// the text before the caret has them or as they were toggled for it, and
/// those a command sets on the text chosen, bold to a link or a color.
@MainActor
final class FormatController {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    /// typing are the block and formats what is typed next takes. The
    /// text view's own typing attributes drop the editor's keys, so the
    /// editor sets them on typed text itself.
    var typing: [NSAttributedString.Key: Any] = [:]
    /// typed is where text went in that the rules let through, for
    /// textViewDidChange to give it the typing attributes as they were:
    /// the selection's change, which comes first, reads them again from
    /// the text just typed, losing a format toggled for it.
    var typed: (range: NSRange, attributes: [NSAttributedString.Key: Any])?
    /// plainAt is the caret after a format typed as Markdown, where typing
    /// goes on without it.
    var plainAt: Int?

    /// formatKeys are the inline formats typing carries on.
    static let formatKeys: [NSAttributedString.Key] = [
        .leafletBold, .leafletItalic, .leafletCode, .leafletUnderline, .leafletStrike,
        .leafletColor, .leafletBackground,
    ]
    /// shownKeys are the attributes the style draws the formats with, which
    /// typing never carries on: the style draws them again for the text.
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
            // Typing carries on the formats of the text before the caret,
            // in its block; links and mentions stay what they are.
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

    /// replacing is what text put in place of range takes, as autocorrect,
    /// a text replacement or dictation puts a word in place of another, or
    /// a composition goes on: the formats, link and mention of the text it
    /// replaces, which the caret's typing would take off.
    func replacing(_ range: NSRange) -> [NSAttributedString.Key: Any] {
        var out = typing
        let replaced = storage.attributes(at: range.location, effectiveRange: nil)
        for k in Self.formatKeys + [.leafletHref, .leafletMention] { out[k] = replaced[k] }
        // A mention is its name, and its page's link: text put in place of
        // all of it is neither.
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

    /// takeTyping gives text typed in range the block and formats it was
    /// typed with: the text view's own typing attributes drop the editor's.
    func takeTyping(_ r: NSRange, _ attributes: [NSAttributedString.Key: Any]) {
        guard NSMaxRange(r) <= storage.length else { return }
        for k in Self.formatKeys where attributes[k] == nil { storage.removeAttribute(k, range: r) }
        storage.removeAttribute(.leafletHref, range: r)
        storage.removeAttribute(.leafletMention, range: r)
        storage.addAttributes(attributes, range: r)
    }

    func toggle(_ key: NSAttributedString.Key) {
        guard textView.isEditable else { return }
        // The system's underline and strikethrough are the text's own, which
        // the style draws: a link's and a checked to-do's lines are not.
        let key: NSAttributedString.Key =
            switch key {
            case .underlineStyle: .leafletUnderline
            case .strikethroughStyle: .leafletStrike
            default: key
            }
        var sel = textView.selectedRange
        if sel.length == 0 {
            // What is typed next takes it, or goes without it.
            typing[key] = typing[key] == nil ? true : nil
            return
        }
        // A mention is formatted whole.
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

    /// set sets a format with a value (a link, a color) on the selection,
    /// or takes it off with nil.
    func set(_ key: NSAttributedString.Key, _ value: Any?) {
        guard textView.isEditable else { return }
        // A mention is formatted whole.
        let sel = text.widenToMentions(textView.selectedRange)
        guard sel.length > 0 else { return }
        let before = storage.attributedSubstring(from: sel)
        if let value {
            storage.addAttribute(key, value: value, range: sel)
        } else {
            storage.removeAttribute(key, range: sel)
        }
        // A page mention linked elsewhere, or no more, is text no longer
        // standing for its page, as the desktop's SetLink has it: the link
        // given is what it opens.
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

    /// editLink asks for the link of the text chosen, or to take it off.
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
