// Package keychain keeps the app's secrets in the system keychain, read
// and written at most once a run each. macOS may ask before each access,
// so a secret the app needs often (a key at every recording, a session at
// every refresh) costs one prompt, not one each time.
package keychain

import (
	"bytes"
	"errors"
	"sync"

	"github.com/egoist/mygo"
)

type entry struct {
	value []byte
	err   error // mygo.ErrSecretNotFound, or why the keychain refused
}

var (
	mu    sync.Mutex
	cache = map[string]entry{}
)

// Get returns a secret, or mygo.ErrSecretNotFound. A refusal is kept for
// the run too, so a denied prompt is not asked again at once.
func Get(name string) ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	e, ok := cache[name]
	if !ok {
		v, err := mygo.Secrets.Get(name)
		if v == nil && err == nil {
			v = []byte{}
		}
		e = entry{value: v, err: err}
		cache[name] = e
	}
	return bytes.Clone(e.value), e.err
}

// Set keeps a secret, unless it already holds that value.
func Set(name string, value []byte) error {
	mu.Lock()
	defer mu.Unlock()
	if e, ok := cache[name]; ok && e.err == nil && bytes.Equal(e.value, value) {
		return nil
	}
	if err := mygo.Secrets.Set(name, value); err != nil {
		return err
	}
	cache[name] = entry{value: bytes.Clone(value)}
	return nil
}

// Delete removes a secret; one that is not there is already deleted.
func Delete(name string) error {
	mu.Lock()
	defer mu.Unlock()
	if e, ok := cache[name]; ok && errors.Is(e.err, mygo.ErrSecretNotFound) {
		return nil
	}
	if err := mygo.Secrets.Delete(name); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
		return err
	}
	cache[name] = entry{err: mygo.ErrSecretNotFound}
	return nil
}
