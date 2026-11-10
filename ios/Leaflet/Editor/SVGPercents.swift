import Foundation

enum SVGPercents {
    static func resolved(_ svg: Data) -> Data {
        guard let text = String(data: svg, encoding: .utf8), text.contains("%"),
            let (width, height) = viewport(text)
        else { return svg }
        let shape = /<(?:rect|circle|ellipse|line|image|use)\b[^>]*>/
        let length =
            /(?<space>\s)(?<name>x|y|width|height|cx|cy|r|rx|ry|x1|y1|x2|y2)(?<equals>\s*=\s*)(?<quote>["'])\s*(?<value>[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?)%\s*\k<quote>/
        let out = text.replacing(shape) { tag in
            String(tag.output).replacing(length) { match in
                let o = match.output
                guard let percent = Double(o.value) else { return String(o.0) }
                let side =
                    switch o.name {
                    case "x", "width", "cx", "rx", "x1", "x2": width
                    case "y", "height", "cy", "ry", "y1", "y2": height
                    default: ((width * width + height * height) / 2).squareRoot()
                    }
                return "\(o.space)\(o.name)\(o.equals)\(o.quote)\(percent / 100 * side)\(o.quote)"
            }
        }
        return Data(out.utf8)
    }

    private static func viewport(_ text: String) -> (Double, Double)? {
        guard let root = text.firstMatch(of: /<svg\b[^>]*>/).map({ String($0.output) }) else {
            return nil
        }
        let number = "[-+]?(?:\\d+\\.?\\d*|\\.\\d+)(?:[eE][-+]?\\d+)?"
        var size: (Double, Double)?
        if let box = try? Regex(
            "\\sviewBox\\s*=\\s*[\"']\\s*\(number)[\\s,]+\(number)[\\s,]+(\(number))[\\s,]+(\(number))"
        ), let m = root.firstMatch(of: box),
            let w = m.output[1].substring.flatMap({ Double($0) }),
            let h = m.output[2].substring.flatMap({ Double($0) })
        {
            size = (w, h)
        } else {
            func attribute(_ name: String) -> Double? {
                let source = "\\s\(name)\\s*=\\s*[\"']\\s*(\(number))(?:px)?\\s*[\"']"
                guard let pattern = try? Regex(source), let m = root.firstMatch(of: pattern)
                else { return nil }
                return m.output[1].substring.flatMap { Double($0) }
            }
            if let w = attribute("width"), let h = attribute("height") {
                size = (w, h)
            }
        }
        guard let size, size.0 > 0, size.1 > 0 else { return nil }
        return size
    }
}
