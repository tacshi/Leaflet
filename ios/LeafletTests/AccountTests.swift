import Foundation
import Testing

@testable import Leaflet

@MainActor
struct AccountTests {
    let core: ScratchCore
    var client: CoreClient { core.client }

    init() throws {
        core = try ScratchCore("AccountTests")
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

    @Test func signingOutWhileSignedOutKeepsThePages() async throws {
        let page = try await client.createPage(NewPage(title: "Mine"))
        #expect(try await client.signOut(anyway: true) == 0)
        #expect(try await client.account().signedIn == false)
        #expect(try await client.library().pages.contains { $0.id == page.id })
    }
}
