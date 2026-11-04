import SwiftUI

/// What the library's menus do: pages, folders and tags made, renamed,
/// moved and deleted, and pages from templates and copies of them, each
/// failure said in a toast.
extension AppModel {
    /// opens reports whether page id is here to open: one of the library's,
    /// or a template's, not one deleted or not synced yet.
    func opens(_ id: String) -> Bool {
        library.page(id) != nil || templates.contains { $0.pageID == id }
    }

    /// createRecordingPage makes a page of its own to record in, named for
    /// its recording, as the desktop's New Recording does, and returns its
    /// ID.
    func createRecordingPage() async -> String? {
        do {
            let page = try await client?.createPage(NewPage(title: L.recBlockTitle(when: Date())))
            await reload()
            return page?.id
        } catch {
            show(Toast(message: L.appErrorCreatePage(error: error.localizedDescription)))
            return nil
        }
    }

    /// createPage makes a page, under parentID or in folderID, and returns
    /// its ID, for the window to open it; link puts a card for a subpage
    /// at the end of its parent. It starts from the template its parent or
    /// folder names for new pages, as on the desktop.
    func createPage(parentID: String = "", folderID: String = "", link: Bool = false) async
        -> String?
    {
        do {
            let page = try await client?.createPage(
                NewPage(parentID: parentID, folderID: folderID, link: link, start: true))
            await reload()
            return page?.id
        } catch {
            show(Toast(message: L.appErrorCreatePage(error: error.localizedDescription)))
            return nil
        }
    }

    func createFolder(named name: String) async {
        do {
            _ = try await client?.createFolder(name)
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
        }
    }

    func rename(_ page: PageRow, to title: String) async {
        do {
            try await client?.renamePage(page.id, title: title)
        } catch {
            show(Toast(message: L.appErrorRename(error: error.localizedDescription)))
        }
        await reload()
    }

    /// renameFolder renames a folder, as the desktop's Rename folder does:
    /// a name of spaces alone changes nothing.
    func renameFolder(_ folder: FolderRow, to name: String) async {
        let name = name.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty, name != folder.name else { return }
        do {
            try await client?.renameFolder(folder.id, name: name)
        } catch {
            show(Toast(message: L.dialogFolderRenameFailed(error: error.localizedDescription)))
        }
        await reload()
    }

    /// delete deletes a page and its subpages, with Undo in a toast: what
    /// was typed in each window goes first, for Undo to bring the pages
    /// back with it.
    func delete(_ page: PageRow) async {
        guard let client else { return }
        for scene in scenes.allObjects { await scene.flush() }
        do {
            let ids = try await client.deletePage(page.id)
            await reload()
            show(
                Toast(
                    message: L.appDeleted(
                        title: page.title.isEmpty ? L.commonUntitled : page.title),
                    actionLabel: L.commonUndo,
                    action: { [weak self] in
                        do {
                            try await client.restorePages(ids)
                        } catch {
                            let message = L.appErrorRestore(error: error.localizedDescription)
                            self?.show(Toast(message: message))
                        }
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.sidebarCouldNotDelete(error: error.localizedDescription)))
        }
    }

    func deleteFolder(_ folder: FolderRow) async {
        do {
            try await client?.deleteFolder(folder.id)
            await reload()
            show(Toast(message: L.sidebarFolderDeleted(name: folder.name)))
        } catch {
            show(Toast(message: L.sidebarCouldNotDelete(error: error.localizedDescription)))
        }
    }

    /// renameTag renames a tag on every page the user may change, and
    /// returns its name as kept, or nil when nothing was renamed.
    func renameTag(_ tag: String, to name: String) async -> String? {
        do {
            let kept = try await client?.renameTag(tag, to: name) ?? ""
            await reload()
            return kept.isEmpty ? nil : kept
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
            return nil
        }
    }

    /// deleteTag takes a tag off every page the user may change, with
    /// Undo in a toast to put each page's tags back as they were.
    func deleteTag(_ tag: String) async {
        guard let client else { return }
        do {
            let before = try await client.deleteTag(tag)
            await reload()
            show(
                Toast(
                    message: L.tagDeleted(name: tag), actionLabel: L.commonUndo,
                    action: { [weak self] in
                        do {
                            try await client.restoreTags(before)
                        } catch {
                            self?.show(
                                Toast(
                                    message: L.sidebarCouldNotSave(
                                        error: error.localizedDescription)))
                        }
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
        }
    }

    // MARK: Templates and copies

    /// applyTemplate fills a blank page from a template, and returns the
    /// title an untitled page took from it, or nil when it took none.
    func applyTemplate(_ key: String, to pageID: String) async -> String? {
        do {
            let title = try await client?.applyTemplate(pageID, key: key) ?? ""
            await reload()
            return title.isEmpty ? nil : title
        } catch {
            show(Toast(message: L.templateUseFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// insertTemplate puts a template's blocks in a page after a block, as
    /// the / menu does, and returns the title an empty, untitled page took
    /// from it, or nil.
    func insertTemplate(_ key: String, into pageID: String, after blockID: String) async
        -> String?
    {
        do {
            let title = try await client?.insertTemplate(pageID, after: blockID, key: key) ?? ""
            await reload()
            return title.isEmpty ? nil : title
        } catch {
            show(Toast(message: L.templateUseFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// editTemplate returns the page of a template to open and change.
    func editTemplate(_ template: TemplateRow) async -> String? {
        do {
            let id = try await client?.editTemplate(template.key)
            await reload()
            return id
        } catch {
            show(Toast(message: L.templateSaveFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// deleteTemplate deletes one of the user's templates as a page, or
    /// hides a starter on this device, with Undo.
    func deleteTemplate(_ template: TemplateRow) async {
        if let id = template.pageID, let page = library.page(id) {
            await delete(page)
            return
        }
        do {
            try await client?.setStarterDeleted(template.key, deleted: true)
            await reload()
            show(
                Toast(
                    message: L.appDeleted(title: template.title), actionLabel: L.commonUndo,
                    action: { [weak self] in
                        try? await self?.client?.setStarterDeleted(template.key, deleted: false)
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.appErrorDelete(error: error.localizedDescription)))
        }
    }

    /// setDefaultTemplate chooses the template new pages under a page or in
    /// a folder start from, nil for blank ones.
    func setDefaultTemplate(
        _ template: TemplateRow?, page: PageRow? = nil, folder: FolderRow? = nil
    ) async {
        do {
            let message =
                try await client?.setDefaultTemplate(
                    pageID: page?.id ?? "", folderID: folder?.id ?? "", key: template?.key ?? "")
                ?? ""
            await reload()
            show(Toast(message: message))
        } catch {
            show(Toast(message: L.templateDefaultFailed(error: error.localizedDescription)))
        }
    }

    /// defaultTemplate is the template a page or folder names for new
    /// pages, if the user has it.
    func defaultTemplate(_ id: String?) -> TemplateRow? {
        guard let id, !id.isEmpty else { return nil }
        return templates.first { $0.pageID == id }
    }

    /// duplicate copies a page and its subpages beside it, and returns the
    /// copy's ID.
    func duplicate(_ page: PageRow) async -> String? {
        do {
            let copy = try await client?.duplicatePage(page.id)
            await reload()
            return copy?.id
        } catch {
            show(Toast(message: L.templateDuplicateFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// saveAsTemplate keeps a copy of a page as a template, with Edit in
    /// the toast to open it.
    func saveAsTemplate(_ page: PageRow, open: @escaping @MainActor (String) -> Void) async {
        do {
            guard let t = try await client?.saveAsTemplate(page.id) else { return }
            await reload()
            show(
                Toast(
                    message: L.templateSaved(title: t.displayTitle), actionLabel: L.commonEdit,
                    action: { open(t.id) }))
        } catch {
            show(Toast(message: L.templateSaveFailed(error: error.localizedDescription)))
        }
    }

    /// move moves a page under parentID, into folderID, or to the top
    /// level with neither, after the pages already there.
    func move(_ page: PageRow, parentID: String, folderID: String) async {
        do {
            try await client?.movePage(page.id, parentID: parentID, folderID: folderID)
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    /// syncs reports whether pages go anywhere but this device, through
    /// an account or iCloud: only then is keeping one here a choice.
    var syncs: Bool { status?.signedIn ?? false || status?.iCloudOn ?? false }

    func setKeepOffline(_ page: PageRow, _ keep: Bool) async {
        do {
            try await client?.setKeepOffline(page.id, keep: keep)
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
        }
        await reload()
    }
}
