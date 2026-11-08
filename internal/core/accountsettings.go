package core

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Account, as the desktop and the iOS app have it: the name
// shown on the user's comments and edits, the devices signed in to the
// account, and deleting it. What goes to the server runs off the app's
// main thread; what fails says why in words for the user.

// remoteTimeout bounds a call to the server from Account.
const remoteTimeout = 20 * time.Second

// deleteTimeout bounds deleting the account on the server.
const deleteTimeout = 60 * time.Second

// SetDisplayName changes the name shown on the user's comments and edits
// on this device, without the spaces around it; SendDisplayName then
// changes it on the server. A name left empty or as it was changes
// nothing, and reports false.
func (c *Core) SetDisplayName(name string) (changed bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" || name == c.Store.Session().DisplayName {
		return false, nil
	}
	if err := c.Store.SetDisplayName(name); err != nil {
		return false, errors.New(locales.SettingsSaveFailed(err.Error()))
	}
	return true, nil
}

// SendDisplayName changes the name on the server to the one kept here,
// signed in: on the account, and on its profile, which others see.
func (c *Core) SendDisplayName() error {
	be := c.Remote
	if !be.SignedIn() {
		return nil
	}
	name := c.Store.Session().DisplayName
	err := be.UpdateUser(map[string]any{"display_name": name})
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

// Device is a device signed in to the account, as Account › Devices
// lists it.
type Device struct {
	ID, Name   string
	LastActive time.Time
	// This is the device the app runs on, which signs out from Account's
	// own Sign Out; Revoked, one signed out from another.
	This, Revoked bool
}

// Devices returns the devices signed in to the account, the one active
// last first.
func (c *Core) Devices() ([]Device, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	rows, err := c.Remote.Select(ctx, "devices", remote.Query{Order: "last_active_at.desc"})
	if err != nil {
		return nil, err
	}
	me := c.Store.DeviceID()
	out := make([]Device, 0, len(rows))
	for _, d := range rows {
		id := store.S(d, "id")
		out = append(out, Device{
			ID: id, Name: store.S(d, "name"), LastActive: store.Time(d["last_active_at"]),
			This: id == me, Revoked: d["revoked_at"] != nil,
		})
	}
	return out, nil
}

// SignOutDevice signs another device out of the account, through the
// server's sign_out_device: its row is revoked, which no device can undo,
// and the session it syncs with ends, so it can no longer renew its
// token. It signs itself out the next time it syncs.
func (c *Core) SignOutDevice(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	if _, err := c.Remote.RPC(ctx, "sign_out_device", map[string]any{"p_device_id": id}); err != nil {
		return errors.New(locales.SettingsDeviceSignOutFailed(err.Error()))
	}
	return nil
}

// DeleteAccount deletes the account on the server, its space, pages,
// recordings and keys, and the keys and sign-ins of agents and speech this
// device's keychain keeps, which Store.Wipe leaves nothing to name. The
// app then leaves this device with Store.Wipe (Leave), as nothing of the
// account is left to keep.
func (c *Core) DeleteAccount() error {
	ctx, cancel := context.WithTimeout(context.Background(), deleteTimeout)
	defer cancel()
	if _, err := c.Remote.Invoke(ctx, "account", map[string]any{"action": "delete"}); err != nil {
		return errors.New(locales.SettingsDeleteAccountFailed(err.Error()))
	}
	if c.Hub != nil {
		c.Hub.ForgetProviders(ctx)
	}
	for _, forget := range []func(string) error{recording.SetEndpointKey, recording.SetLocalSonioxKey, recording.SetMossKey} {
		if err := forget(""); err != nil {
			log.Printf("forget a speech key: %v", err)
		}
	}
	return nil
}
