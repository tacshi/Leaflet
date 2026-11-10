package keychain

import (
	"errors"
	"fmt"
	"testing"
)

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

func TestNoStore(t *testing.T) {
	Use(nil)
	if _, err := Get("key"); err == nil {
		t.Error("read a secret from no store")
	}
	if err := Set("key", nil); err == nil {
		t.Error("kept a secret in no store")
	}
}

type failingStore struct {
	memStore
	err error
}

func (f *failingStore) Get(string) ([]byte, error) {
	f.reads++
	return nil, f.err
}

func TestOnlyRefusalsKept(t *testing.T) {
	t.Cleanup(func() { Use(nil) })
	for _, c := range []struct {
		err   error
		reads int
	}{
		{fmt.Errorf("system: %w", ErrDenied), 1},
		{Denied(errors.New("mygo: secrets: keychain: read: The user name or passphrase you entered is not correct. (OSStatus -25293)")), 1},
		{Denied(errors.New("mygo: secrets: keychain: read: User canceled the operation. (OSStatus -128)")), 1},
		{Denied(errors.New("Interaction is not allowed with the Security Server. (OSStatus -25308)")), 2},
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

func TestDenied(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		denied bool
	}{
		{errors.New("read: The user name or passphrase you entered is not correct. (OSStatus -25293)"), -25293, true},
		{errors.New("read: User canceled the operation. (OSStatus -128)"), -128, true},
		{errors.New("read: The specified item could not be found in the keychain. (OSStatus -25300)"), -25300, false},
		{errors.New("read: (OSStatus x)"), 0, false},
		{errors.New("read: failed"), 0, false},
	} {
		status, _ := OSStatus(c.err)
		got := Denied(c.err)
		if status != c.status || errors.Is(got, ErrDenied) != c.denied || !errors.Is(got, c.err) {
			t.Errorf("%v: status %d, Denied %v", c.err, status, got)
		}
	}
	if Denied(nil) != nil {
		t.Error("no error became one")
	}
}
