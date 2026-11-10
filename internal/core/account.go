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

func (c *Core) UseAccount(user *store.User, drop bool) error {
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
	_ = keychain.Delete(managementKey)
	c.mgmtMu.Lock()
	c.mgmt = nil
	c.mgmtMu.Unlock()
	c.projectSignIn(c.Remote.Project().Ref, false)
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

func keepManagementOf(api *supabaseapi.Client) {
	api.OnRefresh = keepManagement
	keepManagement(api.RefreshToken())
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
	keepManagementOf(api)
	c.mgmtMu.Lock()
	c.mgmt = api
	c.mgmtMu.Unlock()
	c.signInEnded.Store(0)
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
		c.signInEnded.Store(2)
	} else {
		c.signInEnded.Store(1)
	}
	return ended
}

func (c *Core) RenewProject(ctx context.Context, api *supabaseapi.Client) error {
	ref := c.Remote.Project().Ref
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
	keepManagementOf(api)
	c.mgmtMu.Lock()
	c.mgmt = api
	c.mgmtMu.Unlock()
	c.projectSignIn(ref, false)
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
	ref := c.Remote.Project().Ref
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
			c.projectSignIn(ref, true)
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
