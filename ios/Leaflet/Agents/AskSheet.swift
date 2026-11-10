import SwiftUI

struct AskRequest: Identifiable, Equatable {
    let id = UUID()
    var focus: [String]
    var selection: String
    var agent: AgentState.Option?
    var prompt = ""
}

struct AskSheet: View {
    let request: AskRequest
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @State private var runID: String?
    @State private var asking = false
    @State private var failure: String?
    @FocusState private var focused: Bool
    @State private var fit: CGFloat = 0
    @State private var detent = PresentationDetent.medium

    private var run: AgentState.Run? {
        runID.flatMap { id in model.agents.runs.first { $0.id == id } }
    }

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
                            if let answer = run.answer, !answer.isEmpty {
                                MarkdownPreview(markdown: answer)
                            } else if let activity = run.activity, !activity.isEmpty {
                                Label(activity, systemImage: "globe")
                            } else {
                                Label(L.dialogInlineWorking, systemImage: "sparkles")
                            }
                        } else if let e = run.error, !e.isEmpty {
                            Text(e).foregroundStyle(palette.dangerText)
                        } else {
                            MarkdownPreview(markdown: run.summary)
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

    static func closes(_ run: AgentState.Run) -> Bool {
        !run.edits.isEmpty && (run.running || (run.error ?? "").isEmpty)
    }

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
            .buttonStyle(.borderless)
        }
    }

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
