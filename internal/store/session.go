package store

import (
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// LocalUserID is the user until someone signs in.
const LocalUserID = "local"

// Session is who is using the app and which space is theirs.
type Session struct {
	UserID      string
	DisplayName string
	Email       string
	SpaceID     string
	// SignedIn is true when signed in to the server (sync, sharing, cloud
	// models).
	SignedIn bool
}

// Session returns the current session.
func (s *Store) Session() Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

// SetSession replaces the current session.
func (s *Store) SetSession(sess Session) {
	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()
	s.Notify("session", "")
}

// User is a signed-in account, as the backend reports it.
type User struct {
	ID          string
	Email       string
	DisplayName string
}

// LoadSession sets up the session at startup, creating a space on this
// device the first time so the app works before, or without, signing in.
func (s *Store) LoadSession(user *User) (Session, error) {
	if user != nil {
		// The device keeps one account's pages: another account's go first.
		if prev := s.CachedAccount(); prev != "" && prev != user.ID {
			if err := s.ForgetAccount(); err != nil {
				return Session{}, err
			}
		}
	}
	name, hasName := s.Value("profile.name")
	if user != nil {
		spaceID, ok := s.Value("space." + user.ID)
		if !ok {
			var err error
			if spaceID, err = s.ensureLocalSpace(user.ID); err != nil {
				return Session{}, err
			}
		}
		if !hasName {
			name = user.DisplayName
			if name == "" {
				name, _, _ = strings.Cut(user.Email, "@")
			}
			if name == "" {
				name = locales.CommonYou()
			}
		}
		sess := Session{UserID: user.ID, DisplayName: name, Email: user.Email, SpaceID: spaceID, SignedIn: true}
		s.SetSession(sess)
		return sess, nil
	}
	spaceID, err := s.ensureLocalSpace(LocalUserID)
	if err != nil {
		return Session{}, err
	}
	if !hasName {
		name = locales.CommonYou()
	}
	sess := Session{UserID: LocalUserID, DisplayName: name, SpaceID: spaceID}
	s.SetSession(sess)
	return sess, nil
}

// CachedAccount is the account whose pages this device keeps, or "" when
// the pages are this device's own, made before signing in.
func (s *Store) CachedAccount() string {
	id, _ := s.Value("account")
	return id
}

// ForgetAccount removes the account's pages, and everything synced or
// waiting to sync with them, from this device. Settings and the device's
// own model connections stay. The next session starts with an empty space,
// and the device registers anew, as the server's row is the account's.
func (s *Store) ForgetAccount() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM spaces`, `DELETE FROM folders`, `DELETE FROM pages`, `DELETE FROM page_contents`,
		`DELETE FROM comments`, `DELETE FROM recordings`, `DELETE FROM files`, `DELETE FROM ai_jobs`,
		`DELETE FROM model_providers WHERE is_local = 0`, `DELETE FROM page_history`, `DELETE FROM outbox`,
		`DELETE FROM conflicts`, `DELETE FROM page_members`, `DELETE FROM peer_contents`, `DELETE FROM row_versions`,
		`DELETE FROM key_values WHERE key IN ('account', 'profile.name', 'last.page', 'device.id', 'sync.content_waiting')
			OR key LIKE 'space.%' OR key LIKE 'sync.cursor.%' OR key LIKE 'chat.%' OR key LIKE 'icloud.%' OR key LIKE 'invite.%'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Notify("*", "")
	return nil
}

func (s *Store) ensureLocalSpace(owner string) (string, error) {
	if id, ok := s.Value("space.local"); ok {
		return id, nil
	}
	id := doc.NewRowID()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO spaces (id, name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, "My space", owner, now, now); err != nil {
		return "", err
	}
	return id, s.SetValue("space.local", id)
}

// SetDisplayName changes the name shown on comments and bylines.
func (s *Store) SetDisplayName(name string) error {
	if err := s.SetValue("profile.name", name); err != nil {
		return err
	}
	sess := s.Session()
	sess.DisplayName = name
	s.SetSession(sess)
	return nil
}

// DeviceID is a stable ID for this device.
func (s *Store) DeviceID() string {
	if id, ok := s.Value("device.id"); ok {
		return id
	}
	id := doc.NewRowID()
	_ = s.SetValue("device.id", id)
	return id
}
