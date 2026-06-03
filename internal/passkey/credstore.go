// Package passkey adds WebAuthn (passkey) login to harb's web UI on top
// of the stdlib-only github.com/kfet/pinopass verifier.
//
// It owns three things the verifier deliberately leaves to the caller:
// persistent storage of registered credentials (credentials.json in the
// data dir), short-lived challenge bookkeeping between the two halves of
// a ceremony, and the JSON shapes the browser's navigator.credentials
// API expects. Everything security-critical (challenge generation and
// response verification) is delegated to pinopass.
//
// Passkeys are an *alternative* to the password for web-UI login only.
// The password stays as the Reader-API path and the recovery path.
package passkey

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kfet/harb/internal/atomic"
	"github.com/kfet/pinopass"
)

// Credential is a registered passkey in storable form. The []byte fields
// JSON-marshal as standard base64, which round-trips cleanly. It embeds
// pinopass.Credential plus UI metadata (a human label and when it was
// added).
type Credential struct {
	pinopass.Credential
	Label   string    `json:"label"`
	AddedAt time.Time `json:"added_at"`
}

// CredentialStore is the on-disk set of registered passkeys. It is safe
// for concurrent use. The zero value is not usable; construct with
// OpenCredentialStore.
type CredentialStore struct {
	Path string

	mu    sync.RWMutex
	creds []Credential
}

// OpenCredentialStore loads (and lazily creates) a credential store at
// path. A missing file is not an error — it yields an empty store.
func OpenCredentialStore(path string) (*CredentialStore, error) {
	s := &CredentialStore{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	var disk struct {
		Credentials []Credential `json:"credentials"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, err
	}
	s.creds = disk.Credentials
	return s, nil
}

// List returns a copy of the stored credentials, newest first.
func (s *CredentialStore) List() []Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Credential, len(s.creds))
	copy(out, s.creds)
	return out
}

// Len reports how many credentials are registered.
func (s *CredentialStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.creds)
}

// getByID returns the index of the stored credential whose ID matches
// id, and whether one was found. Caller must hold at least a read lock.
func (s *CredentialStore) getByID(id []byte) (int, bool) {
	for i := range s.creds {
		if bytes.Equal(s.creds[i].ID, id) {
			return i, true
		}
	}
	return -1, false
}

// Add appends a new credential and persists the store. It rejects a
// duplicate credential ID.
func (s *CredentialStore) Add(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.getByID(c.ID); ok {
		return errors.New("passkey: credential already registered")
	}
	// Newest first.
	s.creds = append([]Credential{c}, s.creds...)
	return s.persistLocked()
}

// GetByID returns a copy of the stored credential with the given ID.
func (s *CredentialStore) GetByID(id []byte) (Credential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.getByID(id)
	if !ok {
		return Credential{}, false
	}
	return s.creds[i], true
}

// UpdateSignCount writes back the authenticator's new signature counter
// after a successful assertion. A missing ID is an error.
func (s *CredentialStore) UpdateSignCount(id []byte, count uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.getByID(id)
	if !ok {
		return errors.New("passkey: credential not found")
	}
	s.creds[i].SignCount = count
	return s.persistLocked()
}

// Delete removes the credential with the given ID and persists. A
// missing ID is an error.
func (s *CredentialStore) Delete(id []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.getByID(id)
	if !ok {
		return errors.New("passkey: credential not found")
	}
	s.creds = append(s.creds[:i], s.creds[i+1:]...)
	return s.persistLocked()
}

func (s *CredentialStore) persistLocked() error {
	type disk struct {
		Credentials []Credential `json:"credentials"`
	}
	data, err := json.MarshalIndent(disk{Credentials: s.creds}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return atomic.WriteFileMode(s.Path, data, 0o600)
}
