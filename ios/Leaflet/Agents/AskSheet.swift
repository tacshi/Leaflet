import SwiftUI

/// AskRequest is an agent being asked from the editor, about the blocks
/// in focus and the text chosen in them.
struct AskRequest: Identifiable, Equatable {
    let id = UUID()
    var focus: [String]
    var selection: String
    /// agent is the one chosen in "@", or nil for the default.
    var agent: AgentState.Option?
    /// prompt is what the ask starts with, for the user to send or change:
    /// a video's summary, from its menu.
    var prompt = ""
}

/// AskSheet asks an agent to write, rewrite or explain, as the desktop's
/// inline prompt: with the quote, quick asks, and the run's progress until
/// it is done. The agent is the one chosen in "@", else the one Settings
/// makes the default.
struct AskSheet: View {
    let request: AskRequest
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @State private var runID: String?
    /// asking says an ask is on its way to the core, before it has its
    /// run: a second tap meanwhile asks nothing more.
    @State private var asking = false
    /// failure says why the ask didn't start a run: the field stays, to
    /// ask again.
    @State private var failure: String?
    @FocusState private var focused: Bool
    /// fit is the sheet's height for what it shows, as the desktop's
    /// prompt is no taller than its content; detent is the height shown,
    /// fit or, pulled up, large.
    @State private var fit: CGFloat = 0
    @State private var detent = PresentationDetent.medium

    private var run: AgentState.Run? {
        runID.flatMap { id in model.agents.runs.first { $0.id == id } }
    }

    /// agentName names the agent asked.
    private var agentName: String {
        if let agent = request.agent { return agent.name }
        let id = model.agents.defaultAgent?.providerID
        return model.agents.options.first { $0.providerID == id }?.name ?? L.commonAgent
    }

    var body: some View {
        NavigationStack {
            Form {
                if !request.selection.isEmpty {
                    Section {
                        Text(L.panelQuote(text: request.selection))
                            .foregroundStyle(palette.textMuted)
                            .lineLimit(4)
                    }
                }
                Section {
                    TextField(L.dialogInlinePlaceholder, text: $text, axis: .vertical)
                        .lineLimit(2...8)
                        .focused($focused)
                        .onSubmit { send(text) }
                        .disabled(runID != nil)
                    if runID == nil, !request.selection.isEmpty {
                        // Chips in a row, as the desktop's; one under
                        // another where the row has no room for them.
                        ViewThatFits(in: .horizontal) {
                            HStack(spacing: 8) { quickAsks }
                            VStack(alignment: .leading, spacing: 8) { quickAsks }
                        }
                    }
                }
                if let failure {
                    Section {
                        Text(failure).foregroundStyle(palette.dangerText)
                    }
                }
                if let run {
                    Section {
                        if run.running {
                            // The answer as it streams in, until there is
                            // some.
                            if let answer = run.answer, !answer.isEmpty {
                                MarkdownPreview(markdown: answer)
                            } else if let activity = run.activity, !activity.isEmpty {
                                Label(activity, systemImage: "globe")
                            } else {
                                Label(L.dialogInlineWorking, systemImage: "sparkles")
                            }
                        } else if let e = run.error, !e.isEmpty {
                            // The summary is the error then: once is enough.
                            Text(e).foregroundStyle(palette.dangerText)
                        } else {
                            MarkdownPreview(markdown: run.summary)
                            // An answer in words can go in the page, as the
                            // desktop's prompt offers; edits are there already.
                            if run.edits.isEmpty, !run.undone {
                                Button(L.dialogInlineInsert, systemImage: "plus") {
                                    insertReply(run)
                                }
                            }
                        }
                    }
                }
            }
            .onScrollGeometryChange(for: CGFloat.self) { geo in
                geo.contentSize.height + geo.contentInsets.top + geo.contentInsets.bottom
            } action: { _, height in
                fit = height
            }
            .navigationTitle(L.dialogInlineAsk(name: agentName))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    if let run, run.running {
                        Button(L.pageStopRun) {
                            Task { await app.client?.stopAgent(model.id, runID: run.id) }
                        }
                    } else {
                        Button(runID == nil ? L.commonCancel : L.commonDone) { dismiss() }
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if runID == nil {
                        Button(L.dialogInlineSend) { send(text) }
                            .disabled(
                                asking
                                    || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }
            }
        }
        .presentationDetents(
            fit > 0 ? [.height(fit), .large] : [.medium, .large], selection: $detent
        )
        // It keeps to its content as that changes, unless pulled up. Pulled
        // down, it goes and the run goes on, as the desktop's prompt
        // closed: the Agent panel shows it, with Stop and Undo.
        .onChange(of: fit) { _, height in
            if detent != .large { detent = .height(height) }
        }
        .onAppear {
            if text.isEmpty { text = request.prompt }
            focused = true
        }
        .onChange(of: run?.status) { if let run, Self.closes(run) { dismiss() } }
        .onChange(of: run?.edits.count) { if let run, Self.closes(run) { dismiss() } }
    }

    /// closes says the sheet goes for run: once it writes in the page, for
    /// the page to show the writing as it comes, as the desktop's prompt
    /// sits below it, with Stop and Undo in the Agent panel; and once it
    /// is done with edits. An answer in words stays, with Insert, as no
    /// message keeps it, and so does an error.
    static func closes(_ run: AgentState.Run) -> Bool {
        !run.edits.isEmpty && (run.running || (run.error ?? "").isEmpty)
    }

    /// quickAsks are the asks about a selection to send with a tap.
    @ViewBuilder private var quickAsks: some View {
        ForEach(
            [L.dialogInlineExplain, L.dialogInlineShorter, L.dialogInlineGrammar], id: \.self
        ) { q in
            Button {
                send(q)
            } label: {
                Text(q)
                    .font(.subheadline)
                    .foregroundStyle(palette.accentText)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(palette.accentSoft, in: .capsule)
                    .overlay(Capsule().strokeBorder(palette.accentSoftBorder))
            }
            // Each chip its own button: a row's button would take the row.
            .buttonStyle(.borderless)
        }
    }

    /// insertReply has the page's editor put the run's answer after the
    /// last block it was about, as one step to undo, and the sheet goes.
    private func insertReply(_ run: AgentState.Run) {
        Task {
            do {
                let blocks = try await app.client?.replyBlocks(model.id, runID: run.id) ?? []
                scene.editor?.insertBlocks(blocks, after: request.focus.last ?? "")
                dismiss()
            } catch {
                failure = error.localizedDescription
            }
        }
    }

    private func send(_ instruction: String) {
        let t = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, runID == nil, !asking else { return }
        asking = true
        let surface = request.selection.isEmpty ? "inline" : "selection"
        Task {
            defer { asking = false }
            guard let client = app.client else { return }
            do {
                let id = try await client.ask(
                    model.id, surface: surface, instruction: t, focus: request.focus,
                    selection: request.selection,
                    agent: request.agent)
                await model.refreshAgents()
                // A run the page doesn't have would leave the sheet waiting
                // on it, with nothing to show and nothing to ask. The core
                // says, as the page's runs may be a later read's, not in yet.
                let runs = try? await client.agentState(model.id).runs
                guard !id.isEmpty, runs?.contains(where: { $0.id == id }) ?? true else {
                    failure = LI.agentNotStarted
                    return
                }
                failure = nil
                runID = id
            } catch {
                failure = error.localizedDescription
            }
        }
    }
}
