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

const remoteTimeout = 20 * time.Second

const deleteTimeout = 60 * time.Second

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

type Device struct {
	ID, Name      string
	LastActive    time.Time
	This, Revoked bool
}

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

func (c *Core) SignOutDevice(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	if _, err := c.Remote.RPC(ctx, "sign_out_device", map[string]any{"p_device_id": id}); err != nil {
		return errors.New(locales.SettingsDeviceSignOutFailed(err.Error()))
	}
	return nil
}

func (c *Core) DeleteAccount() error {
	ctx, cancel := context.WithTimeout(context.Background(), deleteTimeout)
	defer cancel()
	if _, err := c.Remote.Invoke(ctx, "account", map[string]any{"action": "delete"}); err != nil {
		return errors.New(locales.SettingsDeleteAccountFailed(err.Error()))
	}
	if c.Hub != nil {
		c.Hub.ForgetProviders(ctx)
	}
	forgetEndpoint := func(key string) error { return recording.SetEndpointKey("", key) }
	for _, forget := range []func(string) error{forgetEndpoint, recording.SetLocalSonioxKey, recording.SetMossKey} {
		if err := forget(""); err != nil {
			log.Printf("forget a speech key: %v", err)
		}
	}
	return nil
}
