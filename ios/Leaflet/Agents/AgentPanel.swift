import SwiftUI

/// AgentPanel is the page's agent chat, as the desktop's Agent tab: the
/// conversation, each run's edits with Undo, Stop while one works, the
/// agent to ask, and suggestions for an empty chat.
struct AgentPanel: View {
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @State private var draft = ""
    @FocusState private var composing: Bool
    /// line is the composer's height with one line: its buttons take it, so
    /// they sit level with a line's text, and at the foot of a longer draft.
    @ScaledMetric(relativeTo: .body) private var line: CGFloat = 38
    /// following keeps a reply in view as it streams in, until the chat is
    /// scrolled up from its foot.
    @State private var following = true
    @State private var atFoot = true

    private var state: AgentState { model.agents }

    var body: some View {
        if !state.available && state.options.isEmpty {
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
            TextField(L.panelAskPlaceholder, text: $draft, axis: .vertical)
                .lineLimit(1...6)
                .textFieldStyle(.plain)
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
                .frame(minHeight: line)
                .background(palette.panel, in: .rect(cornerRadius: 10))
                .focused($composing)
                .onSubmit { send(draft) }
            if state.running, let run = state.runs.last(where: { $0.running && $0.stoppable }) {
                Button(L.pageStopRun, systemImage: "stop.circle.fill") {
                    Task { await app.client?.stopAgent(model.id, runID: run.id) }
                }
                .labelStyle(.iconOnly)
                .imageScale(.large)
                .frame(minWidth: 28, minHeight: line)
            } else {
                Button(L.panelSend, systemImage: "arrow.up.circle.fill") { send(draft) }
                    .labelStyle(.iconOnly)
                    .imageScale(.large)
                    .frame(minWidth: 28, minHeight: line)
                    .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
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
        draft = ""
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
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: message.mine ? .trailing : .leading, spacing: 6) {
            if !message.mine, let name = message.agentName, !name.isEmpty {
                Text(name).font(.caption.weight(.semibold)).foregroundStyle(palette.textMuted)
            }
            Group {
                if message.pending && message.text.isEmpty {
                    ProgressView().controlSize(.small)
                } else {
                    MarkdownText(text: message.text)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(
                message.mine ? palette.accentSoft : palette.panel, in: .rect(cornerRadius: 12)
            )
            .foregroundStyle(message.error ? palette.dangerText : palette.text)
            .textSelection(.enabled)
            if let run, !run.edits.isEmpty {
                RunCard(run: run, pageID: model.id)
            }
        }
        .frame(maxWidth: .infinity, alignment: message.mine ? .trailing : .leading)
    }
}

/// MarkdownText shows an agent's answer: its ## headings, - and 1. lists
/// and paragraphs, each with inline markdown.
struct MarkdownText: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                row(line)
            }
        }
    }

    private var lines: [Substring] {
        text.split(separator: "\n", omittingEmptySubsequences: true)
    }

    @ViewBuilder private func row(_ line: Substring) -> some View {
        let t = line.trimmingCharacters(in: .whitespaces)
        if let heading = t.firstMatch(of: /^#{1,6}\s+(.*)/) {
            inline(String(heading.1)).font(.headline)
        } else if let item = t.firstMatch(of: /^[-*•]\s+(.*)/) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text("•")
                inline(String(item.1))
            }
            .padding(.leading, indent(line))
        } else if let item = t.firstMatch(of: /^(\d+[.)])\s+(.*)/) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(item.1).monospacedDigit()
                inline(String(item.2))
            }
            .padding(.leading, indent(line))
        } else {
            inline(t)
        }
    }

    /// indent is a nested item's, from its leading spaces.
    private func indent(_ line: Substring) -> CGFloat {
        CGFloat(line.prefix { $0 == " " }.count / 2) * 14
    }

    private func inline(_ s: String) -> Text {
        Text(
            (try? AttributedString(
                markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
                ?? AttributedString(s))
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
