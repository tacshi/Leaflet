package core

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

const managementKey = "supabase.management"

var updating sync.Mutex

func (c *Core) ReplacesAccount(user *store.User) bool {
	prev := c.Store.CachedAccount()
	return prev != "" && prev != user.ID
}

func (c *Core) LostBySigningIn(user *store.User) int {
	if !c.ReplacesAccount(user) {
		return 0
	}
	return c.unsynced()
}

func (c *Core) stopRuns() {
	if c.Hub != nil {
		c.Hub.Forget()
	}
}

func (c *Core) unsynced() int {
	n := c.Store.PendingCount()
	if c.Rec != nil {
		n += c.Rec.Saving()
	}
	return n
}

// UseAccount makes user's the account this device syncs, as after a sign-in. It runs between sync passes: one under
// way is cut short and waited for, so that it writes nothing of the account before into the store once that is
// cleared. The app may call it on its main thread, as the cut pass asks nothing more of it, or off it, as the wait
// can be long, so it leaves the app's settings, which are its main thread's, alone: the iCloud sync a sign-in ends
// is turned off here, and the app keeps that in its settings, and stops watching the iCloud folder, with KeepICloud
// on its main thread once UseAccount returns.
func (c *Core) UseAccount(user *store.User, drop bool) (err error) {
	c.Sync.Interrupt(func() { err = c.useAccount(user, drop) })
	return err
}

func (c *Core) useAccount(user *store.User, drop bool) error {
	replaces := c.ReplacesAccount(user)
	if replaces && (drop || c.Store.PendingCount() == 0) {
		c.stopRuns()
	}
	if drop && replaces {
		if err := c.Store.ForgetAccount(); err != nil {
			return err
		}
		c.Sync.Forget()
	}
	c.Sync.SetICloud(false)
	_, err := c.Store.LoadSession(user)
	if err == nil {
		c.Sync.Forget()
	}
	if replaces && err == nil {
		c.stopRuns()
	}
	return err
}

func (c *Core) Leave(clear func() error, synced bool) (unsynced int, err error) {
	c.Sync.Hold(func() {
		if synced {
			if unsynced = c.unsynced(); unsynced > 0 {
				return
			}
		}
		c.stopRuns()
		device, _ := c.Store.Value("device.id")
		if err = clear(); errors.Is(err, store.ErrUnsynced) {
			unsynced, err = max(c.unsynced(), 1), nil
			return
		}
		c.stopRuns()
		c.revokeDevice(device)
		c.Remote.SignOut()
		c.ForgetProject()
		c.Sync.Forget()
	})
	return unsynced, err
}

func (c *Core) revokeDevice(id string) {
	if id == "" || !c.Remote.SignedIn() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Remote.RPC(ctx, "sign_out_device", map[string]any{"p_device_id": id}); err != nil {
		log.Printf("sign out: this device's row: %v", err)
	}
}

func (c *Core) ForgetProject() {
	if c.Remote.Fixed() {
		return
	}
	c.mgmtMu.Lock()
	c.mgmt = nil
	_ = keychain.Delete(managementKey)
	c.projectSignIn(c.Remote.Project().Ref, false)
	c.mgmtMu.Unlock()
	_ = c.Remote.Use(remote.Settings{})
}

func (c *Core) SignInWithSupabase(ctx context.Context, opened func(url string)) (*supabaseapi.Client, error) {
	api, err := supabaseapi.SignIn(ctx, func(u string) {
		if opened != nil {
			opened(u)
		}
		if c.OpenURL != nil {
			c.OpenURL(u)
		}
	})
	if err != nil {
		return nil, err
	}
	return api, nil
}

// useManagement makes api the Supabase Management API sign-in that keeps the project up to date, c.mgmt. mgmtMu is
// held: the sign-in, its refresh token kept in the keychain for the runs to come and the mark of a project whose
// sign-in ended change together, under it. A sign-out forgets the sign-in, and another sign-in takes its place, while
// what began with the one before can still run, as an update of the project or the sign-in in Settings, which take
// minutes: that brings back no sign-in forgotten, and ends none but its own. So the tokens api renews are kept only
// for as long as it is the sign-in, and a renewal that lands once it is not keeps nothing.
func (c *Core) useManagement(api *supabaseapi.Client) {
	c.mgmt = api
	api.OnRefresh = func(refresh string) {
		c.mgmtMu.Lock()
		defer c.mgmtMu.Unlock()
		if c.mgmt == api {
			keepManagement(refresh)
		}
	}
}

// adoptManagement makes api, just signed in with, the sign-in that keeps project ref up to date, kept in the keychain,
// and the project's sign-in no longer ended. mgmtMu is held.
func (c *Core) adoptManagement(ref string, api *supabaseapi.Client) {
	c.useManagement(api)
	keepManagement(api.RefreshToken())
	c.projectSignIn(ref, false)
}

func keepManagement(refresh string) {
	if err := keychain.Set(managementKey, []byte(refresh)); err != nil {
		log.Printf("keep the Supabase sign-in: %v", err)
	}
}

const (
	StepStarting = "starting"
	StepSetup    = "setup"
	StepSigning  = "signing"
)

func (c *Core) ConnectProject(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project, progress func(step string, done, all int)) (*remote.Pending, error) {
	if progress == nil {
		progress = func(string, int, int) {}
	}
	if !p.Healthy() {
		progress(StepStarting, 0, 0)
		var err error
		if p, err = provision.Ready(ctx, api, p); err != nil {
			return nil, err
		}
	}
	progress(StepSetup, 0, 0)
	switch err := provision.Setup(ctx, api, p.Ref, func(done, all int) { progress(StepSetup, done, all) }); {
	case err == nil:
		c.projectUpdated(p.Ref)
	case errors.Is(err, provision.ErrSignUpsOpen), errors.Is(err, provision.ErrRealtimeWaits):
		log.Printf("set up project %s: %v", p.Ref, err)
	default:
		return nil, err
	}
	publishable, secret, err := provision.Keys(ctx, api, p.Ref)
	if err != nil {
		return nil, err
	}
	progress(StepSigning, 0, 0)
	project := remote.Settings{URL: p.URL(), PublishableKey: publishable, Ref: p.Ref, Name: p.Name}
	return c.signInAsOwner(ctx, api, p, project, secret)
}

func (c *Core) AdoptProject(pending *remote.Pending, api *supabaseapi.Client) (*store.User, error) {
	user, err := c.Remote.Adopt(pending)
	if err != nil {
		return nil, err
	}
	c.mgmtMu.Lock()
	c.adoptManagement(c.Remote.Project().Ref, api)
	c.mgmtMu.Unlock()
	return user, nil
}

func (c *Core) signInAsOwner(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project, project remote.Settings, secret string) (*remote.Pending, error) {
	email, name, err := provision.Owner(ctx, api, p.OrganizationSlug)
	if err != nil {
		return nil, err
	}
	owner, err := c.Remote.ProjectOwner(ctx, project, secret)
	if err != nil {
		return nil, err
	}
	if owner != "" && !strings.EqualFold(owner, email) {
		return nil, errors.New(locales.SigninNotYourProject(p.Name, owner))
	}
	return c.Remote.SignInAsOwner(ctx, project, secret, email, name)
}

func projectUpdatedKey(ref string) string { return "project.updated." + ref }

func (c *Core) projectUpdated(ref string) {
	sig, err := provision.Signature()
	if err == nil {
		err = c.Store.SetValue(projectUpdatedKey(ref), sig)
	}
	if err != nil {
		log.Printf("update project %s: %v", ref, err)
	}
	c.projectSignIn(ref, false)
}

func projectSignInKey(ref string) string { return "project.signin_ended." + ref }

func (c *Core) projectSignIn(ref string, ended bool) {
	if ref == "" {
		return
	}
	var err error
	if ended {
		err = c.Store.SetValue(projectSignInKey(ref), "1")
	} else if _, was := c.Store.Value(projectSignInKey(ref)); was {
		err = c.Store.DeleteValue(projectSignInKey(ref))
	}
	if err != nil {
		log.Printf("update project %s: %v", ref, err)
	}
	c.signInEnded.Store(0)
}

func (c *Core) ProjectNeedsSignIn() bool {
	ref := c.Remote.Project().Ref
	if ref == "" || !c.Remote.SignedIn() {
		return false
	}
	switch c.signInEnded.Load() {
	case 1:
		return false
	case 2:
		return true
	}
	_, ended := c.Store.Value(projectSignInKey(ref))
	if ended {
		c.signInEnded.CompareAndSwap(0, 2)
	} else {
		c.signInEnded.CompareAndSwap(0, 1)
	}
	return ended
}

func (c *Core) RenewProject(ctx context.Context, api *supabaseapi.Client) error {
	ref, session := c.Remote.Project().Ref, c.Remote.Sessions()
	if ref == "" || !c.Remote.SignedIn() {
		return remote.ErrSignedOut
	}
	updating.Lock()
	defer updating.Unlock()
	switch err := provision.Setup(ctx, api, ref, nil); {
	case err == nil:
		c.projectUpdated(ref)
	case errors.Is(err, provision.ErrSignUpsOpen), errors.Is(err, provision.ErrRealtimeWaits):
		log.Printf("update project %s: %v", ref, err)
	default:
		return err
	}
	c.mgmtMu.Lock()
	defer c.mgmtMu.Unlock()
	// The setup takes minutes: a sign-out meanwhile forgot the project's sign-in, which this one must not bring back,
	// and an account signed in since keeps a sign-in of its own.
	if !c.Remote.SignedIn() || c.Remote.Sessions() != session {
		return remote.ErrSignedOut
	}
	c.adoptManagement(ref, api)
	return nil
}

func (c *Core) projectOutOfDate() bool {
	ref := c.Remote.Project().Ref
	if ref == "" || !c.Remote.SignedIn() {
		return false
	}
	sig, err := provision.Signature()
	if err != nil {
		return true
	}
	done, _ := c.Store.Value(projectUpdatedKey(ref))
	return done != sig
}

func (c *Core) UpdateProject() { _ = c.updateProject() }

func (c *Core) updateProject() (again bool) {
	if !updating.TryLock() {
		return false
	}
	defer updating.Unlock()
	ref, session := c.Remote.Project().Ref, c.Remote.Sessions()
	if ref == "" || !c.Remote.SignedIn() {
		return false
	}
	raw, err := keychain.Get(managementKey)
	if errors.Is(err, keychain.ErrNotFound) || err == nil && len(raw) == 0 {
		return false
	}
	if err != nil {
		log.Printf("update project %s: the Supabase sign-in: %v", ref, err)
		return false
	}
	c.mgmtMu.Lock()
	// A sign-out since the sign-in was read forgot it, and an account signed in since keeps its own.
	if !c.Remote.SignedIn() || c.Remote.Sessions() != session {
		c.mgmtMu.Unlock()
		return false
	}
	api := c.mgmt
	if api == nil || api.RefreshToken() != string(raw) {
		api = supabaseapi.Resume(string(raw))
		c.useManagement(api)
	}
	c.mgmtMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := provision.Setup(ctx, api, ref, nil); err != nil {
		log.Printf("update project %s: %v", ref, err)
		if errors.Is(err, supabaseapi.ErrSignedOut) {
			c.mgmtMu.Lock()
			defer c.mgmtMu.Unlock()
			// Only the sign-in this update ran with ended: one kept in its place meanwhile is not forgotten for it.
			if c.mgmt == api {
				c.mgmt = nil
				if err := keychain.Delete(managementKey); err != nil {
					log.Printf("update project %s: forget the Supabase sign-in: %v", ref, err)
				}
				c.projectSignIn(ref, true)
			}
			return false
		}
		return true
	}
	c.projectUpdated(ref)
	return false
}

var projectRetries = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

func (c *Core) keepProjectUpdated(hold bool) {
	again := c.updateProject()
	if hold {
		c.updatingProject.Store(false)
		c.Sync.Kick()
	}
	for _, wait := range projectRetries {
		if !again {
			return
		}
		time.Sleep(wait)
		again = c.updateProject()
	}
}
