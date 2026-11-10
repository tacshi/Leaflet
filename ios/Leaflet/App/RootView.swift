import SwiftUI
import UIKit

struct RootView: View {
    var opening: String?
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme
    @Environment(\.scenePhase) private var phase
    @Environment(\.horizontalSizeClass) private var sizeClass
    @SceneStorage("page") private var savedPage: String?
    @SceneStorage("inspector.shown") private var savedInspector = false
    @State private var scene = SceneModel()
    @State private var folderName = ""

    private func openReminder() {
        guard app.activeScene === scene, let target = ReminderInbox.shared.target else { return }
        ReminderInbox.shared.target = nil
        scene.open(target.pageID, at: target.blockID)
    }

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
                    SidebarView(stacked: sizeClass == .compact)
                        .ignoresSafeArea(.keyboard, edges: .bottom)
                        .toolbar {
                            ToolbarItem(placement: .topBarLeading) {
                                Button(L.settingsTitle, systemImage: "gearshape") {
                                    scene.settingsShown = true
                                }
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
                .id(app.shown)
            }
        }
        .ignoresSafeArea(.keyboard, edges: .bottom)
        .preferredColorScheme(app.theme.colorScheme)
        .background(SceneReader { scene.toasts.show(in: $0, app: app) })
        .background(FoldReader { scene.sidebarToFold = $0 })
        .sheet(isPresented: $scene.settingsShown) {
            SettingsView().preferredColorScheme(app.theme.colorScheme ?? scheme)
        }
        .alert(L.dialogFolderNew, isPresented: $scene.newFolderShown) {
            TextField(L.dialogFolderName, text: $folderName)
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonSave) {
                let name = folderName.trimmingCharacters(in: .whitespaces)
                let parent = scene.newFolderParent
                if !name.isEmpty { Task { await app.createFolder(named: name, in: parent) } }
            }
        }
        .onChange(of: scene.newFolderShown) { if scene.newFolderShown { folderName = "" } }
        .sheet(item: $scene.moving) { MoveSheet(page: $0) }
        .sheet(item: $scene.movingFolder) { MoveSheet(folder: $0) }
        .sheet(isPresented: $scene.workspaceShown) {
            AgentPanel(model: scene.workspace, isSheet: true)
                .task { scene.workspace.start(app) }
                .onAppear { scene.panelShown(asSheet: true) }
                .onDisappear {
                    scene.focusChat = false
                    scene.panelGone(asSheet: true)
                }
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
        .environment(\.palette, palette)
        .tint(palette.accent)
        .environment(scene)
        .focusedSceneValue(\.scene, scene)
        .onAppear {
            app.activeScene = scene
            scene.reopened()
            scene.open(savedPage ?? opening)
            openReminder()
            openVoiceNote()
            #if DEBUG
                if let title = UserDefaults.standard.string(forKey: "LeafletOpen") {
                    Task {
                        await app.reload()
                        scene.open(app.library.pages.first { $0.title.contains(title) }?.id)
                    }
                }
            #endif
            scene.inspectorShown = savedInspector
            #if DEBUG
                if UserDefaults.standard.bool(forKey: "LeafletSettings") {
                    scene.settingsShown = true
                }
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
            if app.libraryLoaded { scene.keep(Set(ids)) }
        }
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

@MainActor
final class ToastWindow {
    private var window: UIWindow?

    func show(in scene: UIWindowScene, app: AppModel) {
        guard window == nil else { return }
        let w = PassThroughWindow(windowScene: scene)
        let host = UIHostingController(
            rootView: ToastHost { [weak w] in w?.toast = $0 }.environment(app))
        host.view.backgroundColor = .clear
        w.rootViewController = host
        w.windowLevel = .normal + 1
        w.isHidden = false
        window = w
    }
}

private final class PassThroughWindow: UIWindow {
    var toast = CGRect.null

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
        toast.contains(point) ? super.hitTest(point, with: event) : nil
    }
}

private struct ToastHost: View {
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

struct FoldReader: UIViewRepresentable {
    var changed: (CGFloat?) -> Void

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

struct ToastView: View {
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
            .onGeometryChange(for: CGRect.self, of: { $0.frame(in: .global) }, action: placed)
            .padding(.bottom, 24)
            .transition(.move(edge: .bottom).combined(with: .opacity))
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isStaticText)
        }
    }
}

private struct NoPageScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.palette) private var palette
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
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background { palette.surface.ignoresSafeArea() }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
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
