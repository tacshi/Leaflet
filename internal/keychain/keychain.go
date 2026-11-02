// Package keychain keeps the app's secrets in the system keychain, read
// and written at most once a run each. macOS may ask before each access,
// so a secret the app needs often (a key at every recording, a session at
// every refresh) costs one prompt, not one each time.
package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Store is the system's credential store the secrets are kept in: MyGo's
// Secrets on the desktop, the keychain through SecItem on iOS. Use sets
// it as the app starts.
type Store interface {
	// Get returns the secret kept under name, or an error for which
	// errors.Is(err, ErrNotFound) holds when there is none, and
	// errors.Is(err, ErrDenied) when the user turned the system's prompt
	// for it down.
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

// ErrDenied is what a Store's Get fails with when the user turned down the
// system's prompt to read the secret. A Store's own error for it must
// match it with errors.Is.
var ErrDenied = errors.New("keychain: the user did not allow reading the secret")

// Denied wraps err in ErrDenied when it says the user turned the system's
// prompt down, as macOS's keychain says it: MyGo's Secrets on the desktop
// and the iOS app's keychain end their errors with the Security
// framework's status, and give no error of their own to match. Any other
// error is returned as it is.
func Denied(err error) error {
	if s, ok := OSStatus(err); ok && (s == errSecAuthFailed || s == errSecUserCanceled) {
		return fmt.Errorf("%w: %w", ErrDenied, err)
	}
	return err
}

// The Security framework's statuses for the user denying its prompt
// (errSecAuthFailed) or cancelling it (errSecUserCanceled).
const (
	errSecAuthFailed   = -25293
	errSecUserCanceled = -128
)

// OSStatus is the Security framework's status err ends with, as
// "(OSStatus -25308)", if it ends with one.
func OSStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	rest, ok := strings.CutSuffix(err.Error(), ")")
	if !ok {
		return 0, false
	}
	_, code, ok := strings.CutLast(rest, "(OSStatus ")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(code)
	return n, err == nil
}

// MaxSecretSize is the largest secret kept, in bytes: the most the
// Windows Credential Manager keeps, so that the app behaves the same on
// every system.
const MaxSecretSize = 2560

type entry struct {
	value []byte
	err   error // ErrNotFound, or the user's refusal
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

// Get returns a secret, or ErrNotFound. The user's refusal is kept for
// the run too, so a denied prompt is not asked again at once; any other
// failure is not, so a read the system turned down for now, such as the
// iOS keychain's before the device is first unlocked when the app starts
// in the background, is tried again the next time.
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
		switch {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			err = ErrNotFound
		case !errors.Is(err, ErrDenied):
			return nil, err
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
