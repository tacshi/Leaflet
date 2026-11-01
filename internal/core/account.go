package core

import (
	"context"
	"errors"
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

// deployedKey keeps the functions' hash deployed last to a project.
func deployedKey(ref string) string { return "supabase.functions." + ref }

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
	return c.Store.PendingCount()
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
// mcp stores meanwhile. The session is then the device's own, once
// store.LoadSession(nil) loads it.
func (c *Core) Leave(clear func() error, synced bool) (unsynced int, err error) {
	c.Sync.Hold(func() {
		if synced {
			if unsynced = c.Store.PendingCount(); unsynced > 0 {
				return
			}
		}
		c.Remote.SignOut()
		c.ForgetProject()
		err = clear()
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
// opens. The sign-in is kept for UpdateProject.
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
	KeepManagement(api)
	return api, nil
}

// KeepManagement keeps the Supabase account's sign-in in the keychain, as
// it is now and as it rotates, for UpdateProject.
func KeepManagement(api *supabaseapi.Client) {
	api.OnRefresh = keepManagement
	keepManagement(api.RefreshToken())
}

func keepManagement(refresh string) { _ = keychain.Set(managementKey, []byte(refresh)) }

// Steps of ConnectProject, which its progress hears.
const (
	StepStarting = "starting"
	StepSetup    = "setup"
	StepSigning  = "signing"
)

// ConnectProject sets p up for this version of Leaflet, once it is up, and
// signs in to it as its owner. It returns the session pending, which
// Remote.Adopt makes the project and session the device's, or
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
	deployed, _ := c.Store.Value(deployedKey(p.Ref))
	hash, err := provision.Setup(ctx, api, p.Ref, deployed, func(done, all int) { progress(StepSetup, done, all) })
	if err != nil {
		return nil, err
	}
	_ = c.Store.SetValue(deployedKey(p.Ref), hash)
	publishable, secret, err := provision.Keys(ctx, api, p.Ref)
	if err != nil {
		return nil, err
	}
	progress(StepSigning, 0, 0)
	project := remote.Settings{URL: p.URL(), AnonKey: publishable, Ref: p.Ref, Name: p.Name}
	return c.signInAsOwner(ctx, api, p, project, secret)
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
func (c *Core) UpdateProject() {
	if !updating.TryLock() {
		return
	}
	defer updating.Unlock()
	ref := c.Remote.Project().Ref
	if ref == "" || !c.Remote.SignedIn() {
		return
	}
	raw, err := keychain.Get(managementKey)
	if err != nil || len(raw) == 0 {
		return
	}
	api := supabaseapi.Resume(string(raw))
	api.OnRefresh = keepManagement
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	deployed, _ := c.Store.Value(deployedKey(ref))
	hash, err := provision.Setup(ctx, api, ref, deployed, nil)
	if err != nil {
		if errors.Is(err, supabaseapi.ErrSignedOut) {
			_ = keychain.Delete(managementKey)
		}
		return
	}
	_ = c.Store.SetValue(deployedKey(ref), hash)
}
