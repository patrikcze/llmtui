import Foundation
import Security

struct MobileProviderStore: Sendable {
    private static let profilesKey = "iosProviderProfiles"
    private static let activeIDKey = "iosActiveProviderID"
    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func loadProfiles() -> [MobileProviderProfile] {
        guard let data = defaults.data(forKey: Self.profilesKey),
              let profiles = try? JSONDecoder().decode([MobileProviderProfile].self, from: data)
        else { return [] }
        return profiles
    }

    func saveProfiles(_ profiles: [MobileProviderProfile]) throws {
        defaults.set(try JSONEncoder().encode(profiles), forKey: Self.profilesKey)
    }

    func loadActiveID() -> UUID? {
        defaults.string(forKey: Self.activeIDKey).flatMap(UUID.init(uuidString:))
    }

    func saveActiveID(_ id: UUID?) {
        defaults.set(id?.uuidString, forKey: Self.activeIDKey)
    }
}

struct KeychainCredentialStore: Sendable {
    private let service = "com.patriknakladalpersonalteam.LLMTUIiOS.provider-api-key"

    func apiKey(for profileID: UUID) throws -> String {
        let query: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: profileID.uuidString,
            kSecReturnData: true,
            kSecMatchLimit: kSecMatchLimitOne
        ]
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return "" }
        guard status == errSecSuccess,
              let data = result as? Data,
              let value = String(data: data, encoding: .utf8) else {
            throw KeychainError(status: status)
        }
        return value
    }

    func setAPIKey(_ apiKey: String, for profileID: UUID) throws {
        let identity: [CFString: Any] = [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: profileID.uuidString
        ]
        let trimmed = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            let status = SecItemDelete(identity as CFDictionary)
            guard status == errSecSuccess || status == errSecItemNotFound else {
                throw KeychainError(status: status)
            }
            return
        }

        let data = Data(trimmed.utf8)
        let updateStatus = SecItemUpdate(identity as CFDictionary, [kSecValueData: data] as CFDictionary)
        if updateStatus == errSecItemNotFound {
            var item = identity
            item[kSecValueData] = data
            item[kSecAttrAccessible] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let addStatus = SecItemAdd(item as CFDictionary, nil)
            guard addStatus == errSecSuccess else { throw KeychainError(status: addStatus) }
        } else if updateStatus != errSecSuccess {
            throw KeychainError(status: updateStatus)
        }
    }

    func removeAPIKey(for profileID: UUID) throws {
        try setAPIKey("", for: profileID)
    }
}

struct KeychainError: LocalizedError {
    let status: OSStatus

    var errorDescription: String? {
        SecCopyErrorMessageString(status, nil) as String? ?? "Keychain error \(status)"
    }
}
