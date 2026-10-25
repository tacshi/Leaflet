package store

import (
	"strings"
	"time"

	"leaflet/internal/doc"
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

// Initial is the letter on the user's avatar.
func (s Session) Initial() string {
	n := strings.TrimSpace(s.DisplayName)
	if n == "" {
		return "?"
	}
	return strings.ToUpper(string([]rune(n)[:1]))
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
				name = "You"
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
		name = "You"
	}
	sess := Session{UserID: LocalUserID, DisplayName: name, SpaceID: spaceID}
	s.SetSession(sess)
	return sess, nil
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
