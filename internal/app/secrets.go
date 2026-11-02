package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo"

	"leaflet/internal/keychain"
)

func init() {
	// The desktop's secrets are MyGo's, for the app and leaflet mcp alike.
	keychain.Use(mygoSecrets{})
}

// mygoSecrets is the keychain's store on the desktop: MyGo's Secrets, the
// login Keychain on macOS and the Credential Manager on Windows.
type mygoSecrets struct{}

func (mygoSecrets) Get(name string) ([]byte, error) {
	v, err := mygo.Secrets.Get(name)
	return v, secretErr(err)
}

func (mygoSecrets) Set(name string, value []byte) error { return mygo.Secrets.Set(name, value) }

func (mygoSecrets) Delete(name string) error { return secretErr(mygo.Secrets.Delete(name)) }

// deniedStatuses are how MyGo's Secrets ends its errors on macOS when the
// user denies its prompt (errSecAuthFailed) or cancels it
// (errSecUserCanceled): it gives no error of its own to match.
var deniedStatuses = []string{"(OSStatus -25293)", "(OSStatus -128)"}

// secretErr makes MyGo's "no such secret" the keychain's, and the user's
// refusal of the system's prompt the keychain's ErrDenied.
func secretErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return fmt.Errorf("%w: %w", keychain.ErrNotFound, err)
	}
	for _, s := range deniedStatuses {
		if strings.HasSuffix(err.Error(), s) {
			return fmt.Errorf("%w: %w", keychain.ErrDenied, err)
		}
	}
	return err
}
