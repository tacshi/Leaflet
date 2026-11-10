import Testing

@testable import Leaflet

struct OutlineTests {
    private func heading(_ id: String, _ level: Int, _ text: String = "") -> DocNode {
        DocNode(
            id: id, type: "heading",
            data: [
                "level": .int(Int64(level)),
                "delta": .array([.object(["insert": .string(text)])]),
            ],
            children: nil)
    }

    @Test func headingsNestUnderTheLastOfAHigherLevel() {
        let rows = OutlineRow.rows([
            heading("a", 1, "Plan"), heading("b", 2), heading("c", 3), heading("d", 2),
            heading("e", 1), heading("f", 3),
        ])
        #expect(rows.map(\.id) == ["a", "b", "c", "d", "e", "f"])
        #expect(rows.map(\.parent) == [nil, "a", "b", "a", nil, "e"])
        #expect(rows.map(\.depth) == [0, 1, 2, 1, 0, 1])
        #expect(rows.map(\.hasChildren) == [true, true, false, false, true, false])
        #expect(rows.first?.text == "Plan")
        #expect(rows.first?.level == 1)
    }

    @Test func headingsOfOneLevelStayFlat() {
        let rows = OutlineRow.rows([heading("a", 2), heading("b", 2)])
        #expect(rows.count == 2)
        #expect(rows.allSatisfy { $0.parent == nil && !$0.hasChildren })
    }
}
