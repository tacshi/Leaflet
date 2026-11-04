import SwiftUI

/// AgentPanel is the page's agent chat, as the desktop's Agent tab: the
/// conversation, each run's edits with Undo, Stop while one works, the
/// agent to ask, and suggestions for an empty chat.
struct AgentPanel: View {
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @FocusState private var composing: Bool
    /// line is the composer's height with one line: its buttons take it, so
    /// they sit level with a line's text, and at the foot of a longer draft.
    @ScaledMetric(relativeTo: .body) private var line: CGFloat = 38
    /// following keeps a reply in view as it streams in, until the chat is
    /// scrolled up from its foot.
    @State private var following = true
    @State private var atFoot = true

    private var state: AgentState { model.agents }

    /// loneRun is the latest run with edits that no message of the chat
    /// holds, as one Ask or an @agent comment started: its Undo is here,
    /// as the desktop's run bar has it, until a version restored settles
    /// it.
    private var loneRun: AgentState.Run? {
        let held = Set(state.messages.compactMap(\.runID))
        let run = state.runs.last {
            !held.contains($0.id) && !$0.edits.isEmpty && ($0.error ?? "").isEmpty
        }
        return run?.settled == true ? nil : run
    }

    var body: some View {
        if !model.agentsLoaded {
            // Not read yet: no agent to ask is not known yet.
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
                        Text(L.panelChatEmpty)
                            .foregroundStyle(palette.textMuted)
                            .padding(.vertical, 8)
                        ForEach(suggestions, id: \.self) { s in
                            Button(s) { send(s) }
                                .buttonStyle(.bordered)
                                .hoverEffect()
                        }
                    }
                    ForEach(Array(state.messages.enumerated()), id: \.offset) { i, m in
                        MessageView(
                            message: m,
                            run: m.runID.flatMap { id in state.runs.first { $0.id == id } },
                            model: model
                        )
                        .id(i)
                    }
                    if let run = loneRun {
                        VStack(alignment: .leading, spacing: 6) {
                            Label(
                                run.undone
                                    ? L.pageRunUndone(agent: run.agentName)
                                    : L.pageRunEdited(
                                        agent: run.agentName, count: Set(run.blockIDs ?? []).count),
                                systemImage: "sparkles"
                            )
                            .font(.footnote.weight(.medium))
                            RunCard(run: run, pageID: model.id)
                        }
                    }
                }
                .padding(14)
            }
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

    private var suggestions: [String] {
        [L.panelSuggestSummarize, L.panelSuggestStructure, L.panelSuggestGaps, L.panelSuggestChart]
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 8) {
            Menu {
                // Each agent, at the model Settings chose for it, the one
                // the chat asks checked.
                ForEach(state.options) { o in
                    Toggle(
                        o.label,
                        isOn: Binding(
                            get: {
                                state.current
                                    == AgentState.Choice(providerID: o.providerID, model: o.model)
                            },
                            set: { on in
                                guard on else { return }
                                Task {
                                    await app.client?.selectAgent(
                                        model.id, providerID: o.providerID, model: o.model)
                                    await model.refreshAgents()
                                }
                            }))
                }
                Divider()
                Button(L.panelNewConversation, systemImage: "square.and.pencil") {
                    Task { await app.client?.newConversation(model.id) }
                }
                Button(L.panelManageProviders) { openProviders() }
            } label: {
                Image(systemName: "ellipsis.circle").imageScale(.large)
                    .frame(minWidth: 28, minHeight: line)
            }
            .accessibilityLabel(LI.agentOptions)
            TextField(L.panelAskPlaceholder, text: Bindable(model).chatDraft, axis: .vertical)
                .lineLimit(1...6)
                .textFieldStyle(.plain)
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
                .frame(minHeight: line)
                .background(palette.panel, in: .rect(cornerRadius: 10))
                .focused($composing)
                .onSubmit { send(model.chatDraft) }
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
                        model.chatDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                    )
                    .keyboardShortcut(.return, modifiers: .command)
            }
        }
        .padding(10)
    }

    /// openProviders opens Settings, for its agent providers: over the
    /// panel beside the page, or once the panel as a sheet has gone.
    private func openProviders() {
        scene.openSettings()
    }

    private func send(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        model.chatDraft = ""
        let focus = scene.editor?.focusBlockIDs() ?? []
        Task { await app.client?.chat(model.id, text: t, focus: focus) }
    }
}

/// MessageView is one turn of the chat: the user's, or the agent's reply
/// with what its run changed.
struct MessageView: View {
    let message: AgentState.Message
    let run: AgentState.Run?
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: message.mine ? .trailing : .leading, spacing: 6) {
            if !message.mine, let name = message.agentName, !name.isEmpty {
                Text(name).font(.caption.weight(.semibold)).foregroundStyle(palette.textMuted)
            }
            if message.pending && !message.streamed {
                // What the run is doing, bare, as the desktop shows it.
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
                    // A link to a page, a block of one or a moment of a video
                    // opens it here, as one in the page does.
                    .environment(
                        \.openURL,
                        OpenURLAction { [scene] url in
                            guard url.scheme == "leaflet", let editor = scene.editor else {
                                return .systemAction
                            }
                            editor.open(url)
                            return .handled
                        })
            }
            if let run, !run.edits.isEmpty {
                RunCard(run: run, pageID: model.id)
            }
        }
        .frame(maxWidth: .infinity, alignment: message.mine ? .trailing : .leading)
    }
}

/// StatusLine is what an agent's run is doing, as a terminal agent's
/// "Thinking…" line: muted text that a brighter band sweeps across, over
/// and over, as on the desktop; still where motion is reduced.
struct StatusLine: View {
    let text: String
    @Environment(\.palette) private var palette
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// band is the band's half width, as a share of the text; cycle, the
    /// seconds it takes to cross it.
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
                // From wholly before the text to wholly past it.
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

/// RunCard is what a run changed, with Undo or Redo as one step.
struct RunCard: View {
    let run: AgentState.Run
    let pageID: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            // By place: two edits can say the same.
            ForEach(Array(run.edits.enumerated()), id: \.offset) { _, e in
                Label(e, systemImage: "pencil.line").font(.footnote)
            }
            // What the agent said is in its message above.
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

    /// step undoes or redoes the run, saying in a toast why it couldn't:
    /// the core says, and leaves the run as it was.
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
