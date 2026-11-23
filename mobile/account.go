package mobile

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sync"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/provision"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

type supabaseSignIn struct {
	URL      string `json:"url"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
}

func BeginSupabaseSignIn() (_ string, err error) {
	defer catch(&err)
	u, state, verifier := supabaseapi.BeginSignIn()
	return encode(supabaseSignIn{URL: u, State: state, Verifier: verifier})
}

const signInTimeout = 10 * time.Minute

func errNotSaved() error { return errors.New(locales.AppErrorNotSaved()) }

// signedIn is what a sign-in comes to: the account's email, and how many pages not synced yet
// signing in would remove, which the person confirms first; or, with Projects, the projects to
// choose among before it signs in.
type signedIn struct {
	Email    string            `json:"email"`
	Lost     int               `json:"lost"`
	Projects []supabaseProject `json:"projects,omitempty"`
}

type supabaseProject struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Paused bool   `json:"paused"`
}

// projectChoice is a sign-in to Supabase waiting for the person to choose which of the
// account's projects to use.
type projectChoice struct {
	mu       sync.Mutex
	api      *supabaseapi.Client
	projects []supabaseapi.Project
}

func (p *projectChoice) wait(api *supabaseapi.Client, projects []supabaseapi.Project) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.api, p.projects = api, projects
}

func (p *projectChoice) of(ref string) (*supabaseapi.Client, supabaseapi.Project, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.projects, func(p supabaseapi.Project) bool { return p.Ref == ref })
	if i < 0 {
		return nil, supabaseapi.Project{}, false
	}
	return p.api, p.projects[i], true
}

func (p *projectChoice) made(api *supabaseapi.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.api == api {
		p.api, p.projects = nil, nil
	}
}

// FinishSupabaseSignIn signs in to the account's Leaflet project, or, of several, to the one
// this device used last. With several and none of them that one, it answers with the projects,
// and UseSupabaseProject signs in to the one chosen.
func (c *Core) FinishSupabaseSignIn(callbackURL, state, verifier string) (_ string, err error) {
	defer catch(&err)
	answer, err := url.Parse(callbackURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	defer cancel()
	api, err := supabaseapi.FinishSignIn(ctx, answer.Query(), state, verifier)
	if err != nil {
		return "", err
	}
	var last string
	c.q.do(func() { last = c.core.Remote.Project().Ref })
	project, choices, err := leafletProject(ctx, api, last)
	if err != nil {
		return "", err
	}
	if len(choices) > 0 {
		c.choosing.wait(api, choices)
		out := signedIn{Projects: make([]supabaseProject, 0, len(choices))}
		for _, p := range choices {
			out.Projects = append(out.Projects, supabaseProject{Ref: p.Ref, Name: p.Name, Region: p.Region, Paused: p.Status == "INACTIVE"})
		}
		return encode(out)
	}
	return c.signInTo(ctx, api, project)
}

// UseSupabaseProject signs in to the project ref, of those FinishSupabaseSignIn answered with.
// Until it has, the others can still be chosen.
func (c *Core) UseSupabaseProject(ref string) (_ string, err error) {
	defer catch(&err)
	api, project, ok := c.choosing.of(ref)
	if !ok {
		return "", errors.New(locales.OauthTimedOut())
	}
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	defer cancel()
	if project, err = woken(ctx, api, project); err != nil {
		return "", err
	}
	out, err := c.signInTo(ctx, api, project)
	if err != nil {
		return "", err
	}
	c.choosing.made(api)
	return out, nil
}

func (c *Core) signInTo(ctx context.Context, api *supabaseapi.Client, project supabaseapi.Project) (_ string, err error) {
	pending, err := c.core.ConnectProject(ctx, api, project, nil)
	if err != nil {
		return "", err
	}
	var out signedIn
	c.q.do(func() {
		var user *store.User
		if user, err = c.core.AdoptProject(pending, api); err != nil {
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

func (c *Core) FinishProjectSignIn(callbackURL, state, verifier string) (err error) {
	defer catch(&err)
	answer, err := url.Parse(callbackURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	defer cancel()
	api, err := supabaseapi.FinishSignIn(ctx, answer.Query(), state, verifier)
	if err != nil {
		return err
	}
	return c.core.RenewProject(ctx, api)
}

// leafletProject is the Leaflet project of api's account to sign in to, ready for it, as
// pickProject picks it; when it picks none of several, choices are those.
func leafletProject(ctx context.Context, api *supabaseapi.Client, last string) (_ supabaseapi.Project, choices []supabaseapi.Project, err error) {
	found, err := provision.Find(ctx, api)
	if err != nil {
		return supabaseapi.Project{}, nil, err
	}
	p, choices := pickProject(found, last)
	switch {
	case len(choices) > 0:
		return supabaseapi.Project{}, choices, nil
	case p.Ref == "":
		return supabaseapi.Project{}, nil, errors.New(locales.SigninNoProject())
	}
	p, err = woken(ctx, api, p)
	return p, nil, err
}

// pickProject picks the project to sign in to of those found: the one Leaflet project there
// is, as the desktop app does, or of several, the one this device used last, whose ref is
// last. A project named Leaflet that is paused, or starting, is among them, as it may be
// Leaflet's once up, but not one Leaflet has not set up, as the phone sets none up. With
// several and none of them that one, it picks none, and the person chooses among choices.
func pickProject(found provision.Found, last string) (_ supabaseapi.Project, choices []supabaseapi.Project) {
	if len(found.Projects) == 1 {
		return found.Projects[0], nil
	}
	choices = slices.Clone(found.Projects)
	for _, p := range found.Unfinished {
		if !p.Healthy() {
			choices = append(choices, p)
		}
	}
	if i := slices.IndexFunc(choices, func(p supabaseapi.Project) bool { return p.Ref == last }); i >= 0 {
		return choices[i], nil
	}
	if len(choices) == 1 {
		return choices[0], nil
	}
	return supabaseapi.Project{}, choices
}

// woken is p ready to sign in to: one that is paused, or starting, is woken and waited for,
// and taken only if it holds Leaflet's tables.
func woken(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project) (supabaseapi.Project, error) {
	if p.Healthy() {
		return p, nil
	}
	p, err := provision.Ready(ctx, api, p)
	if err != nil {
		return supabaseapi.Project{}, err
	}
	applied, err := api.Migrations(ctx, p.Ref)
	if err != nil {
		return supabaseapi.Project{}, err
	}
	if !provision.IsLeaflet(applied) {
		return supabaseapi.Project{}, errors.New(locales.SigninNoProject())
	}
	return p, nil
}

func (c *Core) signedIn(user *store.User) (lost int, err error) {
	if !c.saveOpen() && c.core.ReplacesAccount(user) {
		guard.Go(c.core.Remote.SignOut)
		return 0, errNotSaved()
	}
	if n := c.core.LostBySigningIn(user); n > 0 {
		return n, nil
	}
	return c.useAccount(user, false)
}

func (c *Core) useAccount(user *store.User, drop bool) (lost int, err error) {
	replaces := c.core.ReplacesAccount(user)
	if replaces && !c.saveOpen() {
		guard.Go(c.core.Remote.SignOut)
		return 0, errNotSaved()
	}
	// UseAccount turns iCloud sync off, which a sign-in ends: the setting and the watch on the folder follow here.
	err = c.core.UseAccount(user, drop)
	c.core.KeepICloud(c.core.Sync.ICloudOn())
	if err != nil {
		if errors.Is(err, store.ErrUnsynced) {
			return c.core.Store.PendingCount(), nil
		}
		guard.Go(c.core.Remote.SignOut)
		return 0, err
	}
	if replaces {
		c.closePages()
	}
	c.core.Sync.Kick()
	return 0, nil
}

func (c *Core) ConfirmSignIn(anyway bool) (err error) {
	defer catch(&err)
	c.q.do(func() {
		user := c.core.Remote.User()
		if user == nil || c.core.Store.Session().UserID == user.ID {
			return
		}
		if !anyway {
			guard.Go(c.core.Remote.SignOut)
			return
		}
		var lost int
		if lost, err = c.useAccount(user, true); err == nil && lost > 0 {
			err = store.ErrUnsynced
		}
	})
	return err
}

func (c *Core) SignOut(anyway bool) (unsynced int, err error) {
	defer catch(&err)
	var saved, held bool
	c.q.do(func() {
		held = c.core.Remote.SignedIn() || c.core.Store.CachedAccount() != ""
		saved = !held || c.saveOpen()
	})
	if !held {
		return 0, nil
	}
	if !saved && !anyway {
		return 0, errNotSaved()
	}
	forget := c.core.Store.ForgetAccount
	if !anyway {
		_ = c.core.Sync.Drain()
		forget = c.core.Store.ForgetSyncedAccount
		c.q.do(func() { saved = c.saveOpen() })
		if !saved {
			return 0, errNotSaved()
		}
	}
	unsynced, err = c.leave(forget, !anyway)
	if unsynced > 0 {
		return unsynced, nil
	}
	if err != nil {
		err = errors.New(locales.SettingsRemovePagesFailed(err.Error()))
	}
	c.q.do(func() {
		if _, e := c.core.Store.LoadSession(nil); e != nil && err == nil {
			err = e
		}
	})
	return 0, err
}

func (c *Core) leave(clear func() error, synced bool) (int, error) {
	return c.core.Leave(func() (err error) {
		c.q.do(func() {
			if err = clear(); !errors.Is(err, store.ErrUnsynced) {
				c.closePages()
			}
		})
		return err
	}, synced)
}

type account struct {
	SignedIn           bool   `json:"signed_in"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	Project            string `json:"project"`
	ProjectRef         string `json:"project_ref"`
	ProjectNeedsSignIn bool   `json:"project_needs_sign_in"`
}

func (c *Core) Account() (_ string, err error) {
	defer catch(&err)
	var a account
	c.q.do(func() {
		se := c.core.Store.Session()
		p := c.core.Remote.Project()
		a = account{
			SignedIn: c.core.Remote.SignedIn(), Email: se.Email, DisplayName: se.DisplayName,
			Project: p.Name, ProjectRef: p.Ref, ProjectNeedsSignIn: c.core.ProjectNeedsSignIn(),
		}
	})
	return encode(a)
}

func (c *Core) saveOpen() bool {
	all := true
	for _, p := range c.pages {
		if !p.live.Save() {
			all = false
		}
	}
	return all
}

func (c *Core) closePages() {
	for id, p := range c.pages {
		p.live.Close()
		c.core.Sync.SetOpen(id, false)
	}
	clear(c.pages)
}

func (c *Core) SetDisplayName(name string) (err error) {
	defer catch(&err)
	var changed bool
	c.q.do(func() { changed, err = c.core.SetDisplayName(name) })
	if err != nil || !changed {
		return err
	}
	return c.core.SendDisplayName()
}

const remoteTimeout = 20 * time.Second

type device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	LastActive int64  `json:"last_active"`
	This       bool   `json:"this"`
	Revoked    bool   `json:"revoked"`
}

func (c *Core) Devices() (_ string, err error) {
	defer catch(&err)
	ds, err := c.core.Devices()
	if err != nil {
		return "", err
	}
	out := make([]device, 0, len(ds))
	for _, d := range ds {
		out = append(out, device{ID: d.ID, Name: d.Name, LastActive: millis(d.LastActive), This: d.This, Revoked: d.Revoked})
	}
	return encode(out)
}

func (c *Core) SignOutDevice(id string) (err error) {
	defer catch(&err)
	return c.core.SignOutDevice(id)
}

func (c *Core) DeleteAccount() (err error) {
	defer catch(&err)
	var signedIn bool
	c.q.do(func() { signedIn = c.core.Remote.SignedIn() })
	if !signedIn {
		return nil
	}
	if err := c.core.DeleteAccount(); err != nil {
		return err
	}
	_, err = c.leave(c.core.Store.Wipe, false)
	if err != nil {
		err = errors.New(locales.SettingsRemovePagesFailed(err.Error()))
	}
	c.q.do(func() {
		if _, e := c.core.Store.LoadSession(nil); e != nil && err == nil {
			err = e
		}
	})
	return err
}
