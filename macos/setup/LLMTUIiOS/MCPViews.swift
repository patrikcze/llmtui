import SwiftUI

struct MCPServerListScreen: View {
    let controller: MobileMCPController
    @State private var edit: MobileMCPProfile?
    @State private var delete: MobileMCPProfile?
    @State private var error: String?
    var body: some View {
        List {
            Section {
                ForEach(controller.profiles) { profile in
                    NavigationLink {
                        MCPServerDetailScreen(profile: profile, controller: controller)
                    } label: {
                        VStack(alignment: .leading, spacing: 5) {
                            Text(profile.name)
                            Text(controller.busy.contains(profile.id) ? "Connecting…" : controller.connections[profile.id] != nil ? "Connected" : "Disconnected")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .swipeActions {
                        Button("Delete", role: .destructive) { delete = profile }
                        Button("Edit") { edit = profile }.tint(.blue)
                    }
                }
            } footer: {
                Text("Connect explicitly, then select servers in each chat. Every external tool call asks for approval. Servers stay connected while the app is in the background; a reply that was using one stops and is not resumed.")
            }
            if controller.profiles.isEmpty { Text("Add a server with a Streamable HTTP endpoint.").foregroundStyle(.secondary) }
        }
        .navigationTitle("MCP Servers")
        .toolbar { Button("Add server", systemImage: "plus") { edit = MobileMCPProfile() }.accessibilityIdentifier("mcp.add") }
        .sheet(item: $edit) { profile in MCPServerEditor(profile: profile, controller: controller) }
        .confirmationDialog("Delete MCP server and its credentials?", isPresented: Binding(get: { delete != nil }, set: { if !$0 { delete = nil } })) {
            Button("Delete", role: .destructive) {
                if let delete { do { try controller.remove(delete.id) } catch { self.error = "Could not delete the server." } }
                delete = nil
            }
        }
        .alert("MCP", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) { Button("OK") { error = nil } } message: { Text(error ?? "") }
    }
}

private struct MCPServerDetailScreen: View {
    let profile: MobileMCPProfile
    let controller: MobileMCPController
    @State private var editing = false
    private var current: MobileMCPProfile { controller.profiles.first(where: { $0.id == profile.id }) ?? profile }
    var body: some View {
        Form {
            Section("Connection") {
                Text(current.endpoint).font(.caption).textSelection(.enabled)
                if let info = controller.connections[profile.id] {
                    LabeledContent("Protocol", value: info.version)
                    Button("Disconnect") { controller.disconnect(profile.id) }
                } else {
                    Button(controller.busy.contains(profile.id) ? "Cancel connection" : "Connect") {
                        if controller.busy.contains(profile.id) { controller.disconnect(profile.id) } else { controller.connect(profile.id) }
                    }.accessibilityIdentifier("mcp.connect")
                }
                if current.authentication == .oauth {
                    Button("Sign in / update permissions") { controller.connect(profile.id, signIn: true) }.disabled(controller.busy.contains(profile.id))
                    Button("Sign out") { controller.signOut(profile.id) }
                }
                if let error = controller.errors[profile.id] { Text(error).foregroundStyle(.red) }
            }
            if let info = controller.connections[profile.id] {
                Section("Tools (\(info.tools.count))") {
                    ForEach(info.tools) { tool in
                        DisclosureGroup(tool.name) {
                            Text(tool.description ?? "No description").font(.caption)
                            Text(String(decoding: (try? tool.inputSchema.data()) ?? Data(), as: UTF8.self)).font(.caption.monospaced()).textSelection(.enabled)
                        }
                    }
                    ForEach(info.warnings, id: \.self) { Text($0).font(.caption).foregroundStyle(.orange) }
                }
            }
        }
        .navigationTitle(current.name)
        .toolbar { Button("Edit") { editing = true } }
        .sheet(isPresented: $editing) { MCPServerEditor(profile: controller.profiles.first(where: { $0.id == profile.id }) ?? profile, controller: controller) }
    }
}

private struct MCPServerEditor: View {
    @State var profile: MobileMCPProfile
    let controller: MobileMCPController
    @Environment(\.dismiss) private var dismiss
    @State private var token = ""
    @State private var replaceToken = false
    @State private var error: String?
    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    TextField("Name", text: $profile.name).accessibilityIdentifier("mcp.name")
                    TextField("MCP endpoint URL", text: $profile.endpoint).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL).accessibilityIdentifier("mcp.endpoint")
                    Toggle("Allow unauthenticated LAN HTTP", isOn: $profile.allowLANHTTP)
                    if profile.allowLANHTTP { Text("HTTP is unencrypted. Only explicitly configured local-network endpoints without credentials are allowed.").font(.caption).foregroundStyle(.orange) }
                }
                Section("Authentication") {
                    Picker("Authentication", selection: $profile.authentication) {
                        Text("None").tag(MobileMCPAuth.none)
                        Text("Bearer token").tag(MobileMCPAuth.bearer)
                        Text("OAuth browser sign-in").tag(MobileMCPAuth.oauth)
                    }
                    if profile.authentication == .bearer {
                        Toggle("Set or replace token", isOn: $replaceToken)
                        if replaceToken || !controller.profiles.contains(where: { $0.id == profile.id }) {
                            SecureField("Bearer token", text: $token)
                        }
                    }
                    if profile.authentication == .oauth {
                        TextField("Registered client ID (optional)", text: $profile.clientID).textInputAutocapitalization(.never).autocorrectionDisabled()
                        TextField("Client metadata HTTPS URL (optional)", text: $profile.clientMetadataURL).textInputAutocapitalization(.never).autocorrectionDisabled()
                        Text("Callback: \(MCPOAuth.redirect). When no client ID is configured, the app tries supported metadata registration or dynamic registration. Sign in after saving.").font(.caption).textSelection(.enabled)
                    }
                }
                if let error { Text(error).foregroundStyle(.red) }
            }
            .navigationTitle("MCP Server")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        do {
                            let new = !controller.profiles.contains(where: { $0.id == profile.id })
                            try controller.save(profile, bearer: profile.authentication == .bearer && (replaceToken || new) ? token : nil)
                            dismiss()
                        } catch { self.error = error.localizedDescription }
                    }.accessibilityIdentifier("mcp.save")
                }
            }
        }
    }
}

struct MCPChatServerPicker: View {
    let model: MobileAppModel
    var body: some View {
        Menu {
            ForEach(model.mcp.profiles) { profile in
                let selected = model.selectedMCPServerIDs.contains(profile.id)
                Button {
                    model.setMCPServer(profile.id, selected: !selected)
                } label: {
                    Label(profile.name + (model.mcp.connections[profile.id] == nil ? " (disconnected)" : ""), systemImage: selected ? "checkmark.circle.fill" : "circle")
                }
            }
            if model.mcp.profiles.isEmpty { Text("Add servers in Settings → MCP Servers") }
        } label: { Label("MCP Servers", systemImage: "wrench.and.screwdriver") }
        .accessibilityIdentifier("mcp.chatSelector")
    }
}
