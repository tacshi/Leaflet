package sync

import (
	"context"
	"errors"
	"runtime"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// supabaseTarget syncs through the Supabase server the user signed in to.
type supabaseTarget struct{ s *Service }

func (t supabaseTarget) ready() bool                    { return t.s.be.SignedIn() }
func (t supabaseTarget) needsNetwork() bool             { return true }
func (t supabaseTarget) pass(ctx context.Context) error { return t.s.pass(ctx) }
func (t supabaseTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.refreshRemote(ctx, pageID)
}
func (t supabaseTarget) flush(ctx context.Context, pageID string) error {
	return t.s.flushRemote(ctx, pageID)
}

// pass is a pass through the server: this device's pages join the
// account's space, once, and the device says it is still in use; then the
// outbox goes up, unless the project is being updated, and what changed
// comes down.
func (s *Service) pass(ctx context.Context) error {
	if err := s.resolveSpace(ctx); err != nil {
		return err
	}
	if err := s.heartbeat(ctx); err != nil {
		return err
	}
	if s.Updating == nil || !s.Updating() {
		if err := s.push(ctx); err != nil {
			return err
		}
	}
	return s.pull(ctx)
}

// resolveSpace, after sign-in, moves the pages made on this device into
// the account's space and queues them for upload. Pages are only ever
// adopted from the device's own space: another account's are forgotten
// before signing in (store.LoadSession).
func (s *Service) resolveSpace(ctx context.Context) error {
	uid := s.be.UserID()
	if acct := s.st.CachedAccount(); acct != "" && acct != uid {
		return errors.New("this device still keeps another account's pages")
	}
	if _, ok := s.st.Value("space." + uid); ok {
		return nil
	}
	own := remote.Query{Filters: []string{remote.Eq("owner_id", uid)}}
	space, err := s.be.SelectOne(ctx, "spaces", own)
	if err != nil {
		return err
	}
	if space == nil {
		rows, err := s.be.Insert(ctx, "spaces", map[string]any{"name": "My space", "owner_id": uid})
		switch {
		case remote.IsConflict(err):
			// Another device of the user's made it meanwhile.
			if space, err = s.be.SelectOne(ctx, "spaces", own); err == nil && space == nil {
				err = errors.New("the server has no space")
			}
		case err == nil && len(rows) == 0:
			err = errors.New("the server made no space")
		case err == nil:
			space = rows[0]
		}
		if err != nil {
			return err
		}
	}
	remoteID := store.S(space, "id")
	joined, err := s.joinsLibrary(ctx, remoteID)
	if err != nil {
		return err
	}
	name := store.S(space, "name")
	if name == "" {
		name = "My space"
	}
	if err := s.moveInto(remoteID, name, uid, joined); err != nil {
		return err
	}
	if err := s.st.SetValue("account", uid); err != nil {
		return err
	}
	sess := s.st.Session()
	sess.UserID, sess.SignedIn = uid, true
	s.st.SetSession(sess)
	// Everything made before sign-in goes up now, so nothing is left out of
	// the queue that signing out checks.
	return s.adopted(ctx, remoteID, "space."+uid)
}

// joinsLibrary reports whether the space's library is another device's,
// which has starter pages of its own, so this device's stay out of it,
// unless the user took them up. The server makes an account's space as it
// signs up, so having one says nothing: a library is another device's once
// it has pages, or once another device claimed it for its starter pages,
// as one signing in at the same time does before either has uploaded
// them. A device with no starter pages left claims nothing.
func (s *Service) joinsLibrary(ctx context.Context, spaceID string) (bool, error) {
	started, err := s.be.SelectOne(ctx, "pages", remote.Query{Select: "id", Filters: []string{remote.Eq("space_id", spaceID)}})
	if err != nil || started != nil || !s.st.StarterPagesUnsettled() {
		return started != nil, err
	}
	won, err := s.be.RPC(ctx, "claim_starter_pages", map[string]any{"p_space_id": spaceID, "p_device": s.st.DeviceID()})
	if err != nil {
		return false, err
	}
	return won != true, nil
}

func (s *Service) heartbeat(ctx context.Context) error {
	id := s.st.DeviceID()
	row, err := s.be.SelectOne(ctx, "devices", remote.Query{Select: "revoked_at", Filters: []string{remote.Eq("id", id)}})
	if err != nil {
		return err
	}
	if row != nil && row["revoked_at"] != nil {
		// The row stays revoked: signing in again, this device registers
		// as a new one. What iCloud sync kept goes with the old ID, as its
		// log is that ID's: under the new one, a later turn-on joins the
		// folder afresh rather than going on from the old one's last batch,
		// which the other devices would wait before for good. Its space
		// stays, for what went up to the folder to be fetched from it.
		space, _ := s.st.Value("icloud.space")
		if space == "" {
			space, _ = s.st.Value(fetchSpaceKey)
		}
		_ = s.st.ForgetICloud()
		if space != "" {
			_ = s.st.SetValue(fetchSpaceKey, space)
		}
		_ = s.st.DeleteValue("device.id")
		s.be.SignOut()
		return remote.ErrSignedOut
	}
	_, err = s.be.Upsert(ctx, "devices", map[string]any{
		"id": id, "user_id": s.be.UserID(), "name": s.deviceName(), "platform": runtime.GOOS,
		"last_active_at": time.Now().UTC().Format(time.RFC3339Nano),
	}, "")
	return err
}
