package mobile

import (
	"errors"
	"testing"

	"leaflet/internal/keychain"
)

// failingKeychain is the app's keychain failing each call with err, as
// the app's Keychain.Failure says it.
type failingKeychain struct {
	*fakePlatform
	err error
}

func (k failingKeychain) SecretGet(string) ([]byte, error) { return nil, k.err }
func (k failingKeychain) SecretSet(string, []byte) error   { return k.err }
func (k failingKeychain) SecretDelete(name string) error   { return k.err }

// The keychain's statuses reach package keychain as it tells them apart:
// none kept, the user's refusal, and anything else, as the device still
// locked since it restarted, to try again.
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
	// No bytes, and no error, is none kept.
	s := secrets{&fakePlatform{t: t, secrets: map[string][]byte{}}}
	if _, err := s.Get("session"); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("an empty answer: %v", err)
	}
}
