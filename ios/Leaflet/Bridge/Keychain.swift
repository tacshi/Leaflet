import Foundation
import Security

/// Keychain keeps the core's secrets (sign-ins, API keys) as generic
/// passwords of the app, on this device only, readable once it has been
/// unlocked after a restart, so sync can run in the background.
struct Keychain: Sendable {
    var service: String

    /// Failure is a status SecItem failed with. Its description always
    /// ends with the status, as "(OSStatus -128)": the core tells a prompt
    /// turned down, which it doesn't ask again, from a device still locked,
    /// which it tries again, by it (mobile's keychainError).
    struct Failure: LocalizedError {
        var status: OSStatus
        var errorDescription: String? {
            let message = SecCopyErrorMessageString(status, nil) as String? ?? "Keychain error"
            return "\(message) (OSStatus \(status))"
        }
    }

    private func query(_ name: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: name,
        ]
    }

    /// get returns the secret under name, or nil when there is none.
    func get(_ name: String) throws -> Data? {
        var q = query(name)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        switch status {
        case errSecSuccess: return out as? Data
        case errSecItemNotFound: return nil
        default: throw Failure(status: status)
        }
    }

    /// set keeps value under name, in place of what it had.
    func set(_ name: String, _ value: Data) throws {
        let attrs: [String: Any] = [
            kSecValueData as String: value,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        var status = SecItemUpdate(query(name) as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            status = SecItemAdd(query(name).merging(attrs) { $1 } as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw Failure(status: status) }
    }

    /// delete removes the secret under name, if there is one.
    func delete(_ name: String) throws {
        let status = SecItemDelete(query(name) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw Failure(status: status)
        }
    }
}
