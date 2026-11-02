package app

import (
	"errors"
	"fmt"

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

// secretErr makes MyGo's "no such secret" the keychain's, and the user's
// refusal of the system's prompt the keychain's ErrDenied.
func secretErr(err error) error {
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return fmt.Errorf("%w: %w", keychain.ErrNotFound, err)
	}
	return keychain.Denied(err)
}
