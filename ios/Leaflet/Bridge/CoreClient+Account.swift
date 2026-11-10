import Foundation
import LeafletCore

struct SupabaseSignIn: Codable, Sendable {
    var url: URL
    var state: String
    var verifier: String
}

struct SignedIn: Codable, Sendable {
    var email: String
    var lost: Int
}

struct AccountInfo: Codable, Sendable {
    var signedIn: Bool
    var email: String
    var displayName: String
    var project: String
    var projectRef: String
    var projectNeedsSignIn: Bool

    enum CodingKeys: String, CodingKey {
        case email, project
        case signedIn = "signed_in"
        case displayName = "display_name"
        case projectRef = "project_ref"
        case projectNeedsSignIn = "project_needs_sign_in"
    }
}

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

struct ExportedAll: Codable, Sendable {
    var path: String
    var name: String
    var missing: Int
    var stale: Int

    var url: URL { URL(fileURLWithPath: path) }
}

extension CoreClient {
    func beginSupabaseSignIn() throws -> SupabaseSignIn {
        try decode(SupabaseSignIn.self, try Self.answer { e in MobileBeginSupabaseSignIn(e) })
    }

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

    nonisolated(nonsending) func finishProjectSignIn(_ begun: SupabaseSignIn, callback: URL)
        async throws
    {
        let url = callback.absoluteString
        try await call(waiting: true) {
            try $0.finishProjectSign(in: url, state: begun.state, verifier: begun.verifier)
        }
    }

    nonisolated(nonsending) func confirmSignIn(anyway: Bool) async throws {
        try await call { try $0.confirmSign(in: anyway) }
    }

    nonisolated(nonsending) func signOut(anyway: Bool) async throws -> Int {
        try await call(waiting: true) { core in
            var unsynced = 0
            try core.signOut(anyway, unsynced: &unsynced)
            return unsynced
        }
    }

    nonisolated(nonsending) func account() async throws -> AccountInfo {
        try await call(AccountInfo.self) { core in try Self.answer { e in core.account(e) } }
    }

    nonisolated(nonsending) func setDisplayName(_ name: String) async throws {
        try await call(waiting: true) { try $0.setDisplayName(name) }
    }

    nonisolated(nonsending) func devices() async throws -> [Device] {
        try await call([Device].self, waiting: true) { core in
            try Self.answer { e in core.devices(e) }
        }
    }

    nonisolated(nonsending) func signOutDevice(_ id: String) async throws {
        try await call(waiting: true) { try $0.signOutDevice(id) }
    }

    nonisolated(nonsending) func deleteAccount() async throws {
        try await call(waiting: true) { try $0.deleteAccount() }
    }

    nonisolated(nonsending) func exportAll(into dir: URL) async throws -> ExportedAll {
        let path = dir.path(percentEncoded: false)
        return try await call(ExportedAll.self, waiting: true) { core in
            try Self.answer { e in core.exportAll(path, error: e) }
        }
    }
}
