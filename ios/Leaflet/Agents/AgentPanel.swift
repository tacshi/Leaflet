import LeafletCore
import SwiftUI

struct AgentPanel<Chat: AgentChat>: View {
    let model: Chat
    var allPages: Binding<Bool>?
    var isSheet = false
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @FocusState private var composing: Bool
    @ScaledMetric(relativeTo: .body) private var line: CGFloat = 38
    @State private var following = true
    @State private var atFoot = true

    private var state: AgentState { model.agents }

    private var loneRun: AgentState.Run? {
        let held = Set(state.messages.compactMap(\.runID))
        let run = state.runs.last {
            !held.contains($0.id) && !$0.edits.isEmpty && ($0.error ?? "").isEmpty
        }
        return run?.settled == true ? nil : run
    }

    var body: some View {
        if !model.agentsLoaded {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if !state.available && state.options.isEmpty {
            ContentUnavailableView {
                Label(L.panelNoProviderTitle, systemImage: "sparkles")
            } description: {
                Text(L.panelNoProviderBody)
            } actions: {
                Button(L.panelOpenProviders) { openProviders() }
                    .buttonStyle(.borderedProminent)
            }
            .onAppear { scene.focusChat = false }
        } else {
            VStack(spacing: 0) {
                conversation
                Divider()
                composer
            }
        }
    }

    private var conversation: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    if state.messages.isEmpty {
                        Text(model.emptyText)
                            .foregroundStyle(palette.textMuted)
                            .padding(.vertical, 8)
                        ForEach(model.suggestions, id: \.self) { s in
                            Button(s) { send(s) }
                                .buttonStyle(.bordered)
                                .hoverEffect()
                        }
                    }
                    ForEach(Array(state.messages.enumerated()), id: \.offset) { i, m in
                        MessageView(
                            message: m,
                            run: m.runID.flatMap { id in state.runs.first { $0.id == id } },
                            pageID: model.id, isSheet: isSheet
                        )
                        .id(i)
                    }
                    if let run = loneRun {
                        VStack(alignment: .leading, spacing: 6) {
                            Label(runText(run), systemImage: "sparkles")
                                .font(.footnote.weight(.medium))
                            RunCard(run: run, pageID: model.id, isSheet: isSheet)
                        }
                    }
                }
                .padding(14)
            }
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .onChange(of: state.messages.count) {
                withAnimation { proxy.scrollTo(state.messages.count - 1, anchor: .bottom) }
            }
            .onChange(of: state.messages.last?.text) {
                if following { proxy.scrollTo(state.messages.count - 1, anchor: .bottom) }
            }
            .onScrollGeometryChange(for: Bool.self) { geo in
                geo.visibleRect.maxY >= geo.contentSize.height - 40
            } action: { _, foot in
                atFoot = foot
            }
            .onScrollPhaseChange { _, phase in
                if phase == .idle { following = atFoot }
            }
        }
    }

    private func runText(_ run: AgentState.Run) -> String {
        if run.undone { return L.pageRunUndone(agent: run.agentName) }
        let blocks = Set(run.blockIDs ?? []).count
        if blocks == 0, let last = run.edits.last {
            return L.pageRunDid(agent: run.agentName, edit: last)
        }
        return L.pageRunEdited(agent: run.agentName, count: blocks)
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 8) {
            if let allPages { scopeMenu(allPages) }
            Menu {
                ForEach(state.options) { o in
                    Toggle(
                        o.label,
                        isOn: Binding(
                            get: { state.current?.providerID == o.providerID },
                            set: { on in
                                guard on else { return }
                                Task {
                                    await app.client?.selectAgent(
                                        model.id, providerID: o.providerID, model: "")
                                    await model.refreshAgents()
                                }
                            }))
                }
                Divider()
                Button(L.panelNewConversation, systemImage: "square.and.pencil") {
                    Task { await app.client?.newConversation(model.id) }
                }
                if !state.past.isEmpty {
                    Menu(L.panelPastConversations, systemImage: "clock") {
                        ForEach(state.past) { past in
                            Button(past.title.isEmpty ? L.commonUntitled : past.title) {
                                Task {
                                    await app.client?.openConversation(model.id, id: past.id)
                                    await model.refreshAgents()
                                }
                            }
                        }
                    }
                    .disabled(state.running)
                }
                Button(L.panelManageProviders) { openProviders() }
            } label: {
                Image(systemName: "ellipsis.circle").imageScale(.large)
                    .frame(minWidth: 28, minHeight: line)
            }
            .accessibilityLabel(LI.agentOptions)
            TextField(model.placeholder, text: Bindable(model).chatDraft, axis: .vertical)
                .lineLimit(1...6)
                .textFieldStyle(.plain)
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
                .frame(minHeight: line)
                .background(palette.panel, in: .rect(cornerRadius: 10))
                .focused($composing)
                .onSubmit { send(model.chatDraft) }
                .onAppear { takeFocus() }
                .onChange(of: scene.focusChat) { takeFocus() }
            if state.running, let run = state.runs.last(where: { $0.running && $0.stoppable }) {
                Button(L.pageStopRun, systemImage: "stop.circle.fill") {
                    Task { await app.client?.stopAgent(model.id, runID: run.id) }
                }
                .labelStyle(.iconOnly)
                .imageScale(.large)
                .frame(minWidth: 28, minHeight: line)
            } else {
                Button(L.panelSend, systemImage: "arrow.up.circle.fill") { send(model.chatDraft) }
                    .labelStyle(.iconOnly)
                    .imageScale(.large)
                    .frame(minWidth: 28, minHeight: line)
                    .disabled(
                        state.running
                            || model.chatDraft.trimmingCharacters(in: .whitespacesAndNewlines)
                                .isEmpty
                    )
                    .keyboardShortcut(.return, modifiers: .command)
            }
        }
        .padding(10)
    }

    private func scopeMenu(_ allPages: Binding<Bool>) -> some View {
        Menu {
            Picker(selection: allPages) {
                Label(L.panelScopePage, systemImage: "doc.text").tag(false)
                Label(L.panelScopeAll, systemImage: "square.grid.2x2").tag(true)
            } label: {
                EmptyView()
            }
        } label: {
            Image(systemName: allPages.wrappedValue ? "square.grid.2x2" : "doc.text").imageScale(
                .large
            )
            .frame(minWidth: 28, minHeight: line)
        }
        .accessibilityLabel(L.panelScope)
        .accessibilityValue(allPages.wrappedValue ? L.panelScopeAll : L.panelScopePage)
    }

    private func openProviders() {
        scene.openSettings()
    }

    private func takeFocus() {
        guard scene.focusChat else { return }
        scene.focusChat = false
        composing = true
    }

    private func send(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, !state.running, let client = app.client else { return }
        model.chatDraft = ""
        Task { [model, scene] in await model.send(t, client: client, scene: scene) }
    }
}

struct MessageView: View {
    let message: AgentState.Message
    let run: AgentState.Run?
    let pageID: String
    var isSheet = false
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: message.mine ? .trailing : .leading, spacing: 6) {
            if !message.mine, let name = message.agentName, !name.isEmpty {
                Text(name).font(.caption.weight(.semibold)).foregroundStyle(palette.textMuted)
            }
            if message.pending && !message.streamed {
                if message.text.isEmpty {
                    ProgressView().controlSize(.small)
                } else {
                    StatusLine(text: message.text)
                }
            } else {
                MarkdownPreview(markdown: message.text)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(
                        message.mine ? palette.accentSoft : palette.panel,
                        in: .rect(cornerRadius: 12)
                    )
                    .foregroundStyle(message.error ? palette.dangerText : palette.text)
                    .textSelection(.enabled)
                    .environment(
                        \.openURL,
                        OpenURLAction { [scene, app, pageID, isSheet] url in
                            guard url.scheme == "leaflet" else { return .systemAction }
                            let id = url.pathComponents.dropFirst().first ?? ""
                            guard url.host() == "page", app.opens(id) else { return .handled }
                            let href = url.absoluteString
                            scene.openFromChat(
                                id, at: MobileLinkedBlock(href), moment: MobileLinkedMoment(href),
                                workspace: pageID == WorkspaceAgentModel.chatID, sheet: isSheet)
                            return .handled
                        })
            }
            if let run, !run.edits.isEmpty {
                RunCard(run: run, pageID: pageID, isSheet: isSheet)
            }
        }
        .frame(maxWidth: .infinity, alignment: message.mine ? .trailing : .leading)
    }
}

struct StatusLine: View {
    let text: String
    @Environment(\.palette) private var palette
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private static let band = 0.3
    private static let cycle = 2.0

    var body: some View {
        if reduceMotion {
            Text(text).foregroundStyle(palette.textMuted)
        } else {
            TimelineView(.animation(minimumInterval: 1.0 / 30)) { context in
                let t =
                    context.date.timeIntervalSinceReferenceDate
                    .truncatingRemainder(dividingBy: Self.cycle) / Self.cycle
                let at = -Self.band + (1 + 2 * Self.band) * t
                Text(text).foregroundStyle(
                    LinearGradient(
                        colors: [palette.textMuted, palette.text, palette.textMuted],
                        startPoint: UnitPoint(x: at - Self.band, y: 0.5),
                        endPoint: UnitPoint(x: at + Self.band, y: 0.5)))
            }
        }
    }
}

struct RunCard: View {
    let run: AgentState.Run
    let pageID: String
    var isSheet = false
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(run.edits.enumerated()), id: \.offset) { i, e in
                if let page = run.page(i), !run.undone, app.opens(page) {
                    Button {
                        scene.openFromChat(
                            page, workspace: pageID == WorkspaceAgentModel.chatID, sheet: isSheet)
                    } label: {
                        Label(e, systemImage: "doc.text")
                    }
                    .font(.footnote)
                } else {
                    Label(e, systemImage: "pencil.line").font(.footnote)
                }
            }
            HStack {
                Spacer()
                if run.undone {
                    Button(L.pageRedo) {
                        step { try await $0.redoRun(pageID, runID: run.id) }
                    }
                } else if !run.running {
                    Button(L.commonUndo) {
                        step { try await $0.undoRun(pageID, runID: run.id) }
                    }
                }
            }
            .font(.footnote)
        }
        .padding(10)
        .background(palette.agentSection, in: .rect(cornerRadius: 10))
    }

    private func step(_ body: @escaping @MainActor (CoreClient) async throws -> Void) {
        guard let client = app.client else { return }
        Task {
            do {
                try await body(client)
            } catch {
                app.show(Toast(message: error.localizedDescription))
            }
        }
    }
}
