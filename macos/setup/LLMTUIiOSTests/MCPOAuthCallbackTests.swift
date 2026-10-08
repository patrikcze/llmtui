import Foundation
import Testing
@testable import LLMTUIiOS

struct MCPOAuthCallbackTests {
    @Test func OAuthCallbackAndPKCEValidation() throws {
        let state = try MCPOAuth.random()
        #expect(state.count >= 43)
        #expect(MCPOAuth.base64url(Data([255, 254])) == "__4")
        let callback = URL(string: MCPOAuth.redirect + "?code=code&state=\(state)&iss=https%3A%2F%2Fissuer.example")!
        #expect(try MCPOAuth.validateCallback(callback, state: state, issuer: "https://issuer.example", issuerRequired: true) == "code")
        #expect(throws: MobileMCPError.self) { try MCPOAuth.validateCallback(callback, state: "wrong", issuer: "https://issuer.example", issuerRequired: true) }
        #expect(throws: MobileMCPError.self) { try MCPOAuth.validateCallback(callback, state: state, issuer: "https://different.example", issuerRequired: true) }
        #expect(throws: MobileMCPError.self) { try MCPOAuth.validateCallback(URL(string: MCPOAuth.redirect + "?code=c&state=\(state)&state=\(state)")!, state: state, issuer: "", issuerRequired: false) }
        #expect(throws: MobileMCPError.self) { try MCPOAuth.https("http://issuer.example") }
    }
}
