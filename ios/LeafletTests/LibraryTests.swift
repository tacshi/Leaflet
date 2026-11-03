import Foundation
import SwiftUI
import Testing

@testable import Leaflet

/// LibraryTests cover the library as the sidebar arranges it, a window's
/// pages before and after, and the values the app keeps as JSON, apart
/// from the core.
@MainActor
struct LibraryTests {
    private func page(
        _ id: String, parent: String? = nil, folder: String? = nil, space: String = "me",
        key: String = "a", template: Bool = false, tags: [String] = [], updated: Int64 = 0
    ) -> PageRow {
        PageRow(
            id: id, spaceID: space, parentID: parent, folderID: folder, title: id, icon: nil,
            sortKey: key, updatedAt: updated, keepOffline: false, template: template, tags: tags,
            canEdit: true, createdBy: nil, defaultTemplateID: nil)
    }

    @Test func pagesNestAndSortAsTheDesktopsTree() {
        let lib = LibrarySnapshot(
            spaceID: "me",
            pages: [
                page("b", key: "b"), page("a", key: "a"), page("a2", parent: "a", key: "b"),
                page("a1", parent: "a", key: "a"), page("f1", folder: "f"),
                page("t", template: true), page("s", space: "them"),
                page("s1", parent: "s", space: "them"), page("lone", parent: "gone", space: "them"),
            ],
            folders: [FolderRow(id: "f", name: "Work", sortKey: "a")], tags: [])
        #expect(lib.topPages.map(\.id) == ["a", "b"])
        #expect(lib.children(of: page("a")).map(\.id) == ["a1", "a2"])
        #expect(lib.pages(in: lib.sortedFolders[0]).map(\.id) == ["f1"])
        // A shared page whose parent the user has not is a top one.
        #expect(lib.sharedPages.map(\.id) == ["lone", "s"])
        #expect(lib.page("a1")?.parentID == "a")
        #expect(lib.page("nope") == nil)
    }

    /// A page whose parents go round in a loop, as two devices' moves can
    /// leave them, still has a path, which ends.
    @Test func aLoopOfParentsEnds() {
        let lib = LibrarySnapshot(
            spaceID: "me", pages: [page("x", parent: "y"), page("y", parent: "x")], folders: [],
            tags: [])
        #expect(lib.path(to: page("x", parent: "y")).map(\.id) == ["y"])
        #expect(lib.topPages.isEmpty)
    }

    @Test func taggedPagesAreTheLastChangedFirst() {
        let lib = LibrarySnapshot(
            spaceID: "me",
            pages: [
                page("old", tags: ["home"], updated: 1), page("new", tags: ["home"], updated: 2),
                page("t", template: true, tags: ["home"], updated: 3),
            ], folders: [], tags: [TagRow(tag: "home", count: 2)])
        #expect(lib.pages(tagged: "home").map(\.id) == ["new", "old"])
        #expect(lib.pages(tagged: "work").isEmpty)
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
