import AuthenticationServices
import SwiftUI

/// SignInView signs in with Supabase, to the Leaflet project the desktop
/// app set up: Supabase's sign-in page opens in a web session, which ends
/// at the Worker's redirect (internal/supabaseapi.Auth); the core then
/// finds the project and signs in to it.
struct SignInView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @Environment(\.webAuthenticationSession) private var webSession
    @Environment(\.dismiss) private var dismiss
    @State private var busy = false
    @State private var failure: String?
    /// waiting is a sign-in that removes changes not uploaded yet, to ask
    /// about.
    @State private var waiting: SignedIn?

    /// callback is the OAuth app's redirect, which the web session takes
    /// without loading it: the Associated Domains entitlement ties the app
    /// to this host for webcredentials.
    static let callback = ASWebAuthenticationSession.Callback.https(
        host: "leaflet-auth.capyvibe.app", path: "/auth/supabase")

    var body: some View {
        Form {
            Section {
                VStack(spacing: 8) {
                    Image(systemName: "arrow.triangle.2.circlepath.icloud")
                        .font(.largeTitle)
                        .foregroundStyle(.tint)
                    Text(L.signinTitle).font(.title2.bold())
                    Text(L.signinSubtitle)
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
            }
            Section {
                Button {
                    Task { await signIn() }
                } label: {
                    if busy {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text(L.signinSigningIn)
                        }
                    } else {
                        Text(L.signinContinueSupabase)
                    }
                }
                .disabled(busy || app.client == nil)
                .listRowBackground(palette.surface)
            } footer: {
                if let failure {
                    Text(failure).foregroundStyle(palette.dangerText)
                } else {
                    Text(LI.signInFromDesktop)
                }
            }
        }
        .navigationTitle(L.commonSignIn)
        .navigationBarTitleDisplayMode(.inline)
        .alert(
            L.signinAnyway,
            isPresented: Binding(get: { waiting != nil }, set: { if !$0 { waiting = nil } }),
            presenting: waiting
        ) { _ in
            Button(L.commonCancel, role: .cancel) { Task { await confirm(anyway: false) } }
            Button(L.commonSignIn, role: .destructive) { Task { await confirm(anyway: true) } }
        } message: { waiting in
            Text(L.signinRemovesUnsynced(count: waiting.lost, email: waiting.email))
        }
    }

    private func signIn() async {
        guard let client = app.client else { return }
        busy = true
        failure = nil
        defer { busy = false }
        do {
            let begun = try client.beginSupabaseSignIn()
            let answer = try await webSession.authenticate(
                using: begun.url, callback: Self.callback, additionalHeaderFields: [:])
            let done = try await client.finishSupabaseSignIn(begun, callback: answer)
            if done.lost > 0 {
                waiting = done
                return
            }
            await signedIn(done.email)
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
            // Closing Supabase's page is no failure.
        } catch {
            failure = error.localizedDescription
        }
    }

    private func confirm(anyway: Bool) async {
        guard let client = app.client, let asked = waiting else { return }
        waiting = nil
        busy = true
        defer { busy = false }
        do {
            try await client.confirmSignIn(anyway: anyway)
            if anyway { await signedIn(asked.email) }
        } catch {
            failure = error.localizedDescription
        }
    }

    private func signedIn(_ email: String) async {
        await app.reload()
        app.show(Toast(message: L.settingsSignedInAs(email: email)))
        dismiss()
    }
}
