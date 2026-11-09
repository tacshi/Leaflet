import Foundation
import SwiftUI
import Testing

@testable import Leaflet

/// LibraryTests cover the library as the sidebar arranges it, a window's
/// pages before and after, and the values the app keeps as JSON, apart
/// from the core.
@MainActor
struct LibraryTests {
    private func page(_ id: String, parent: String? = nil) -> PageRow {
        PageRow(
            id: id, spaceID: "me", parentID: parent, folderID: nil, title: id, icon: nil,
            sortKey: "a", updatedAt: 0, keepOffline: false, template: false, tags: [],
            canEdit: true, createdBy: nil, defaultTemplateID: nil)
    }

    /// The library reads as the core arranges it: each list of the tree
    /// its pages, in the core's order, and an ID it has no page of left
    /// out.
    @Test func theCoresTreeReadsAsItsPages() throws {
        func row(_ id: String, parent: String? = nil) -> String {
            let p = parent.map { #","parent_id":"\#($0)""# } ?? ""
            return #"""
                {"id":"\#(id)","space_id":"me","title":"\#(id)","sort_key":"a","updated_at":0,"keep_offline":false,"template":false,"tags":[],"can_edit":true\#(p)}
                """#
        }
        let json = """
            {"space_id":"me","pages":[\(row("b")),\(row("a")),\(row("a1", parent: "a")),\(row("f1")),\(row("s"))],
            "folders":[{"id":"f","name":"Work","sort_key":"a"}],
            "tags":[{"tag":"home","count":2,"pages":["b","a"]}],
            "files":[{"id":"in","folder_id":"f","name":"a.pdf","size":1},
            {"id":"left","folder_id":"deleted","name":"b.pdf","size":2},
            {"id":"top","name":"c.pdf","size":3}],
            "top":["a","b","gone"],"in_folder":{"f":["f1"]},"children":{"a":["a1"]},"shared":["s"]}
            """
        let lib = try JSONDecoder().decode(LibrarySnapshot.self, from: Data(json.utf8))
        #expect(lib.topPages.map(\.id) == ["a", "b"])
        #expect(lib.children(of: page("a")).map(\.id) == ["a1"])
        #expect(lib.pages(in: lib.folders[0]).map(\.id) == ["f1"])
        #expect(lib.sharedPages.map(\.id) == ["s"])
        #expect(lib.pages(tagged: "home").map(\.id) == ["b", "a"])
        #expect(lib.pages(tagged: "work").isEmpty)
        #expect(lib.page("a1")?.parentID == "a")
        #expect(lib.page("nope") == nil)
        #expect(lib.files(in: lib.folders[0]).map(\.id) == ["in"])
        #expect(lib.topFiles.map(\.id) == ["left", "top"])
    }

    /// The tree's rows are the pages and, under each one open, its
    /// subpages a level deeper: closing a page takes away all under it,
    /// and pages nested in a loop come once.
    @Test func theTreesRowsFollowThePagesOpen() {
        let lib = LibrarySnapshot(
            spaceID: "me",
            pages: [
                page("a"), page("a1", parent: "a"), page("a2", parent: "a"),
                page("a1x", parent: "a1"), page("b"),
            ], folders: [], tags: [], top: ["a", "b"],
            children: ["a": ["a1", "a2"], "a1": ["a1x", "a"]])
        func rows(_ open: Set<String>) -> [String] {
            lib.rows(of: lib.topPages, open: open).map {
                "\($0.page.id):\($0.depth)\($0.folds ? "+" : "")"
            }
        }
        #expect(rows([]) == ["a:0+", "b:0"])
        #expect(rows(["a"]) == ["a:0+", "a1:1+", "a2:1", "b:0"])
        #expect(rows(["a", "a1"]) == ["a:0+", "a1:1+", "a1x:2", "a2:1", "b:0"])
        #expect(rows(["a1"]) == ["a:0+", "b:0"])
    }

    /// A page whose parents go round in a loop, as two devices' moves can
    /// leave them, still has a path, which ends.
    @Test func aLoopOfParentsEnds() {
        let lib = LibrarySnapshot(
            spaceID: "me", pages: [page("x", parent: "y"), page("y", parent: "x")], folders: [],
            tags: [])
        #expect(lib.path(to: page("x", parent: "y")).map(\.id) == ["y"])
    }

    /// Back and Forward leave out the pages deleted, and a window whose
    /// page goes shows the one before it.
    @Test func aWindowKeepsOnlyThePagesStillThere() {
        let scene = SceneModel()
        scene.open("a")
        scene.open("b")
        scene.open("c")
        scene.goBack()
        #expect(scene.selection == "b")
        #expect(scene.canGoForward)
        scene.keep(["a", "b"])
        #expect(!scene.canGoForward)
        scene.keep(["a"])
        #expect(scene.selection == "a")
        #expect(!scene.canGoBack)
    }

    /// A block's data reads back as it was written, whatever its values.
    @Test func jsonValuesRoundTrip() throws {
        let value = JSONValue.object([
            "n": .null, "b": .bool(true), "i": .int(42), "d": .double(1.5), "s": .string("é"),
            "a": .array([.int(1), .string("x")]), "o": .object(["k": .bool(false)]),
        ])
        let data = try JSONEncoder().encode(value)
        #expect(try JSONDecoder().decode(JSONValue.self, from: data) == value)
        #expect(value["i"]?.int == 42)
        #expect(value["d"]?.double == 1.5)
    }

    /// A bookmark of the iCloud Drive folder that no longer opens has the
    /// user pick it again, rather than sync through nothing.
    @Test func aBookmarkThatNoLongerOpensIsLost() throws {
        let suite = "LibraryTests-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set(Data("not a bookmark".utf8), forKey: "icloud.folder")
        let folder = ICloudFolder(defaults: defaults)
        #expect(folder.lost)
        #expect(folder.root == nil)
    }

    /// A tag's color is the core's, whatever its case, for every device.
    @Test func tagsAreColoredByTheCore() {
        #expect(TagChip<EmptyView>.color("Work") == TagChip<EmptyView>.color("WORK"))
    }
}
