import Foundation
import Security

nonisolated struct MCPCredentials: Codable, Sendable {
    var accessToken = ""
    var refreshToken = ""
    var expiresAt: Date?
    var issuer = ""
    var resource = ""
    var tokenEndpoint = ""
    var clientID = ""
    var scopes = ""
}
nonisolated protocol MCPCredentialStoring: Sendable {
    func load(_ id: UUID) throws -> MCPCredentials
    func save(_ value: MCPCredentials, id: UUID) throws
    func remove(_ id: UUID) throws
}
nonisolated struct MCPCredentialStore: MCPCredentialStoring {
    private let service = "com.patriknakladalpersonalteam.LLMTUIiOS.mcp-credentials"
    private func identity(_ id: UUID) -> [CFString: Any] {
        [kSecClass: kSecClassGenericPassword, kSecAttrService: service, kSecAttrAccount: id.uuidString]
    }
    func load(_ id: UUID) throws -> MCPCredentials {
        var query = identity(id)
        query[kSecReturnData] = true; query[kSecMatchLimit] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status == errSecItemNotFound { return MCPCredentials() }
        guard status == errSecSuccess, let data = item as? Data else { throw KeychainError(status: status) }
        return try JSONDecoder().decode(MCPCredentials.self, from: data)
    }
    func save(_ value: MCPCredentials, id: UUID) throws {
        let data = try JSONEncoder().encode(value)
        let query = identity(id)
        let status = SecItemUpdate(query as CFDictionary, [kSecValueData: data] as CFDictionary)
        if status == errSecItemNotFound {
            var item = query; item[kSecValueData] = data
            item[kSecAttrAccessible] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let added = SecItemAdd(item as CFDictionary, nil)
            guard added == errSecSuccess else { throw KeychainError(status: added) }
        } else if status != errSecSuccess { throw KeychainError(status: status) }
    }
    func remove(_ id: UUID) throws {
        let status = SecItemDelete(identity(id) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainError(status: status) }
    }
}

nonisolated struct MobileMCPProfileStore {
    var defaults = UserDefaults.standard
    func load() -> [MobileMCPProfile] {
        guard let data = defaults.data(forKey: "iosMCPProfiles") else { return [] }
        return (try? JSONDecoder().decode([MobileMCPProfile].self, from: data)) ?? []
    }
    func save(_ profiles: [MobileMCPProfile]) throws { defaults.set(try JSONEncoder().encode(profiles), forKey: "iosMCPProfiles") }
}
