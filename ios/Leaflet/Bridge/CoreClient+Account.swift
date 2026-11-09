import Foundation
import LeafletCore

/// SupabaseSignIn is a sign-in with Supabase begun: the page the web
/// session opens, and what finishing checks its answer against.
struct SupabaseSignIn: Codable, Sendable {
    var url: URL
    var state: String
    var verifier: String
}

/// SignedIn is how a sign-in ended. While lost is more than 0, it waits
/// for confirmSignIn: signing in as email removes that many changes of the
/// account signed in here before, which never went up.
struct SignedIn: Codable, Sendable {
    var email: String
    var lost: Int
}

/// AccountInfo is who is signed in, and the Supabase project Leaflet
/// syncs through.
struct AccountInfo: Codable, Sendable {
    var signedIn: Bool
    var email: String
    var displayName: String
    var project: String
    var projectRef: String
    /// projectNeedsSignIn says the project waits to be brought up to this
    /// version of Leaflet for the person to sign in to Supabase again.
    var projectNeedsSignIn: Bool

    enum CodingKeys: String, CodingKey {
        case email, project
        case signedIn = "signed_in"
        case displayName = "display_name"
        case projectRef = "project_ref"
        case projectNeedsSignIn = "project_needs_sign_in"
    }
}

/// Device is a device signed in to the account, as Account › Devices
/// lists it: this one, or another to sign out, unless one was already.
struct Device: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var name: String
    var lastActive: Date
    var this: Bool
    var revoked: Bool

    enum CodingKeys: String, CodingKey {
        case id, name, this, revoked
        case lastActive = "last_active"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        this = try c.decode(Bool.self, forKey: .this)
        revoked = try c.decode(Bool.self, forKey: .revoked)
        lastActive = Date(
            timeIntervalSince1970: TimeInterval(try c.decode(Int64.self, forKey: .lastActive))
                / 1000)
    }
}

/// ExportedAll is Export all's zip, in the folder it was written to, for
/// the user to move where they pick; missing counts the pages it left out,
/// which this device does not have and could not download, and stale the
/// pages that could not be brought up to date, which went as this device
/// last had them.
struct ExportedAll: Codable, Sendable {
    var path: String
    var name: String
    var missing: Int
    var stale: Int

    var url: URL { URL(fileURLWithPath: path) }
}

extension CoreClient {
    // MARK: Account

    /// beginSupabaseSignIn begins signing in with Supabase.
    func beginSupabaseSignIn() throws -> SupabaseSignIn {
        try decode(SupabaseSignIn.self, try Self.answer { e in MobileBeginSupabaseSignIn(e) })
    }

    /// finishSupabaseSignIn finishes the sign-in begun with the URL the web
    /// session came back to: it finds the Leaflet project the desktop app
    /// set up and signs in to it.
    nonisolated(nonsending) func finishSupabaseSignIn(_ begun: SupabaseSignIn, callback: URL)
        async throws -> SignedIn
    {
        let url = callback.absoluteString
        return try await call(SignedIn.self, waiting: true) { core in
            try Self.answer { e in
                core.finishSupabaseSign(
                    in: url, state: begun.state, verifier: begun.verifier, error: e)
            }
        }
    }

    /// finishProjectSignIn finishes signing in to Supabase again for the
    /// project in use, with the URL the web session came back to: the
    /// core brings the project up to date and keeps the sign-in.
    nonisolated(nonsending) func finishProjectSignIn(_ begun: SupabaseSignIn, callback: URL)
        async throws
    {
        let url = callback.absoluteString
        try await call(waiting: true) {
            try $0.finishProjectSign(in: url, state: begun.state, verifier: begun.verifier)
        }
    }

    /// confirmSignIn answers a sign-in that waits: anyway signs in,
    /// removing the changes it counted; otherwise the new account signs
    /// out again.
    nonisolated(nonsending) func confirmSignIn(anyway: Bool) async throws {
        try await call { try $0.confirmSign(in: anyway) }
    }

    /// signOut uploads the changes waiting, then signs out and removes the
    /// account's pages from this device. It returns how many changes could
    /// not go up, without signing out, to ask; anyway signs out all the
    /// same.
    nonisolated(nonsending) func signOut(anyway: Bool) async throws -> Int {
        try await call(waiting: true) { core in
            var unsynced = 0
            // gomobile labels the count as Go names it.
            try core.signOut(anyway, unsynced: &unsynced)
            return unsynced
        }
    }

    nonisolated(nonsending) func account() async throws -> AccountInfo {
        try await call(AccountInfo.self) { core in try Self.answer { e in core.account(e) } }
    }

    /// setDisplayName changes the name shown on the user's comments and
    /// edits, here and, signed in, on the server.
    nonisolated(nonsending) func setDisplayName(_ name: String) async throws {
        try await call(waiting: true) { try $0.setDisplayName(name) }
    }

    /// devices are the devices signed in to the account, the one active
    /// last first.
    nonisolated(nonsending) func devices() async throws -> [Device] {
        try await call([Device].self, waiting: true) { core in
            try Self.answer { e in core.devices(e) }
        }
    }

    nonisolated(nonsending) func signOutDevice(_ id: String) async throws {
        try await call(waiting: true) { try $0.signOutDevice(id) }
    }

    /// deleteAccount deletes the account on the server, then signs out
    /// and wipes this device.
    nonisolated(nonsending) func deleteAccount() async throws {
        try await call(waiting: true) { try $0.deleteAccount() }
    }

    /// exportAll writes every page as Markdown in a zip, into dir.
    nonisolated(nonsending) func exportAll(into dir: URL) async throws -> ExportedAll {
        let path = dir.path(percentEncoded: false)
        return try await call(ExportedAll.self, waiting: true) { core in
            try Self.answer { e in core.exportAll(path, error: e) }
        }
    }
}
