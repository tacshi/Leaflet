package sync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

type supabaseTarget struct{ s *Service }

func (t supabaseTarget) ready() bool        { return t.s.be.SignedIn() }
func (t supabaseTarget) needsNetwork() bool { return true }
func (t supabaseTarget) pass(ctx context.Context, pull bool) error {
	return t.s.pass(ctx, pull)
}
func (t supabaseTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.refreshRemote(ctx, pageID)
}

func (s *Service) pass(ctx context.Context, pull bool) error {
	if err := s.resolveSpace(ctx); err != nil {
		return err
	}
	if err := s.heartbeat(ctx); err != nil {
		return err
	}
	var unpushed error
	if s.Updating == nil || !s.Updating() {
		pushing, cancel := context.WithTimeout(ctx, pushFor)
		err := s.push(pushing)
		more := ranOut(pushing, err) && ctx.Err() == nil
		cancel()
		switch {
		case more:
			unpushed = fmt.Errorf("%w: %w", errPushRanOut, err)
			s.KickPush()
		case err != nil:
			return err
		}
	}
	if !pull {
		return unpushed
	}
	return cmp.Or(s.pull(ctx), unpushed)
}

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
	return s.adopted(ctx, remoteID, "space."+uid)
}

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

const heartbeatEvery = 10 * time.Minute

type every struct {
	at   time.Time
	whom string
}

func (r *every) due(whom string, d time.Duration) bool {
	return r.at.IsZero() || r.whom != whom || time.Since(r.at) >= d
}

func (s *Service) heartbeat(ctx context.Context) error {
	uid, id := s.be.UserID(), s.st.DeviceID()
	whom := fmt.Sprintf("%s %s %d", uid, id, s.be.Sessions())
	s.mu.Lock()
	due := s.beat.due(whom, heartbeatEvery)
	s.mu.Unlock()
	if !due {
		return nil
	}
	rows, err := s.be.UpsertReturning(ctx, "devices", map[string]any{
		"id": id, "user_id": uid, "name": s.deviceName(), "platform": runtime.GOOS,
		"last_active_at": time.Now().UTC().Format(time.RFC3339Nano),
	}, "", "revoked_at")
	if remote.IsInvalid(err) {
		log.Printf("device %s: %v", id, err)
		rows, err = nil, nil
	}
	if err != nil {
		return err
	}
	if len(rows) > 0 && rows[0]["revoked_at"] != nil {
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
	s.mu.Lock()
	s.beat = every{time.Now(), whom}
	s.mu.Unlock()
	return nil
}
