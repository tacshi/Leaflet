import SwiftUI

/// NewComment is a comment being started on a block, from the editor's
/// selection.
struct NewComment: Equatable {
    var blockID: String
    var quote: String
}

/// CommentsPanel is the page's comment threads, as the desktop's Comments
/// tab: a new comment on the text chosen, threads with their replies,
/// Resolve and Reopen, and @agent to have an agent answer.
struct CommentsPanel: View {
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @State private var showResolved = false
    @State private var draft = ""

    private var open: [CommentThread] { model.threads.filter { !$0.root.resolved } }
    private var resolved: [CommentThread] { model.threads.filter { $0.root.resolved } }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 12) {
                if let new = scene.newComment {
                    newCard(new)
                }
                if open.isEmpty && scene.newComment == nil {
                    ContentUnavailableView(
                        L.panelCommentsEmpty, systemImage: "text.bubble",
                        description: Text(L.panelCommentsEmptyHint))
                }
                ForEach(open) { ThreadCard(thread: $0, model: model) }
                if !resolved.isEmpty {
                    Button(
                        showResolved
                            ? L.panelHideResolved : L.panelShowResolved(count: resolved.count)
                    ) {
                        showResolved.toggle()
                    }
                    .font(.footnote)
                    if showResolved {
                        ForEach(resolved) { ThreadCard(thread: $0, model: model).opacity(0.7) }
                    }
                }
            }
            .padding(14)
        }
    }

    private func newCard(_ new: NewComment) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if !new.quote.isEmpty {
                Text(L.panelQuote(text: new.quote)).font(.footnote).foregroundStyle(
                    palette.textMuted
                ).lineLimit(2)
            }
            TextField(L.panelCommentPlaceholder, text: $draft, axis: .vertical)
                .lineLimit(2...8)
            HStack {
                Spacer()
                Button(L.commonCancel) {
                    scene.newComment = nil
                    draft = ""
                }
                Button(L.panelSend) {
                    let body = draft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !body.isEmpty else { return }
                    Task {
                        try? await app.client?.addComment(
                            model.id, blockID: new.blockID, body: body, quote: new.quote)
                        await model.refreshThreads()
                    }
                    scene.newComment = nil
                    draft = ""
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.return, modifiers: .command)
            }
        }
        .padding(12)
        .background(palette.panel, in: .rect(cornerRadius: 12))
    }
}

/// ThreadCard is a thread: what it is on, its comments, a reply, and
/// Resolve or Reopen.
struct ThreadCard: View {
    let thread: CommentThread
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @State private var reply = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top) {
                Button {
                    scene.editor?.reveal(blockID: thread.root.blockID)
                } label: {
                    Text(thread.root.quote.map { L.panelQuote(text: $0) } ?? L.panelOnBlock)
                        .font(.footnote)
                        .foregroundStyle(palette.textMuted)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }
                .buttonStyle(.plain)
                Spacer()
                Button(thread.root.resolved ? L.panelReopen : L.panelResolve) {
                    Task {
                        try? await app.client?.resolveThread(
                            thread.root.id, resolved: !thread.root.resolved)
                        await model.refreshThreads()
                    }
                }
                .font(.footnote)
            }
            ForEach([thread.root] + thread.replies) { c in
                CommentRow(comment: c, model: model)
            }
            if !thread.root.resolved {
                HStack(alignment: .lastTextBaseline) {
                    TextField(L.panelReplyPlaceholder, text: $reply, axis: .vertical)
                        .lineLimit(1...6)
                        .onSubmit(send)
                    Button(L.panelReply, action: send)
                        .disabled(reply.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .keyboardShortcut(.return, modifiers: .command)
                }
                .font(.subheadline)
            }
        }
        .padding(12)
        .background(palette.surface, in: .rect(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(palette.border, lineWidth: 0.5))
    }

    private func send() {
        let body = reply.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { return }
        reply = ""
        Task {
            try? await app.client?.addComment(
                model.id, blockID: thread.root.blockID, body: body, parentID: thread.root.id)
            await model.refreshThreads()
        }
    }
}

/// CommentRow is one comment: who wrote it and when, and what.
struct CommentRow: View {
    let comment: CommentThread.Comment
    let model: PageModel
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Image(systemName: comment.authorKind == "agent" ? "sparkles" : "person.crop.circle")
                    .foregroundStyle(palette.accent)
                Text(comment.authorName.isEmpty ? L.pageBylineSomeone : comment.authorName)
                    .font(.footnote.weight(.semibold))
                Text(comment.created, format: .relative(presentation: .named))
                    .font(.caption).foregroundStyle(palette.textMuted)
            }
            Text(
                (try? AttributedString(
                    markdown: comment.body,
                    options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
                    ?? AttributedString(comment.body)
            )
            .font(.subheadline)
            .textSelection(.enabled)
        }
        .contextMenu {
            Button(L.panelDeleteComment, systemImage: "trash", role: .destructive) {
                Task {
                    try? await app.client?.deleteComment(comment.id)
                    await model.refreshThreads()
                }
            }
        }
    }
}
