package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/egoist/mygo"

	"leaflet/internal/keychain"
)

// MyGo's errors become the keychain's: no such secret, and the user
// turning down or cancelling the system's prompt; any other stays as it
// is.
func TestSecretErr(t *testing.T) {
	failed := errors.New("keychain: read: write permissions error (OSStatus -61)")
	for _, tc := range []struct {
		err              error
		notFound, denied bool
	}{
		{fmt.Errorf("read: %w", mygo.ErrSecretNotFound), true, false},
		{errors.New("keychain: read: The user name or passphrase you entered is not correct. (OSStatus -25293)"), false, true},
		{errors.New("keychain: read: User canceled the operation. (OSStatus -128)"), false, true},
		{failed, false, false},
	} {
		got := secretErr(tc.err)
		if errors.Is(got, keychain.ErrNotFound) != tc.notFound || errors.Is(got, keychain.ErrDenied) != tc.denied || !errors.Is(got, tc.err) {
			t.Errorf("secretErr(%v) = %v", tc.err, got)
		}
	}
	if secretErr(nil) != nil {
		t.Error("no error became one")
	}
}
