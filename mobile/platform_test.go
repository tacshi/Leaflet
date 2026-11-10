package mobile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"leaflet/internal/icloud"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/locales"
)

type failingKeychain struct {
	*fakePlatform
	err error
}

func (k failingKeychain) SecretGet(string) ([]byte, error) { return nil, k.err }
func (k failingKeychain) SecretSet(string, []byte) error   { return k.err }
func (k failingKeychain) SecretDelete(name string) error   { return k.err }

func TestKeychainStatuses(t *testing.T) {
	for _, tc := range []struct {
		message           string
		notFound, refused bool
	}{
		{"The specified item could not be found in the keychain. (OSStatus -25300)", true, false},
		{"The user name or passphrase you entered is not correct. (OSStatus -25293)", false, true},
		{"The operation couldn’t be completed. (OSStatus -128)", false, true},
		{"User interaction is not allowed. (OSStatus -25308)", false, false},
		{"Keychain error -25308", false, false},
		{"(OSStatus x)", false, false},
	} {
		s := secrets{failingKeychain{&fakePlatform{t: t}, errors.New(tc.message)}}
		_, err := s.Get("session")
		if errors.Is(err, keychain.ErrNotFound) != tc.notFound || errors.Is(err, keychain.ErrDenied) != tc.refused {
			t.Errorf("%q: Get failed with %v", tc.message, err)
		}
		if err == nil || err.Error() == "" {
			t.Errorf("%q: no error", tc.message)
		}
		if err := s.Delete("session"); errors.Is(err, keychain.ErrNotFound) != tc.notFound {
			t.Errorf("%q: Delete failed with %v", tc.message, err)
		}
	}
	s := secrets{&fakePlatform{t: t, secrets: map[string][]byte{}}}
	if _, err := s.Get("session"); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("an empty answer: %v", err)
	}
}

type syncEvents struct {
	fakeEvents
	failures chan string
}

func (e *syncEvents) SyncState(running bool, failure string) { e.failures <- failure }

func TestSyncStateWordsTheServersFailures(t *testing.T) {
	to := &syncEvents{failures: make(chan string, 1)}
	e := &events{to: to, q: newQueue()}
	other := errors.New("disk full")
	for err, want := range map[error]string{
		fmt.Errorf("push: %w", remote.ErrSignedOut):                       locales.SettingsSyncSignedOut(),
		fmt.Errorf("push: %w", context.DeadlineExceeded):                  locales.SettingsSyncTimedOut(),
		fmt.Errorf("push: %w", &remote.Error{Status: 403, Message: "no"}): locales.SettingsSyncRefused(403, "no"),
		fmt.Errorf("pass: %w", icloud.ErrUnavailable):                     locales.SettingsICloudUnavailable(),
		fmt.Errorf("pass: %w", icloud.ErrOtherLibrary):                    locales.SettingsICloudOtherLibrary(),
		fmt.Errorf("pass: %w", icloud.ErrNewerFormat):                     locales.SettingsICloudNewer(),
		fmt.Errorf("pass: %w", icloud.ErrNoAccess):                        locales.SettingsICloudFolderLost(),
		other: "disk full",
		nil:   "",
	} {
		e.syncState(false, err)
		select {
		case got := <-to.failures:
			if got != want {
				t.Errorf("%v: %q, want %q", err, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: no SyncState", err)
		}
	}
}
