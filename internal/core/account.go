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
	if drop && c.ReplacesAccount(user) {
		c.stopRuns()
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
		if err = clear(); errors.Is(err, store.ErrUnsynced) {
			unsynced, err = max(c.unsynced(), 1), nil
			return
		}
		c.Remote.SignOut()
		c.ForgetProject()
	})
	return unsynced, err
}

// ForgetProject forgets the project chosen and the Supabase account's
// sign-in, on signing out: signing in again finds the project anew.
func (c *Core) ForgetProject() {
	if c.Remote.Fixed() {
		return
	}
	_ = keychain.Delete(managementKey)
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
	if err := provision.Setup(ctx, api, p.Ref, func(done, all int) { progress(StepSetup, done, all) }); err != nil {
		return nil, err
	}
	publishable, secret, err := provision.Keys(ctx, api, p.Ref)
	if err != nil {
		return nil, err
	}
	progress(StepSigning, 0, 0)
	project := remote.Settings{URL: p.URL(), AnonKey: publishable, Ref: p.Ref, Name: p.Name}
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
	api := supabaseapi.Resume(string(raw))
	api.OnRefresh = keepManagement
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := provision.Setup(ctx, api, ref, nil); err != nil {
		log.Printf("update project %s: %v", ref, err)
		if errors.Is(err, supabaseapi.ErrSignedOut) {
			if err := keychain.Delete(managementKey); err != nil {
				log.Printf("update project %s: forget the Supabase sign-in: %v", ref, err)
			}
			return false
		}
		return true
	}
	return false
}

// projectRetries are the waits before trying again to bring the project up
// to date, after the first try failed, as offline as the app started;
// tests shorten them.
var projectRetries = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// keepProjectUpdated brings the project up to date as the app starts, and
// tries again a few times later if that failed. The changes wait to go up
// for the first try alone: a project that can't be reached then is most
// likely up to date already, and holding them back longer would leave
// them on this device.
func (c *Core) keepProjectUpdated() {
	again := c.updateProject()
	c.updatingProject.Store(false)
	c.Sync.Kick()
	// One run of tries at a time, as iOS starts the core again each time
	// the app comes back to the screen.
	if !again || !c.retryingUpdate.CompareAndSwap(false, true) {
		return
	}
	defer c.retryingUpdate.Store(false)
	for _, wait := range projectRetries {
		if !again {
			return
		}
		time.Sleep(wait)
		again = c.updateProject()
	}
}
