import Testing

@testable import Leaflet

/// OutlineTests check the outline nests headings as the desktop's does.
struct OutlineTests {
    private func heading(_ id: String) -> DocNode {
        DocNode(id: id, type: "heading", data: nil, children: nil)
    }

    @Test func headingsNestUnderTheLastOfAHigherLevel() {
        let rows = OutlineRow.rows([
            (heading("a"), 1), (heading("b"), 2), (heading("c"), 3), (heading("d"), 2),
            (heading("e"), 1), (heading("f"), 3),
        ])
        #expect(rows.map(\.parent) == [nil, "a", "b", "a", nil, "e"])
        #expect(rows.map(\.depth) == [0, 1, 2, 1, 0, 1])
        #expect(rows.map(\.hasChildren) == [true, true, false, false, true, false])
    }

    @Test func headingsOfOneLevelStayFlat() {
        let rows = OutlineRow.rows([(heading("a"), 2), (heading("b"), 2)])
        #expect(rows.allSatisfy { $0.parent == nil && !$0.hasChildren })
    }
}
