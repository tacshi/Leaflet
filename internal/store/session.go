package store

import (
	"database/sql"
	"errors"
	"log"
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
// The device keeps one account's pages: another account's go first, once
// their changes have all gone up, and ErrUnsynced says one waits. Signing
// in as another account forgets them itself once the user agrees to.
func (s *Store) LoadSession(user *User) (Session, error) {
	if user != nil {
		prev, _, err := s.Lookup("account")
		if err != nil {
			return Session{}, err
		}
		if prev != "" && prev != user.ID {
			if err := s.ForgetSyncedAccount(); err != nil {
				return Session{}, err
			}
		}
	}
	name, hasName := s.Value("profile.name")
	if user != nil {
		spaceID, ok, err := s.Lookup("space." + user.ID)
		if err != nil {
			return Session{}, err
		}
		if !ok {
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

// ErrNoSpace is a change that goes into a space no longer on this device:
// the app has signed in, out or to another account meanwhile.
var ErrNoSpace = errors.New("the space is no longer on this device: Leaflet changed accounts, try again")

// ReloadSession reads the session again from what the app last stored, for
// leaflet mcp, which shares the database with the app and may outlive the
// account it started with. It makes nothing, and fails while the app is
// between accounts and has no space yet.
func (s *Store) ReloadSession() (Session, error) {
	acct, _, err := s.Lookup("account")
	if err != nil {
		return Session{}, err
	}
	spaceID, ok := "", false
	if acct != "" {
		if spaceID, ok, err = s.Lookup("space." + acct); err != nil {
			return Session{}, err
		}
	}
	if !ok {
		if spaceID, ok, err = s.Lookup("space.local"); err != nil {
			return Session{}, err
		} else if !ok {
			return Session{}, ErrNoSpace
		}
	}
	sess := s.Session()
	sess.UserID, sess.SpaceID, sess.SignedIn = LocalUserID, spaceID, acct != ""
	if acct != "" {
		sess.UserID = acct
	}
	if name, ok := s.Value("profile.name"); ok {
		sess.DisplayName = name
	}
	if sess != s.Session() {
		s.SetSession(sess)
	}
	return sess, nil
}

// CachedAccount is the account whose pages this device keeps, or "" when
// the pages are this device's own, made before signing in.
func (s *Store) CachedAccount() string {
	id, _ := s.Value("account")
	return id
}

// hasSpace reports whether a space is on this device.
func hasSpace(q querier, id string) bool {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM spaces WHERE id = ?`, id).Scan(&n)
	return n > 0
}

// ensureLocalSpace is the space of this device's pages, made the first
// time (valueOnce): one made by the app and another by leaflet mcp at
// once would leave the pages of one out of the other's.
func (s *Store) ensureLocalSpace(owner string) (string, error) {
	return s.valueOnce("space.local", func(tx *sql.Tx) (string, error) {
		id := doc.NewRowID()
		now := time.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO spaces (id, name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, "My space", owner, now, now)
		return id, err
	})
}

// PutSpace stores a space row.
func (s *Store) PutSpace(id, name, owner string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO spaces (id, name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, owner_id = excluded.owner_id, updated_at = excluded.updated_at`,
		id, name, owner, now, now)
	return err
}

// MoveSpace moves everything of one space into another, in one
// transaction, so nothing made meanwhile is left in the space it deletes.
func (s *Store) MoveSpace(from, to string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE pages SET space_id = ? WHERE space_id = ?`,
			`UPDATE folders SET space_id = ? WHERE space_id = ?`,
			`UPDATE files SET space_id = ? WHERE space_id = ?`,
		} {
			if _, err := tx.Exec(q, to, from); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`DELETE FROM spaces WHERE id = ?`, from)
		return err
	})
	if err != nil {
		return err
	}
	s.Notify("*", "")
	return nil
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

// DeviceID is a stable ID for this device, made the first time
// (valueOnce). It is "" while the database cannot be read, never an ID
// made anew, under which this device would sync as another: a folder of
// the Leaflet folder in iCloud Drive refuses "" (icloud.DevicePath).
func (s *Store) DeviceID() string {
	id, err := s.valueOnce("device.id", func(*sql.Tx) (string, error) { return doc.NewRowID(), nil })
	if err != nil {
		log.Printf("store: device ID: %v", err)
	}
	return id
}
