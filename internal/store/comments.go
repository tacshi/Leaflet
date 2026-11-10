package store

import (
	"database/sql"
	"time"

	"leaflet/internal/doc"
)

const maxCommentLen = 100000

func ClampComment(text string) string { return clampChars(text, maxCommentLen) }

type Comment struct {
	ID              string
	PageID          string
	BlockID         string
	ParentCommentID string
	AuthorID        string
	AuthorKind      string
	AuthorName      string
	Body            string
	Quote           string
	Resolved        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       time.Time
}

type Thread struct {
	Root    Comment
	Replies []Comment
}

func (t Thread) All() []Comment { return append([]Comment{t.Root}, t.Replies...) }

const commentCols = `id, page_id, block_id, parent_comment_id, author_id, author_kind, author_name, body, quote, resolved, created_at, updated_at, deleted_at`

func scanComment(sc scanner) (Comment, error) {
	var c Comment
	var parent, author, quote sql.NullString
	var cr, up int64
	var del sql.NullInt64
	err := sc.Scan(&c.ID, &c.PageID, &c.BlockID, &parent, &author, &c.AuthorKind, &c.AuthorName, &c.Body, &quote, &c.Resolved, &cr, &up, &del)
	c.ParentCommentID, c.AuthorID, c.Quote = parent.String, author.String, quote.String
	c.CreatedAt, c.UpdatedAt, c.DeletedAt = time.UnixMilli(cr), time.UnixMilli(up), fromMs(del)
	return c, err
}

func (s *Store) Comment(id string) (Comment, error) { return commentIn(s.db, id) }

func commentIn(q querier, id string) (Comment, error) {
	return scanComment(q.QueryRow(`SELECT `+commentCols+` FROM comments WHERE id = ?`, id))
}

func (s *Store) HasGuestComments(pageID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE page_id = ? AND author_kind = 'guest'`, pageID).Scan(&n)
	return n > 0, err
}

func (s *Store) GuestCommented() (map[string]bool, error) {
	return pageSet(s.db.Query(`SELECT DISTINCT page_id FROM comments WHERE author_kind = 'guest'`))
}

func (s *Store) AllComments() ([]Comment, error) {
	rows, err := s.db.Query(`SELECT ` + commentCols + ` FROM comments ORDER BY created_at`)
	return collect(rows, err, scanComment)
}

func (s *Store) Threads(pageID string) ([]Thread, error) {
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM comments WHERE page_id = ? ORDER BY created_at`, pageID)
	all, err := collect(rows, err, scanComment)
	if err != nil {
		return nil, err
	}
	var out []Thread
	for _, c := range all {
		if c.ParentCommentID != "" {
			continue
		}
		t := Thread{Root: c}
		for _, r := range all {
			if r.ParentCommentID == c.ID && r.DeletedAt.IsZero() {
				t.Replies = append(t.Replies, r)
			}
		}
		if !c.DeletedAt.IsZero() && len(t.Replies) == 0 {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) PutComment(c Comment) error { return putComment(s.db, c) }

func putComment(q querier, c Comment) error {
	if !c.DeletedAt.IsZero() {
		c.Body, c.Quote = "", ""
	}
	_, err := q.Exec(`INSERT INTO comments (`+commentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET block_id = excluded.block_id, parent_comment_id = excluded.parent_comment_id,
		author_id = excluded.author_id, author_kind = excluded.author_kind, author_name = excluded.author_name,
		body = excluded.body, quote = excluded.quote, resolved = excluded.resolved, updated_at = excluded.updated_at,
		deleted_at = excluded.deleted_at`,
		c.ID, c.PageID, c.BlockID, ns(c.ParentCommentID), ns(c.AuthorID), c.AuthorKind, c.AuthorName, c.Body, ns(c.Quote),
		c.Resolved, ms(c.CreatedAt), ms(c.UpdatedAt), nt(c.DeletedAt))
	return err
}

func (s *Store) changeComments(label string, fn func(*Comment), where string, args ...any) (int, error) {
	var all []Comment
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT `+commentCols+` FROM comments WHERE `+where, args...) // #nosec G202
		if all, err = collect(rows, err, scanComment); err != nil {
			return err
		}
		now := time.Now()
		for _, c := range all {
			old := CommentToRemote(c)
			fn(&c)
			c.UpdatedAt = now
			if !c.DeletedAt.IsZero() {
				c.Body, c.Quote = "", ""
				old["body"], old["quote"] = "", nil
			}
			if err := putComment(tx, c); err != nil {
				return err
			}
			if err := queueChange(tx, "comments", c.ID, old, CommentToRemote(c), label, c.PageID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(all) == 0 {
		return 0, err
	}
	s.Notify("outbox", all[0].PageID)
	s.Notify("comments", all[0].PageID)
	return len(all), nil
}

func (s *Store) saveComment(c Comment, label string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putComment(tx, c); err != nil {
			return err
		}
		return queue(tx, "comments", c.ID, CommentToRemote(c), label, c.PageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", c.PageID)
	s.Notify("comments", c.PageID)
	return nil
}

func (s *Store) AddComment(pageID, blockID, body, parentID, quote, authorKind, authorName string) (Comment, error) {
	sess := s.Session()
	if authorKind == "" {
		authorKind = "user"
	}
	if authorName == "" {
		authorName = sess.DisplayName
	}
	now := time.Now()
	c := Comment{ID: doc.NewRowID(), PageID: pageID, BlockID: blockID, ParentCommentID: parentID, AuthorKind: authorKind,
		AuthorName: authorName, Body: ClampComment(body), Quote: ClampComment(quote), CreatedAt: now, UpdatedAt: now}
	if authorKind == "user" && sess.SignedIn {
		c.AuthorID = sess.UserID
	}
	label := "new comment"
	if parentID != "" {
		label = "reply"
	}
	return c, s.saveComment(c, label)
}

func (s *Store) SetResolved(rootID string, resolved bool) error {
	label := "reopened"
	if resolved {
		label = "resolved thread"
	}
	n, err := s.changeComments(label, func(c *Comment) { c.Resolved = resolved }, `id = ?`, rootID)
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	return err
}

func (s *Store) DeleteComment(id string) error {
	n, err := s.changeComments("deleted comment", func(c *Comment) { c.DeletedAt = time.Now() }, `id = ?`, id)
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	return err
}
