import SwiftUI
import UIKit

/// RootView is a window: the library in the sidebar, and the page chosen
/// in it. On iPhone, on a foldable closed, and in a narrow window on iPad,
/// the two stack.
struct RootView: View {
    /// opening is the page a window opened for one starts on.
    var opening: String?
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme
    @Environment(\.scenePhase) private var phase
    /// sizeClass is the window's: compact, the columns stack. (A column's
    /// own is compact on iPad too.)
    @Environment(\.horizontalSizeClass) private var sizeClass
    @SceneStorage("page") private var savedPage: String?
    @SceneStorage("inspector.shown") private var savedInspector = false
    @State private var scene = SceneModel()
    /// folderName is the name typed for a new folder.
    @State private var folderName = ""

    /// openReminder opens the to-do of a reminder tapped, in the window in
    /// front.
    private func openReminder() {
        guard app.activeScene === scene, let target = ReminderInbox.shared.target else { return }
        ReminderInbox.shared.target = nil
        scene.open(target.pageID, at: target.blockID)
    }

    /// openVoiceNote opens the page of a voice note started from outside
    /// the app, in the window in front.
    private func openVoiceNote() {
        guard app.activeScene === scene, let id = app.voiceNote else { return }
        app.voiceNote = nil
        scene.open(id)
    }

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
                    SidebarView(stacked: sizeClass == .compact)
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
                        .navigationSplitViewColumnWidth(
                            min: 240, ideal: scene.sidebarToFold ?? 300,
                            max: scene.sidebarToFold ?? 420)
                } detail: {
                    if let selection = scene.selection {
                        PageScreen(pageID: selection).id(selection)
                            .ignoresSafeArea(.keyboard, edges: .bottom)
                    } else {
                        NoPageScreen()
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
        .background(FoldReader { scene.sidebarToFold = $0 })
        .sheet(isPresented: $scene.settingsShown) {
            // A sheet open keeps the scheme it opened with, and one let go
            // to follow the system keeps the last it was given: the theme
            // chosen in it comes as the scheme it shows, the system's too.
            SettingsView().preferredColorScheme(app.theme.colorScheme ?? scheme)
        }
        // The window's, not the sidebar's, for ⇧⌘N to ask with the sidebar
        // hidden, or behind the page on iPhone; each time with no name yet.
        .alert(L.dialogFolderNew, isPresented: $scene.newFolderShown) {
            TextField(L.dialogFolderName, text: $folderName)
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonSave) {
                let name = folderName.trimmingCharacters(in: .whitespaces)
                if !name.isEmpty { Task { await app.createFolder(named: name) } }
            }
        }
        .onChange(of: scene.newFolderShown) { if scene.newFolderShown { folderName = "" } }
        .sheet(item: $scene.moving) { MoveSheet(page: $0) }
        // The workspace agent's chat with no panel beside a page for it, as
        // on iPhone's list of pages.
        .sheet(isPresented: $scene.workspaceShown) {
            AgentPanel(model: scene.workspace, isSheet: true)
                .task { scene.workspace.start(app) }
                // A panel as a sheet, which Settings waits for to go.
                .onAppear { scene.panelShown(asSheet: true) }
                .onDisappear {
                    scene.focusChat = false
                    scene.panelGone(asSheet: true)
                }
                // Clear of the sheet's grabber and its round corners: no
                // tabs stand above the chat here, as they do in a page's.
                .padding(.top, 14)
                .padding(.horizontal, 8)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
        }
        .sheet(item: $scene.history) { HistorySheet(page: $0).presentationSizing(.page) }
        .fileExporter(
            isPresented: Binding(
                get: { scene.exported != nil }, set: { if !$0 { scene.exported = nil } }),
            document: scene.exported,
            contentType: scene.exported?.type ?? ExportFile.markdown,
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
            scene.reopened()
            // The page the window was left at, as it comes back, over the
            // one it opened for, which it keeps as its own.
            scene.open(savedPage ?? opening)
            openReminder()
            openVoiceNote()
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
        .onChange(of: ReminderInbox.shared.target) { openReminder() }
        .onChange(of: app.voiceNote) { openVoiceNote() }
        .onChange(of: sizeClass, initial: true) { scene.stacked = sizeClass == .compact }
        .onChange(of: scene.inspectorShown) { savedInspector = scene.inspectorShown }
        .onChange(of: app.library.pages.map(\.id)) { _, ids in
            // Pages deleted, here or on another device, or gone as the
            // account signed out, leave the window, once the library is
            // read: the last page too.
            if app.libraryLoaded { scene.keep(Set(ids)) }
        }
        // The page the window still holds closes with it, and its
        // workspace agent's chat stops following the core.
        .onDisappear {
            scene.closeHeld()
            scene.workspace.stop()
        }
        .onChange(of: phase) { _, phase in
            switch phase {
            case .active:
                app.activeScene = scene
                app.foreground(scene)
                openVoiceNote()
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
        let w = PassThroughWindow(windowScene: scene)
        let host = UIHostingController(
            rootView: ToastHost { [weak w] in w?.toast = $0 }.environment(app))
        host.view.backgroundColor = .clear
        w.rootViewController = host
        // Over the scene's window and the sheets in it, under the alerts.
        w.windowLevel = .normal + 1
        w.isHidden = false
        window = w
    }
}

/// PassThroughWindow takes only the touches on the toast, where its view
/// says the toast is: the others go to the window under it. From iOS 18,
/// SwiftUI's hit test finds the hosting view for its buttons too, so what
/// is under a touch says nothing of whether the toast is.
private final class PassThroughWindow: UIWindow {
    /// toast is where the toast is in the window, or null with none.
    var toast = CGRect.null

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
        toast.contains(point) ? super.hitTest(point, with: event) : nil
    }
}

/// ToastHost is the toast window's view: the toast at its foot, in the
/// app's theme, as the windows have it. It says where the toast is, for
/// the window to take its touches.
private struct ToastHost: View {
    /// placed hears where the toast is, in the window.
    var placed: (CGRect) -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        let palette = Palette.of(accent: app.accent, mode: app.theme, scheme: scheme)
        ToastView(placed: placed)
            .onChange(of: app.toast == nil) { _, gone in
                if gone { placed(.null) }
            }
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

/// FoldReader says the width that takes the sidebar up to the fold of a
/// half folded display, or nil with no fold, as it changes.
struct FoldReader: UIViewRepresentable {
    var changed: (CGFloat?) -> Void

    /// width is the sidebar's in a window of width with fold down it: to
    /// the middle of the fold, as long as that leaves it and the page room.
    static func width(of window: CGFloat, fold: ClosedRange<CGFloat>?) -> CGFloat? {
        guard let fold else { return nil }
        let middle = (fold.lowerBound + fold.upperBound) / 2
        return middle >= 240 && window - middle >= 320 ? middle : nil
    }

    func makeUIView(context: Context) -> FoldView {
        let view = FoldView()
        view.isUserInteractionEnabled = false
        view.changed = changed
        return view
    }

    func updateUIView(_ view: FoldView, context: Context) { view.changed = changed }

    final class FoldView: UIView {
        var changed: ((CGFloat?) -> Void)?
        // Before the page's own, for it to come in clear of the fold and
        // not lay out twice.
        private lazy var watch = FoldWatch(view: self, wait: .milliseconds(250)) { [weak self] in
            guard let self else { return }
            changed?(FoldReader.width(of: bounds.width, fold: $0))
        }

        override init(frame: CGRect) {
            super.init(frame: frame)
            if #available(iOS 27.1, *) {
                addInteraction(UIHingeInteraction { [weak self] _, _ in self?.watch.hingeMoved() })
            }
        }

        required init?(coder: NSCoder) { fatalError("not coded") }

        override func layoutSubviews() {
            super.layoutSubviews()
            watch.resized()
        }
    }
}

/// ToastView shows the app's toast, with its action, at the foot of the
/// window.
struct ToastView: View {
    /// placed hears where the toast is in the window, as it shows.
    var placed: (CGRect) -> Void = { _ in }
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
            // The toast window's hosting view fills it, so the global
            // space is the window's.
            .onGeometryChange(for: CGRect.self, of: { $0.frame(in: .global) }, action: placed)
            .padding(.bottom, 24)
            .transition(.move(edge: .bottom).combined(with: .opacity))
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isStaticText)
        }
    }
}

/// NoPageScreen is the window with no page open: a new page to start, or a
/// recording in one, and the panel beside it, the workspace agent's chat
/// alone, of all the user's pages.
private struct NoPageScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.palette) private var palette
    /// pageTrailing is where this view ends on screen, for the panel to
    /// tell whether it stands beside it or floats over it.
    @State private var pageTrailing: CGFloat?

    var body: some View {
        ContentUnavailableView {
            Label(L.shellNoPageOpen, systemImage: "doc.text")
        } description: {
            Text(L.shellNoPageHint)
        } actions: {
            Button(L.shellNewPage) {
                Task { scene.open(await app.createPage()) }
            }
            .buttonStyle(.borderedProminent)
            Button(L.shellAskAgent, systemImage: "sparkles") { scene.askWorkspace() }
                .buttonStyle(.bordered)
        }
        // On the page's ground, as a page open is: the sidebar's glass
        // takes its color from it. Under the keyboard too, while the
        // sidebar's search has it.
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background { palette.surface.ignoresSafeArea() }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                // A recording in a page of its own, which records once it
                // shows, as the desktop's New Recording.
                Button(L.trayNewRecording, systemImage: "record.circle") {
                    Task {
                        guard let id = await app.createRecordingPage() else { return }
                        scene.recordOnOpen = id
                        scene.open(id)
                    }
                }
                .tint(Color.primary)
                .disabled(Recorder.shared.state.recording)
            }
            ToolbarItem(placement: .primaryAction) {
                Button(LI.showInspector, systemImage: "sidebar.trailing") {
                    scene.inspectorShown.toggle()
                }
                .tint(scene.inspectorShown ? nil : Color.primary)
            }
        }
        .onGeometryChange(for: CGFloat.self) {
            $0.frame(in: .global).maxX
        } action: {
            pageTrailing = $0
        }
        .inspector(
            isPresented: Binding(get: { scene.inspectorShown }, set: { scene.inspectorShown = $0 })
        ) {
            Inspector(
                tab: Binding(get: { scene.inspectorTab }, set: { scene.inspectorTab = $0 }),
                model: nil, isSheet: sizeClass == .compact, noPage: true,
                pageTrailing: pageTrailing
            )
            .inspectorColumnWidth(min: 280, ideal: 340, max: 640)
            .presentationDetents([.medium, .large])
        }
    }
}
