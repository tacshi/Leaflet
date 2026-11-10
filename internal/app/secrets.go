package app

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo"

	"leaflet/internal/keychain"
)

func init() {
	keychain.Use(mygoSecrets{})
}

type mygoSecrets struct{}

func (mygoSecrets) Get(name string) ([]byte, error) {
	v, err := mygo.Secrets.Get(name)
	return v, secretErr(err)
}

func (mygoSecrets) Set(name string, value []byte) error { return mygo.Secrets.Set(name, value) }

func (mygoSecrets) Delete(name string) error { return secretErr(mygo.Secrets.Delete(name)) }

func secretErr(err error) error {
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return fmt.Errorf("%w: %w", keychain.ErrNotFound, err)
	}
	return keychain.Denied(err)
}
