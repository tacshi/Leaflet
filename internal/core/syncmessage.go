package core

import (
	"context"
	"errors"

	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/locales"
)

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
