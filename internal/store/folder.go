package store

// Folder returns one folder, deleted or not.
func (s *Store) Folder(id string) (Folder, error) {
	return scanFolder(s.db.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
}
