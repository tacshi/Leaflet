package mobile

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"leaflet/internal/core"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

// Signing in with Supabase, on the phone: to the Leaflet project the
// desktop app set up in the person's Supabase account, which the phone
// finds and never creates. The app runs Supabase's sign-in page in
// ASWebAuthenticationSession between BeginSupabaseSignIn and
// FinishSupabaseSignIn, which then sets the session up as the desktop's
// sign-in does.

// supabaseSignIn is a sign-in begun, as BeginSupabaseSignIn returns it:
// the page to open, and what FinishSupabaseSignIn checks the answer
// against.
type supabaseSignIn struct {
	URL      string `json:"url"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
}

// BeginSupabaseSignIn begins signing in with Supabase, returning a
// supabaseSignIn as JSON. The redirect is the Worker's /auth/supabase,
// which the app's web session takes as its https callback.
func BeginSupabaseSignIn() (string, error) {
	u, state, verifier := supabaseapi.BeginSignIn()
	return encode(supabaseSignIn{URL: u, State: state, Verifier: verifier})
}

// signInTimeout bounds the sign-in after the browser: finding the
// project, bringing it up to this version and signing in to it.
const signInTimeout = 10 * time.Minute

// errNotSaved stops signing in or out while an open page's edits could
// not be saved; the save said why.
var errNotSaved = errors.New("Leaflet couldn't save the page open here.") //nolint:staticcheck // shown to people as a sentence.

// signedIn is how a sign-in ended, as FinishSupabaseSignIn returns it.
type signedIn struct {
	Email string `json:"email"`
	// Lost counts the changes of the account signed in here before that
	// never went up, which signing in as Email removes: while it is more
	// than 0, the sign-in waits for ConfirmSignIn.
	Lost int `json:"lost"`
}

// FinishSupabaseSignIn finishes signing in with the URL the web session
// came back to, and state and verifier as BeginSupabaseSignIn returned
// them. It finds the account's Leaflet project, brings it up to this
// version of Leaflet and signs in to it as its owner, returning a
// signedIn as JSON. An account with no Leaflet project fails: the phone
// signs in to the one the desktop set up.
func (c *Core) FinishSupabaseSignIn(callbackURL, state, verifier string) (string, error) {
	answer, err := url.Parse(callbackURL)
	if err != nil {
		return "", err
	}
	// The network's part runs off the core's queue, as it runs off the
	// desktop's main thread.
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	defer cancel()
	api, err := supabaseapi.FinishSignIn(ctx, answer.Query(), state, verifier)
	if err != nil {
		return "", err
	}
	core.KeepManagement(api)
	found, err := provision.Find(ctx, api)
	if err != nil {
		return "", err
	}
	// The oldest, when there are more: the desktop asks which, and the
	// phone signs in to what it set up.
	if len(found.Projects) == 0 {
		return "", errors.New(locales.SigninNoProject())
	}
	pending, err := c.core.ConnectProject(ctx, api, found.Projects[0], nil)
	if err != nil {
		return "", err
	}
	var out signedIn
	c.q.do(func() {
		var user *store.User
		if user, err = c.core.Remote.Adopt(pending); err != nil {
			return
		}
		out.Email = user.Email
		out.Lost, err = c.signedIn(user)
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// signedIn sets the session up after signing in as user and starts
// syncing, on the queue. As someone else than the account whose pages this
// device keeps, whose changes not uploaded yet would go, it returns how
// many, to ask first; an open page that could not be saved stops it.
func (c *Core) signedIn(user *store.User) (lost int, err error) {
	if !c.saveOpen() && c.core.ReplacesAccount(user) {
		go c.core.Remote.SignOut()
		return 0, errNotSaved
	}
	if n := c.core.LostBySigningIn(user); n > 0 {
		return n, nil
	}
	return c.useAccount(user, false)
}

// useAccount makes user the session, on the queue: the previous account's
// pages go, if it was another's, with their changes not uploaded yet only
// when drop says the user agreed. A change stored since they were counted
// returns the count again, to ask again.
func (c *Core) useAccount(user *store.User, drop bool) (lost int, err error) {
	replaces := c.core.ReplacesAccount(user)
	if replaces && !c.saveOpen() {
		go c.core.Remote.SignOut()
		return 0, errNotSaved
	}
	if err := c.core.UseAccount(user, drop); err != nil {
		if errors.Is(err, store.ErrUnsynced) {
			return c.core.Store.PendingCount(), nil
		}
		return 0, err
	}
	if replaces {
		c.closePages()
	}
	c.core.Sync.Kick()
	return 0, nil
}

// ConfirmSignIn answers a sign-in that waits, as FinishSupabaseSignIn
// left it: anyway signs in, removing the changes it counted; otherwise the
// new account signs out again. With no sign-in waiting it does nothing.
func (c *Core) ConfirmSignIn(anyway bool) error {
	var err error
	c.q.do(func() {
		user := c.core.Remote.User()
		if user == nil || c.core.Store.Session().UserID == user.ID {
			return
		}
		if !anyway {
			go c.core.Remote.SignOut()
			return
		}
		var lost int
		if lost, err = c.useAccount(user, true); err == nil && lost > 0 {
			err = store.ErrUnsynced
		}
	})
	return err
}

// SignOut uploads the changes waiting, then signs out and removes the
// account's pages from this device. When some could not go up it does
// neither and returns how many, to ask; anyway signs out all the same,
// removing them. An open page that could not be saved stops it, unless
// anyway. Pages that are this device's own, never signed in, stay.
func (c *Core) SignOut(anyway bool) (int, error) {
	var saved, held bool
	c.q.do(func() {
		held = c.core.Remote.SignedIn() || c.core.Store.CachedAccount() != ""
		saved = !held || c.saveOpen()
	})
	if !held {
		return 0, nil
	}
	if !saved && !anyway {
		return 0, errNotSaved
	}
	forget := c.core.Store.ForgetAccount
	if !anyway {
		_ = c.core.Sync.SyncNow()
		forget = c.core.Store.ForgetSyncedAccount
	}
	// Between sync passes, which Leave waits for, off the queue.
	unsynced, err := c.core.Leave(forget, !anyway)
	if unsynced > 0 {
		return unsynced, nil
	}
	if err != nil {
		err = errors.New(locales.SettingsRemovePagesFailed(err.Error()))
	}
	c.q.do(func() {
		c.closePages()
		if _, e := c.core.Store.LoadSession(nil); e != nil && err == nil {
			err = e
		}
	})
	return 0, err
}

// account is who is signed in, and to which project, as Account returns
// it.
type account struct {
	SignedIn    bool   `json:"signed_in"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// Project and ProjectRef name the Supabase project Leaflet syncs
	// through.
	Project    string `json:"project"`
	ProjectRef string `json:"project_ref"`
}

// Account returns who is signed in, and to which project, as an account in
// JSON.
func (c *Core) Account() (string, error) {
	var a account
	c.q.do(func() {
		se := c.core.Store.Session()
		p := c.core.Remote.Project()
		a = account{
			SignedIn: c.core.Remote.SignedIn(), Email: se.Email, DisplayName: se.DisplayName,
			Project: p.Name, ProjectRef: p.Ref,
		}
	})
	return encode(a)
}

// saveOpen saves the open pages' edits, on the queue. It reports whether
// nothing is left to save.
func (c *Core) saveOpen() bool {
	all := true
	for _, p := range c.pages {
		if !p.live.Save() {
			all = false
		}
		p.tellSaving()
	}
	return all
}

// closePages closes the open pages, on the queue, once the account whose
// pages they were left this device: the app's editors are left with
// nothing to save to.
func (c *Core) closePages() {
	for id, p := range c.pages {
		p.live.Close()
		c.core.Sync.SetOpen(id, false)
	}
	clear(c.pages)
}

// SetDisplayName changes the name shown on the user's comments and edits,
// as the desktop's Account › Name does: on this device, then on the
// server while signed in. A name left empty or as it was changes nothing.
// Saved here but not on the server, it fails saying so.
func (c *Core) SetDisplayName(name string) error {
	name = strings.TrimSpace(name)
	var err error
	var signedIn bool
	c.q.do(func() {
		if name == "" || name == c.core.Store.Session().DisplayName {
			return
		}
		if err = c.core.Store.SetDisplayName(name); err != nil {
			err = errors.New(locales.SettingsSaveFailed(err.Error()))
			return
		}
		signedIn = c.core.Remote.SignedIn()
	})
	if err != nil || !signedIn {
		return err
	}
	// The network's part runs off the core's queue.
	be := c.core.Remote
	err = be.UpdateUser(map[string]any{"display_name": name})
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	if _, uerr := be.Upsert(ctx, "profiles", map[string]any{"id": be.UserID(), "display_name": name}, ""); err == nil {
		err = uerr
	}
	if err != nil {
		return errors.New(locales.SettingsNameServerFailed(err.Error()))
	}
	return nil
}

// remoteTimeout bounds a call to the server from Settings, as the
// desktop's.
const remoteTimeout = 20 * time.Second

// device is a device signed in to the account, as Devices lists it.
type device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// LastActive is in milliseconds since 1970.
	LastActive int64 `json:"last_active"`
	// This is the device the app runs on, which signs out from Account's
	// own Sign Out; Revoked, one signed out from another.
	This    bool `json:"this"`
	Revoked bool `json:"revoked"`
}

// Devices returns the devices signed in to the account, the one active
// last first, as a JSON array of device: Account › Devices.
func (c *Core) Devices() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	rows, err := c.core.Remote.Select(ctx, "devices", remote.Query{Order: "last_active_at.desc"})
	if err != nil {
		return "", err
	}
	me := c.core.Store.DeviceID()
	out := make([]device, 0, len(rows))
	for _, d := range rows {
		id := store.S(d, "id")
		out = append(out, device{
			ID: id, Name: store.S(d, "name"), LastActive: millis(store.Time(d["last_active_at"])),
			This: id == me, Revoked: d["revoked_at"] != nil,
		})
	}
	return encode(out)
}

// SignOutDevice signs another device out of the account: it signs itself
// out the next time it syncs.
func (c *Core) SignOutDevice(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	be := c.core.Remote
	_, err := be.Upsert(ctx, "devices", map[string]any{"id": id, "user_id": be.UserID(), "revoked_at": time.Now().UTC().Format(time.RFC3339)}, "")
	if err != nil {
		return errors.New(locales.SettingsDeviceSignOutFailed(err.Error()))
	}
	return nil
}

// deleteTimeout bounds deleting the account on the server.
const deleteTimeout = 60 * time.Second

// DeleteAccount deletes the account on the server, its space, pages,
// recordings and keys, then signs out and wipes this device, as the
// desktop's Delete account does once DELETE is typed.
func (c *Core) DeleteAccount() error {
	var signedIn bool
	c.q.do(func() { signedIn = c.core.Remote.SignedIn() })
	if !signedIn {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), deleteTimeout)
	defer cancel()
	if _, err := c.core.Remote.Invoke(ctx, "account", map[string]any{"action": "delete"}); err != nil {
		return errors.New(locales.SettingsDeleteAccountFailed(err.Error()))
	}
	// Nothing of the account is left to keep: the open pages go unsaved.
	_, err := c.core.Leave(c.core.Store.Wipe, false)
	if err != nil {
		err = errors.New(locales.SettingsRemovePagesFailed(err.Error()))
	}
	c.q.do(func() {
		c.closePages()
		if _, e := c.core.Store.LoadSession(nil); e != nil && err == nil {
			err = e
		}
	})
	return err
}
