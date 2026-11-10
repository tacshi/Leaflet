package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type Store interface {
	Get(name string) ([]byte, error)
	Set(name string, value []byte) error
	Delete(name string) error
}

var ErrNotFound = errors.New("keychain: no secret kept under this name")

var ErrDenied = errors.New("keychain: the user did not allow reading the secret")

func Denied(err error) error {
	if s, ok := OSStatus(err); ok && (s == errSecAuthFailed || s == errSecUserCanceled) {
		return fmt.Errorf("%w: %w", ErrDenied, err)
	}
	return err
}

const (
	errSecAuthFailed   = -25293
	errSecUserCanceled = -128
)

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

const MaxSecretSize = 2560

type entry struct {
	value []byte
	err   error
}

var (
	mu    sync.Mutex
	store Store
	cache = map[string]entry{}
)

var errNoStore = errors.New("keychain: no store to keep secrets in")

func Use(s Store) {
	mu.Lock()
	defer mu.Unlock()
	store = s
	cache = map[string]entry{}
}

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
