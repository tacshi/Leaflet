import SwiftUI
import UIKit

/// RootView is a window: the library in the sidebar, and the page chosen
/// in it. On iPhone, and in a narrow window on iPad, the two stack.
struct RootView: View {
    /// opening is the page a window opened for one starts on.
    var opening: String?
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme
    @Environment(\.scenePhase) private var phase
    @SceneStorage("page") private var savedPage: String?
    @SceneStorage("inspector.shown") private var savedInspector = false
    @State private var scene = SceneModel()

    var body: some View {
        let palette = Palette.of(accent: app.accent, mode: app.theme, scheme: scheme)
        @Bindable var scene = scene
        Group {
            if let failure = app.openFailure {
                ContentUnavailableView(
                    LI.coreFailed, systemImage: "exclamationmark.triangle",
                    description: Text(failure))
            } else {
                NavigationSplitView(columnVisibility: $scene.columns) {
                    // Each column makes room for the keyboard on its own: in
                    // neither does a band of background stand behind the
                    // keyboard's floating shortcuts bar.
                    SidebarView()
                        .ignoresSafeArea(.keyboard, edges: .bottom)
                        .toolbar {
                            ToolbarItem(placement: .topBarLeading) {
                                Button(L.settingsTitle, systemImage: "gearshape") {
                                    scene.settingsShown = true
                                }
                                // Plain, as the sidebar's own button.
                                .tint(Color.primary)
                            }
                        }
                        .navigationSplitViewColumnWidth(min: 240, ideal: 300, max: 420)
                } detail: {
                    if let selection = scene.selection {
                        PageScreen(pageID: selection).id(selection)
                            .ignoresSafeArea(.keyboard, edges: .bottom)
                    } else {
                        ContentUnavailableView {
                            Label(L.shellNoPageOpen, systemImage: "doc.text")
                        } description: {
                            Text(L.shellNoPageHint)
                        } actions: {
                            Button(L.shellNewPage) {
                                Task { scene.open(await app.createPage()) }
                            }
                            .buttonStyle(.borderedProminent)
                        }
                        // On the page's ground, as a page open is: the
                        // sidebar's glass takes its color from it.
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(palette.surface)
                    }
                }
                // Drawn again in a language chosen in the settings.
                .id(app.shown)
            }
        }
        // Nothing makes room for the keyboard's floating shortcuts bar, or
        // a keyboard, with a band of background: the editor keeps its
        // text clear of them itself, and the sidebar scrolls under them.
        .ignoresSafeArea(.keyboard, edges: .bottom)
        .preferredColorScheme(app.theme.colorScheme)
        // The toast shows in a window of its own, over the sheets.
        .background(SceneReader { scene.toasts.show(in: $0, app: app) })
        .sheet(isPresented: $scene.settingsShown) {
            // A sheet open keeps the scheme it opened with, and one let go
            // to follow the system keeps the last it was given: the theme
            // chosen in it comes as the scheme it shows, the system's too.
            SettingsView().preferredColorScheme(app.theme.colorScheme ?? scheme)
        }
        .sheet(item: $scene.moving) { MoveSheet(page: $0) }
        .sheet(item: $scene.history) { HistorySheet(page: $0).presentationSizing(.page) }
        .fileExporter(
            isPresented: Binding(
                get: { scene.exported != nil }, set: { if !$0 { scene.exported = nil } }),
            document: scene.exported.map { MarkdownFile($0.markdown) },
            contentType: MarkdownFile.readableContentTypes[0],
            defaultFilename: scene.exported?.name
        ) { result in
            switch result {
            case .success(let url):
                app.show(Toast(message: L.hostExported(name: url.lastPathComponent)))
            case .failure(let error):
                app.show(Toast(message: L.hostErrorExport(error: error.localizedDescription)))
            }
        }
        // After the sheets, for them to take the theme too.
        .environment(\.palette, palette)
        .tint(palette.accent)
        .environment(scene)
        .focusedSceneValue(\.scene, scene)
        .onAppear {
            app.activeScene = scene
            scene.open(opening ?? savedPage)
            #if DEBUG
                // -LeafletOpen <title> opens the page of that title, to
                // look at it on a simulator without a tap.
                if let title = UserDefaults.standard.string(forKey: "LeafletOpen") {
                    Task {
                        await app.reload()
                        scene.open(app.library.pages.first { $0.title.contains(title) }?.id)
                    }
                }
            #endif
            scene.inspectorShown = savedInspector
            #if DEBUG
                // -LeafletSettings YES opens Settings.
                if UserDefaults.standard.bool(forKey: "LeafletSettings") {
                    scene.settingsShown = true
                }
                // -LeafletInspector <tab> opens the panel on that tab.
                if let tab = UserDefaults.standard.string(forKey: "LeafletInspector")
                    .flatMap(InspectorTab.init(rawValue:))
                {
                    scene.inspectorTab = tab
                    scene.inspectorShown = true
                }
            #endif
        }
        .onChange(of: scene.selection) { savedPage = scene.selection }
        .onChange(of: scene.inspectorShown) { savedInspector = scene.inspectorShown }
        .onChange(of: app.library.pages.map(\.id)) { _, ids in
            // Pages deleted, here or on another device, or gone as the
            // account signed out, leave the window, once the library is
            // read: the last page too.
            if app.libraryLoaded { scene.keep(Set(ids)) }
        }
        .onChange(of: phase) { _, phase in
            switch phase {
            case .active:
                app.activeScene = scene
                app.foreground(scene)
            case .background: app.background(scene)
            default: break
            }
        }
    }
}

/// ToastWindow shows the app's toast in a window of its own, over the
/// window scene's: drawn in the scene's window, it would go under the
/// sheets, as Settings and History, which raise toasts too. The window
/// lets touches through, but on the toast.
@MainActor
final class ToastWindow {
    private var window: UIWindow?

    /// show puts the window up in scene, once.
    func show(in scene: UIWindowScene, app: AppModel) {
        guard window == nil else { return }
        let host = UIHostingController(rootView: ToastHost().environment(app))
        host.view.backgroundColor = .clear
        let w = PassThroughWindow(windowScene: scene)
        w.rootViewController = host
        // Over the scene's window and the sheets in it, under the alerts.
        w.windowLevel = .normal + 1
        w.isHidden = false
        window = w
    }
}

/// PassThroughWindow takes only the touches on its views, as the toast:
/// the others go to the window under it.
private final class PassThroughWindow: UIWindow {
    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
        let hit = super.hitTest(point, with: event)
        return hit === rootViewController?.view ? nil : hit
    }
}

/// ToastHost is the toast window's view: the toast at its foot, in the
/// app's theme, as the windows have it.
private struct ToastHost: View {
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        let palette = Palette.of(accent: app.accent, mode: app.theme, scheme: scheme)
        ToastView()
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
            .environment(\.palette, palette)
            .tint(palette.accent)
            .preferredColorScheme(app.theme.colorScheme)
            .animation(.default, value: app.toast?.id)
    }
}

/// SceneReader gives the window scene the view is in, once it is in one.
private struct SceneReader: UIViewRepresentable {
    var found: (UIWindowScene) -> Void

    func makeUIView(context: Context) -> SceneView {
        let view = SceneView()
        view.found = found
        return view
    }

    func updateUIView(_ view: SceneView, context: Context) { view.found = found }

    final class SceneView: UIView {
        var found: ((UIWindowScene) -> Void)?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if let scene = window?.windowScene { found?(scene) }
        }
    }
}

/// ToastView shows the app's toast, with its action, at the foot of the
/// window.
struct ToastView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        if let toast = app.toast {
            HStack(spacing: 12) {
                Text(toast.message).lineLimit(2)
                if let label = toast.actionLabel, let action = toast.action {
                    Button(label) {
                        app.toast = nil
                        Task { await action() }
                    }
                    .bold()
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .glassEffect(.regular, in: .capsule)
            .padding(.bottom, 24)
            .transition(.move(edge: .bottom).combined(with: .opacity))
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isStaticText)
        }
    }
}
