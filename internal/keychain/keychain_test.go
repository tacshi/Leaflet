package keychain

import (
	"errors"
	"fmt"
	"testing"
)

// memStore is a Store in memory, which counts what it is asked.
type memStore struct {
	secrets map[string][]byte
	reads   int
}

func (m *memStore) Get(name string) ([]byte, error) {
	m.reads++
	v, ok := m.secrets[name]
	if !ok {
		return nil, fmt.Errorf("system: %w", ErrNotFound)
	}
	return v, nil
}

func (m *memStore) Set(name string, value []byte) error {
	m.secrets[name] = value
	return nil
}

func (m *memStore) Delete(name string) error {
	if _, ok := m.secrets[name]; !ok {
		return ErrNotFound
	}
	delete(m.secrets, name)
	return nil
}

// A secret is read from the store once a run, missing ones too, and one
// not there is already deleted.
func TestSecretsReadOnce(t *testing.T) {
	m := &memStore{secrets: map[string][]byte{}}
	Use(m)
	t.Cleanup(func() { Use(nil) })
	for range 2 {
		if _, err := Get("key"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a missing secret: %v", err)
		}
	}
	if err := Set("key", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("key"); err != nil || string(v) != "v" {
		t.Fatalf("got %q, %v", v, err)
	}
	if m.reads != 1 {
		t.Errorf("read the store %d times", m.reads)
	}
	if err := Delete("key"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("key"); err != nil {
		t.Errorf("deleting again: %v", err)
	}
	if _, err := Get("key"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete: %v", err)
	}
}

// Without a store, nothing is kept, and nothing panics.
func TestNoStore(t *testing.T) {
	Use(nil)
	if _, err := Get("key"); err == nil {
		t.Error("read a secret from no store")
	}
	if err := Set("key", nil); err == nil {
		t.Error("kept a secret in no store")
	}
}

// failingStore fails each Get with err.
type failingStore struct {
	memStore
	err error
}

func (f *failingStore) Get(string) ([]byte, error) {
	f.reads++
	return nil, f.err
}

// Only the user's refusal is kept for the run: another failure, such as
// the iOS keychain's before the device is first unlocked, is asked again.
func TestOnlyRefusalsKept(t *testing.T) {
	t.Cleanup(func() { Use(nil) })
	for _, c := range []struct {
		err   error
		reads int
	}{
		{fmt.Errorf("system: %w", ErrDenied), 1},
		{errors.New("mygo: secrets: keychain: read: The user name or passphrase you entered is not correct. (OSStatus -25293)"), 1},
		{errors.New("mygo: secrets: keychain: read: User canceled the operation. (OSStatus -128)"), 1},
		{errors.New("Interaction is not allowed with the Security Server."), 2},
	} {
		f := &failingStore{err: c.err}
		Use(f)
		for range 2 {
			if _, err := Get("key"); err == nil {
				t.Fatalf("%v: no error", c.err)
			}
		}
		if f.reads != c.reads {
			t.Errorf("%v: read the store %d times, want %d", c.err, f.reads, c.reads)
		}
	}
}
