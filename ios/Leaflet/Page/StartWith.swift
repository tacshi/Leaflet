import SwiftUI

struct StartWith: View {
    let page: PageRow
    let draft: () -> Void
    let addSubpage: () -> Void
    let upload: () -> Void
    let apply: (TemplateRow) -> Void
    let edit: (TemplateRow) -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            title(L.pageStartWith)
            row("sparkles", L.pageDraftWithAgent, L.pageDraftWithAgentHint, action: draft)
            row("doc.badge.plus", L.pageAddSubpage, L.pageAddSubpageHint, action: addSubpage)
            row("square.and.arrow.up", L.pageUpload, L.pageUploadHint, action: upload)
            if !page.template, !app.templates.isEmpty {
                title(L.templateSectionTitle).padding(.top, 10)
                ForEach(app.templates) { t in
                    row(t.symbol, t.title, t.hint) { apply(t) }
                        .contextMenu {
                            Button(L.templateEdit, systemImage: "pencil") { edit(t) }
                            Button(L.templateDelete, systemImage: "trash", role: .destructive) {
                                Task { await app.deleteTemplate(t) }
                            }
                        }
                }
            }
        }
        .frame(maxWidth: 420, alignment: .leading)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func title(_ text: String) -> some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .foregroundStyle(palette.textMuted)
            .padding(.leading, 2)
    }

    private func row(
        _ symbol: String, _ label: String, _ hint: String, action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: symbol)
                    .font(.system(size: 15))
                    .foregroundStyle(palette.icon)
                    .frame(width: 30, height: 30)
                    .background(palette.ground, in: .rect(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.divider))
                VStack(alignment: .leading, spacing: 1) {
                    Text(label).font(.subheadline.weight(.medium)).foregroundStyle(palette.text)
                    Text(hint).font(.caption).foregroundStyle(palette.textMuted)
                }
                .lineLimit(1)
                Spacer(minLength: 0)
            }
            .padding(.vertical, 8)
            .padding(.horizontal, 10)
            .contentShape(.rect(cornerRadius: 8))
        }
        .buttonStyle(.plain)
        .hoverEffect()
    }
}
