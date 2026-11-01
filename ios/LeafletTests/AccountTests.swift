import Foundation
import Testing

@testable import Leaflet

/// AccountTests drive signing in and out through the bridge, as far as
/// they go offline: the sign-in itself needs Supabase and a device.
@MainActor
struct AccountTests {
    let client: CoreClient

    init() throws {
        let dir = FileManager.default.temporaryDirectory
            .appending(path: "AccountTests-\(UUID().uuidString)", directoryHint: .isDirectory)
        client = try CoreClient.open(dataDir: dir, iCloudRoot: nil, locale: "en")
    }

    @Test func beginsAtSupabaseWithTheWorkersRedirect() throws {
        let begun = try client.beginSupabaseSignIn()
        let query = URLComponents(url: begun.url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        let item = { (name: String) in query.first { $0.name == name }?.value }
        #expect(item("state") == begun.state)
        #expect(item("redirect_uri") == "https://leaflet-auth.capyvibe.app/auth/supabase")
        #expect(!begun.verifier.isEmpty)
    }

    @Test func refusesAStaleAnswer() async throws {
        let begun = try client.beginSupabaseSignIn()
        let answer = URL(
            string: "https://leaflet-auth.capyvibe.app/auth/supabase?state=other&code=c")!
        await #expect(throws: (any Error).self) {
            try await client.finishSupabaseSignIn(begun, callback: answer)
        }
        #expect(try await client.account().signedIn == false)
    }

    @Test func signingOutLeavesTheDevicesOwnPages() async throws {
        let page = try await client.createPage(NewPage(title: "Mine"))
        #expect(try await client.signOut(anyway: true) == 0)
        #expect(try await client.library().pages.contains { $0.id == page.id })
    }
}
