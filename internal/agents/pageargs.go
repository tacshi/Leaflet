package agents

import (
	"strings"

	"leaflet/internal/store"
)

// The tools that make, name, tag and file pages and folders, which the agent
// of the workspace and agents outside the app (leaflet mcp) both have, read
// their arguments here alike. Which pages and folders a call may name and
// change is each surface's own policy, which it passes in.

// TitleArg is the title a call gives a page: required, without the space
// around it, and no longer than a title is.
func TitleArg(args map[string]any) (string, error) {
	title, err := str(args, "title")
	if err != nil {
		return "", err
	}
	return store.ClampTitle(strings.TrimSpace(title)), nil
}

// TagsArg is the tags a call gives a page, a list of words, which an empty
// list takes all off.
func TagsArg(args map[string]any) ([]string, error) {
	raw, ok := args["tags"].([]any)
	if !ok {
		return nil, terr("tags must be a list of words.")
	}
	tags := make([]string, 0, len(raw))
	for _, t := range raw {
		s, ok := t.(string)
		if !ok {
			return nil, terr("tags must be a list of words.")
		}
		tags = append(tags, s)
	}
	return tags, nil
}

// FolderNameArg is the name a call gives a folder: required, without the
// space around it.
func FolderNameArg(args map[string]any) (string, error) {
	name, err := str(args, "name")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// ParentFolderArg is the folder a call puts a folder in, parent_folder_id,
// or none for the top level.
func ParentFolderArg(args map[string]any) (string, error) {
	id, err := optionalString(args, "parent_folder_id")
	return strings.TrimSpace(id), err
}

// Place is where a call puts a page, as create_page and move_page take it:
// inside the page parent_id names, as page finds it and checks it may go
// there, or in the folder folder_id names, as folder checks it where the
// surface checks it here, not both, or with neither at the top level.
func Place[P any](args map[string]any, page func(id string) (P, error), folder func(id string) error) (parent P, parentID, folderID string, err error) {
	if parentID, err = optionalString(args, "parent_id"); err != nil {
		return parent, "", "", err
	}
	if folderID, err = optionalString(args, "folder_id"); err != nil {
		return parent, "", "", err
	}
	parentID, folderID = strings.TrimSpace(parentID), strings.TrimSpace(folderID)
	switch {
	case parentID != "" && folderID != "":
		return parent, "", "", terr("Give parent_id or folder_id, not both: a page inside another is in no folder.")
	case parentID != "":
		parent, err = page(parentID)
		return parent, parentID, "", err
	case folderID != "" && folder != nil:
		return parent, "", folderID, folder(folderID)
	}
	return parent, "", folderID, nil
}
