import SwiftUI

extension AppModel {
    func opens(_ id: String) -> Bool {
        library.page(id) != nil || templates.contains { $0.pageID == id }
    }

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

    func createFolder(named name: String, in parentID: String = "") async {
        do {
            _ = try await client?.createFolder(name, in: parentID)
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

    func defaultTemplate(_ id: String?) -> TemplateRow? {
        guard let id, !id.isEmpty else { return nil }
        return templates.first { $0.pageID == id }
    }

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

    func move(_ page: PageRow, parentID: String, folderID: String) async {
        do {
            try await client?.movePage(page.id, parentID: parentID, folderID: folderID)
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    /// Puts the folder into the folder parentID, last, or at the top level when parentID is empty.
    func moveFolder(_ id: String, into parentID: String) async {
        guard id != parentID, library.folder(id) != nil,
            parentID.isEmpty || !library.folder(parentID, isIn: id)
        else { return }
        do {
            try await client?.moveFolder(id, parentID: parentID)
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    /// Puts the folder beside the folder target, in the folder that one is in.
    func placeFolder(_ id: String, beside target: String, before: Bool) async {
        guard id != target, library.folder(id) != nil, !library.folder(target, isIn: id)
        else { return }
        let parent = library.folderTree.parent[target] ?? ""
        do {
            if before {
                try await client?.moveFolderBefore(id, parentID: parent, beforeID: target)
            } else {
                try await client?.moveFolder(id, parentID: parent, afterID: target)
            }
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    func place(_ id: String, beside target: PageRow, before: Bool) async {
        guard let page = library.page(id), page.id != target.id, page.canEdit, target.canEdit,
            !page.template, !target.template, library.own(page), library.own(target),
            !library.path(to: target).contains(where: { $0.id == page.id })
        else { return }
        let parent = target.parentID ?? ""
        let folder = target.folderID ?? ""
        do {
            if before {
                try await client?.movePageBefore(
                    page.id, parentID: parent, folderID: folder, beforeID: target.id)
            } else {
                try await client?.movePage(
                    page.id, parentID: parent, folderID: folder, afterID: target.id)
            }
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    func upload(_ urls: [URL], folderID: String = "") async {
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                try await client?.addFolderFile(folderID, path: url.path)
            } catch {
                show(Toast(message: L.hostErrorAddFile(error: error.localizedDescription)))
            }
        }
        await reload(.library)
    }

    func delete(_ file: FileRow) async {
        do {
            try await client?.deleteFile(file.id)
            await reload(.library)
        } catch {
            show(Toast(message: L.sidebarCouldNotDelete(error: error.localizedDescription)))
        }
    }

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
