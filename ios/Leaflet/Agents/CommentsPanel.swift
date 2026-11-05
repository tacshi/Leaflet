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
    /// sending says the new comment is on its way to the core.
    @State private var sending = false

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
            TextField(
                L.panelCommentPlaceholder, text: Bindable(model).commentDraft, axis: .vertical
            )
            .lineLimit(2...8)
            HStack {
                Spacer()
                Button(L.commonCancel) {
                    scene.newComment = nil
                    model.commentDraft = ""
                }
                Button(L.panelSend) {
                    let body = model.commentDraft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !body.isEmpty, !sending else { return }
                    sending = true
                    Task {
                        defer { sending = false }
                        // The comment stays, to send again, until the core
                        // has it.
                        do {
                            try await app.client?.addComment(
                                model.id, blockID: new.blockID, body: body, quote: new.quote)
                        } catch {
                            app.show(
                                Toast(message: L.appErrorSave(error: error.localizedDescription)))
                            return
                        }
                        if scene.newComment == new {
                            scene.newComment = nil
                            model.commentDraft = ""
                        }
                        await model.refreshThreads()
                    }
                }
                .disabled(sending)
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

    /// reply is the reply being written, which the page keeps.
    private var reply: String {
        get { model.replies[thread.root.id] ?? "" }
        nonmutating set { model.replies[thread.root.id] = newValue.isEmpty ? nil : newValue }
    }

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
                if thread.mayResolve {
                    resolveButton
                }
            }
            ForEach([thread.root] + thread.replies) { c in
                CommentRow(comment: c, model: model)
            }
            if !thread.root.resolved {
                HStack(alignment: .lastTextBaseline) {
                    TextField(
                        L.panelReplyPlaceholder,
                        text: Binding(get: { reply }, set: { reply = $0 }), axis: .vertical
                    )
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

    /// resolveButton resolves the thread, or reopens it.
    private var resolveButton: some View {
        Button(thread.root.resolved ? L.panelReopen : L.panelResolve) {
            let reopen = thread.root.resolved
            Task {
                do {
                    try await app.client?.resolveThread(thread.root.id, resolved: !reopen)
                } catch {
                    let why = error.localizedDescription
                    app.show(
                        Toast(
                            message: reopen
                                ? L.panelCouldNotReopen(error: why)
                                : L.panelCouldNotResolve(error: why)))
                }
                await model.refreshThreads()
            }
        }
        .font(.footnote)
    }

    private func send() {
        let body = reply.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { return }
        reply = ""
        Task {
            do {
                try await app.client?.addComment(
                    model.id, blockID: thread.root.blockID, body: body, parentID: thread.root.id)
            } catch {
                // The reply comes back, to send again, unless another was
                // begun meanwhile.
                if reply.isEmpty { reply = body }
                app.show(Toast(message: L.appErrorSave(error: error.localizedDescription)))
            }
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
                if let note = Self.note(comment) {
                    Text(note).font(.caption).foregroundStyle(palette.textMuted).lineLimit(1)
                }
                // As the byline says when, in the app's language, moving on
                // as the panel stays open.
                TimelineView(.everyMinute) { context in
                    Text(Byline.humanTime(comment.created, now: context.date))
                        .font(.caption).foregroundStyle(palette.textMuted)
                }
            }
            if comment.deleted == true {
                // A first comment deleted stays for its replies, as the
                // desktop shows it.
                Text(L.panelCommentDeleted).font(.subheadline).foregroundStyle(palette.textMuted)
            } else {
                Text(Self.text(comment.body))
                    .font(.subheadline)
                    .textSelection(.enabled)
            }
        }
        .contextMenu {
            if comment.mayDelete {
                deleteButton
            }
        }
    }

    /// note says how a comment came, after its author, as the desktop's
    /// comments do: who had an agent write it, or by the page's public
    /// link, through which a guest names themself, any name.
    static func note(_ comment: CommentThread.Comment) -> String? {
        comment.authorKind == "guest" ? L.panelViaPublicLink : comment.askedBy
    }

    /// text is a comment's text, its inline Markdown read, with only the
    /// links the page itself opens (EditorStyle.linkTarget): a web, mail
    /// or page link. Whoever comments, a guest by a public link too, could
    /// otherwise link a file, or another app to do as the link says.
    static func text(_ body: String) -> AttributedString {
        var out =
            (try? AttributedString(
                markdown: body, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(body)
        let links = out.runs[\.link].compactMap { link, range in link.map { ($0, range) } }
        for (link, range) in links {
            out[range].link = EditorStyle.linkTarget([.leafletHref: link.absoluteString])
        }
        return out
    }

    /// deleteButton deletes the comment.
    private var deleteButton: some View {
        Button(L.panelDeleteComment, systemImage: "trash", role: .destructive) {
            Task {
                do {
                    try await app.client?.deleteComment(comment.id)
                } catch {
                    app.show(
                        Toast(message: L.panelCouldNotDelete(error: error.localizedDescription))
                    )
                }
                await model.refreshThreads()
            }
        }
    }
}
