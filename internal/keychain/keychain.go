// Package keychain keeps the app's secrets in the system keychain, read
// and written at most once a run each. macOS may ask before each access,
// so a secret the app needs often (a key at every recording, a session at
// every refresh) costs one prompt, not one each time.
package keychain

import (
	"bytes"
	"errors"
	"sync"
)

// Store is the system's credential store the secrets are kept in: MyGo's
// Secrets on the desktop, the keychain through SecItem on iOS. Use sets
// it as the app starts.
type Store interface {
	// Get returns the secret kept under name, or an error for which
	// errors.Is(err, ErrNotFound) holds when there is none.
	Get(name string) ([]byte, error)
	// Set keeps value under name, in place of what it had.
	Set(name string, value []byte) error
	// Delete removes the secret under name; one that is not there fails
	// with ErrNotFound, or with no error.
	Delete(name string) error
}

// ErrNotFound is what Get fails with when no secret is kept under a name.
// A Store's own error for it must match it with errors.Is.
var ErrNotFound = errors.New("keychain: no secret kept under this name")

// MaxSecretSize is the largest secret kept, in bytes: the most the
// Windows Credential Manager keeps, so that the app behaves the same on
// every system.
const MaxSecretSize = 2560

type entry struct {
	value []byte
	err   error // ErrNotFound, or why the keychain refused
}

var (
	mu    sync.Mutex
	store Store
	cache = map[string]entry{}
)

// errNoStore is what each call fails with before Use gives the package a
// store.
var errNoStore = errors.New("keychain: no store to keep secrets in")

// Use makes s the store the secrets are kept in, forgetting what was read
// from the one before. Call it as the app starts, before the first secret.
func Use(s Store) {
	mu.Lock()
	defer mu.Unlock()
	store = s
	cache = map[string]entry{}
}

// Get returns a secret, or ErrNotFound. A refusal is kept for the run too,
// so a denied prompt is not asked again at once.
func Get(name string) ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return nil, errNoStore
	}
	e, ok := cache[name]
	if !ok {
		v, err := store.Get(name)
		if v == nil && err == nil {
			v = []byte{}
		}
		if err != nil && errors.Is(err, ErrNotFound) {
			err = ErrNotFound
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
	if store == nil {
		return errNoStore
	}
	if e, ok := cache[name]; ok && e.err == nil && bytes.Equal(e.value, value) {
		return nil
	}
	if err := store.Set(name, value); err != nil {
		return err
	}
	cache[name] = entry{value: bytes.Clone(value)}
	return nil
}

// Delete removes a secret; one that is not there is already deleted.
func Delete(name string) error {
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return errNoStore
	}
	if e, ok := cache[name]; ok && errors.Is(e.err, ErrNotFound) {
		return nil
	}
	if err := store.Delete(name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	cache[name] = entry{err: ErrNotFound}
	return nil
}
