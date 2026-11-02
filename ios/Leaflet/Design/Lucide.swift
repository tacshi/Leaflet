/// Lucide maps the Lucide icons the core names, as the desktop draws them,
/// to SF Symbols.
enum Lucide {
    static func symbol(_ name: String) -> String {
        switch name {
        case "type": "text.alignleft"
        case "heading-1": "textformat.size.larger"
        case "heading-2": "textformat.size"
        case "heading-3": "textformat.size.smaller"
        case "list": "list.bullet"
        case "list-ordered": "list.number"
        case "square-check": "checkmark.square"
        case "quote": "text.quote"
        case "info": "info.circle"
        case "chevron-right": "chevron.right.square"
        case "code": "chevron.left.forwardslash.chevron.right"
        case "minus": "minus"
        case "table": "tablecells"
        case "chart-column": "chart.bar"
        case "sigma": "function"
        case "workflow": "point.3.connected.trianglepath.dotted"
        case "layout-grid": "square.grid.2x2"
        case "file-plus": "doc.badge.plus"
        case "image": "photo"
        case "play": "play.rectangle"
        case "globe": "globe"
        case "paperclip": "paperclip"
        case "audio-lines": "waveform"
        case "sparkles": "sparkles"
        case "file-text": "doc.text"
        case "user": "person"
        case "calendar": "calendar"
        case "lightbulb": "lightbulb"
        case "square-pen": "square.and.pencil"
        default: "rectangle.3.group"
        }
    }
}
