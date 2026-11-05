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

// Signing in and out: the account whose pages this device keeps, and the
// person's own Supabase project Leaflet syncs through, which signing in
// with Supabase finds, or creates, and sets up.

// managementKey keeps the Supabase account's sign-in, so a newer Leaflet
// can bring the project up to date.
const managementKey = "supabase.management"

// updating is held while UpdateProject runs, as the iOS app starts the
// core again each time it comes to the front.
var updating sync.Mutex

// ReplacesAccount reports whether signing in as user removes the pages of
// another account this device keeps.
func (c *Core) ReplacesAccount(user *store.User) bool {
	prev := c.Store.CachedAccount()
	return prev != "" && prev != user.ID
}

// LostBySigningIn counts the changes signing in as user removes: those of
// another account this device keeps the pages of, which never went up.
func (c *Core) LostBySigningIn(user *store.User) int {
	if !c.ReplacesAccount(user) {
		return 0
	}
	return c.unsynced()
}

// stopRuns stops the agents' runs before the account's pages go: one that
// ended after would store them again.
func (c *Core) stopRuns() {
	if c.Hub != nil {
		c.Hub.Forget()
	}
}

// unsynced counts the changes here not uploaded yet: the outbox's, and the
// recordings whose audio is still being recorded, saved or transcribed,
// which the outbox gets only once they finish. Removing the account's
// pages would remove that audio too.
func (c *Core) unsynced() int {
	n := c.Store.PendingCount()
	if c.Rec != nil {
		n += c.Rec.Saving()
	}
	return n
}

// UseAccount makes user the session, once signed in: the previous
// account's pages go first, if it was another's, with their changes not
// uploaded yet only when drop says the user agreed. Pages sync one way at
// a time, the server's now, so iCloud sync turns off, in the settings too,
// which then show it off once signed out again. It fails with
// store.ErrUnsynced when a change was stored since the count, by leaflet
// mcp, to ask again.
func (c *Core) UseAccount(user *store.User, drop bool) error {
	// The previous account's agents stop, and their chats go, as its pages
	// do: with drop here, else as the session loads (store.LoadSession),
	// which leaves them while a change of theirs waits to go up.
	replaces := c.ReplacesAccount(user)
	if replaces && (drop || c.Store.PendingCount() == 0) {
		c.stopRuns()
	}
	if drop && replaces {
		if err := c.Store.ForgetAccount(); err != nil {
			return err
		}
	}
	if s := c.settings(); c.Sync.ICloudOn() || s.ICloudSync {
		c.Sync.SetICloud(false)
		c.watch(false)
		s.ICloudSync = false
		c.saveSettings(s)
	}
	_, err := c.Store.LoadSession(user)
	// Again once the chats are gone from the store, as Leave does.
	if replaces && err == nil {
		c.stopRuns()
	}
	return err
}

// Leave signs out and clears this device with clear, as
// store.ForgetSyncedAccount, ForgetAccount or Wipe, between sync passes.
// With synced, it does neither while a change waits to go up, and returns
// how many do; clear checks again as it removes, for a change that leaflet
// mcp stores meanwhile, and so clears before signing out. The session is
// then the device's own, once store.LoadSession(nil) loads it.
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
		// Again once the chats are gone from the store: one the app read
		// back from it in between, as the workspace agent's it draws,
		// would stay with the next account.
		c.stopRuns()
		c.revokeDevice(device)
		c.Remote.SignOut()
		c.ForgetProject()
	})
	return unsynced, err
}

// revokeDevice revokes the row of this device, as it leaves the account
// under an ID it forgets: kept, each sign-in here would count as one more
// of the devices an account may have. It is best tried: signing out goes on
// without the server, and a device not registered yet has no row.
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

// ForgetProject forgets the project chosen and the Supabase account's
// sign-in, on signing out: signing in again finds the project anew.
func (c *Core) ForgetProject() {
	if c.Remote.Fixed() {
		return
	}
	_ = keychain.Delete(managementKey)
	c.mgmtMu.Lock()
	c.mgmt = nil
	c.mgmtMu.Unlock()
	_ = c.Remote.Use(remote.Settings{})
}

// SignInWithSupabase signs in to the person's Supabase account through
// the browser: opened hears Supabase's sign-in page, which OpenURL then
// opens. AdoptProject keeps the sign-in for UpdateProject.
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

// keepManagementOf keeps the Supabase account's sign-in in the keychain,
// as it is now and as it rotates, for UpdateProject.
func keepManagementOf(api *supabaseapi.Client) {
	api.OnRefresh = keepManagement
	keepManagement(api.RefreshToken())
}

func keepManagement(refresh string) {
	if err := keychain.Set(managementKey, []byte(refresh)); err != nil {
		log.Printf("keep the Supabase sign-in: %v", err)
	}
}

// Steps of ConnectProject, which its progress hears.
const (
	StepStarting = "starting"
	StepSetup    = "setup"
	StepSigning  = "signing"
)

// ConnectProject sets p up for this version of Leaflet, once it is up, and
// signs in to it as its owner. It returns the session pending, which
// AdoptProject makes the project and session the device's, or
// Remote.Discard drops. progress hears each step as it starts, and how
// many of the setup's steps are done.
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
	// Sign-ups it could not close yet, or a Realtime not ready, leave the
	// project set up but for them: the sign-in goes on, and the next start
	// does the rest.
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

// AdoptProject makes pending, as ConnectProject returned it, the device's
// project and session, and keeps api, the Supabase account's sign-in, for
// UpdateProject. Only then: a sign-in that fails or is cancelled before
// leaves the project in use with the sign-in kept for it.
func (c *Core) AdoptProject(pending *remote.Pending, api *supabaseapi.Client) (*store.User, error) {
	user, err := c.Remote.Adopt(pending)
	if err != nil {
		return nil, err
	}
	keepManagementOf(api)
	c.mgmtMu.Lock()
	c.mgmt = api
	c.mgmtMu.Unlock()
	return user, nil
}

// signInAsOwner signs in to p as the person signed in to Supabase, who
// owns it: the one member of its organization. A project whose owner is
// someone else, as one in an organization they were added to, is refused:
// being able to manage it does not make them its owner in Leaflet.
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

// projectUpdatedKey keeps, by a project's ref, the Signature of the build
// that last brought it up to date from this device.
func projectUpdatedKey(ref string) string { return "project.updated." + ref }

// projectUpdated keeps that this build brought the project of ref up to
// date.
func (c *Core) projectUpdated(ref string) {
	sig, err := provision.Signature()
	if err == nil {
		err = c.Store.SetValue(projectUpdatedKey(ref), sig)
	}
	if err != nil {
		log.Printf("update project %s: %v", ref, err)
	}
}

// projectOutOfDate reports whether the project in use may lack what this
// version of Leaflet brings it: signed in to it, with the Supabase
// account's sign-in kept, and not brought up to date by this build yet.
// Each Setup renews that sign-in, whose refresh token rotates, and makes
// a few calls more: they wait for a build that has something new.
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

// UpdateProject brings the project up to this version of Leaflet, when a
// newer one adds migrations or changes the functions, with the Supabase
// account's sign-in kept from signing in. One already running does it
// for this one too: two at once would renew the same sign-in, and the
// one that loses forgets it.
func (c *Core) UpdateProject() { _ = c.updateProject() }

// updateProject is UpdateProject, and reports whether it is worth trying
// again: it failed, but for the Supabase sign-in gone.
func (c *Core) updateProject() (again bool) {
	if !updating.TryLock() {
		return false
	}
	defer updating.Unlock()
	ref := c.Remote.Project().Ref
	if ref == "" || !c.Remote.SignedIn() {
		return false
	}
	raw, err := keychain.Get(managementKey)
	if errors.Is(err, keychain.ErrNotFound) || err == nil && len(raw) == 0 {
		// Signed in without the Supabase account, as to a server of one's
		// own: there is nothing to bring up to date with.
		return false
	}
	if err != nil {
		log.Printf("update project %s: the Supabase sign-in: %v", ref, err)
		return false
	}
	// The sign-in this run used last, with its access token, while it is
	// still the one kept: a refresh token renewed for it is kept as it
	// rotates (keepManagement), so the next try does not renew it again.
	c.mgmtMu.Lock()
	api := c.mgmt
	if api == nil || api.RefreshToken() != string(raw) {
		api = supabaseapi.Resume(string(raw))
		api.OnRefresh = keepManagement
		c.mgmt = api
	}
	c.mgmtMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := provision.Setup(ctx, api, ref, nil); err != nil {
		log.Printf("update project %s: %v", ref, err)
		if errors.Is(err, supabaseapi.ErrSignedOut) {
			c.mgmtMu.Lock()
			if c.mgmt == api {
				c.mgmt = nil
			}
			c.mgmtMu.Unlock()
			if err := keychain.Delete(managementKey); err != nil {
				log.Printf("update project %s: forget the Supabase sign-in: %v", ref, err)
			}
			return false
		}
		return true
	}
	c.projectUpdated(ref)
	return false
}

// projectRetries are the waits before trying again to bring the project up
// to date, after the first try failed, as offline as the app started;
// tests shorten them.
var projectRetries = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// keepProjectUpdated brings the project up to date as the app starts, and
// tries again a few times later if that failed. With hold, the changes
// wait to go up for the first try alone: a project that can't be reached
// then is most likely up to date already, and holding them back longer
// would leave them on this device. Start runs it as a run of the app first
// starts, and again as it starts later while the project is still not up
// to date and no run of tries is under way.
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
