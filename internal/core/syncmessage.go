package core

import (
	"context"
	"errors"

	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/locales"
)

// SyncMessage says why a sync pass failed with the server, in the
// language the app shows, the same on the desktop and the phone: signed
// out, timed out, or refused, with what the server said. ok is false for
// another error, which the app words itself.
func SyncMessage(err error) (msg string, ok bool) {
	switch re, refused := errors.AsType[*remote.Error](err); {
	case err == nil:
		return "", false
	case errors.Is(err, remote.ErrSignedOut):
		return locales.SettingsSyncSignedOut(), true
	case errors.Is(err, context.DeadlineExceeded):
		return locales.SettingsSyncTimedOut(), true
	case refused:
		return locales.SettingsSyncRefused(re.Status, re.Message), true
	}
	return "", false
}

// ICloudMessage says why sync through iCloud Drive failed, in the
// language the app shows, the same on the desktop and the phone: iCloud
// Drive not there, the Leaflet folder replaced, or written by a newer
// Leaflet. ok is false for another error. No access is each app's to word:
// on the desktop the system keeps Leaflet out, while the phone has lost
// the folder the user picked, to pick again.
func ICloudMessage(err error) (msg string, ok bool) {
	switch {
	case errors.Is(err, icloud.ErrUnavailable):
		return locales.SettingsICloudUnavailable(), true
	case errors.Is(err, icloud.ErrOtherLibrary):
		return locales.SettingsICloudOtherLibrary(), true
	case errors.Is(err, icloud.ErrNewerFormat):
		return locales.SettingsICloudNewer(), true
	}
	return "", false
}
