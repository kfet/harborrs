package passkey

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/pinopass"
)

func mkCred(id, pub byte, label string) Credential {
	return Credential{
		Credential: pinopass.Credential{ID: []byte{id}, PublicKey: []byte{pub}, SignCount: 0},
		Label:      label,
		AddedAt:    time.Unix(0, 0).UTC(),
	}
}

func TestOpenCredentialStoreMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, err := OpenCredentialStore(p)
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("want empty, got %d", s.Len())
	}
}

func TestOpenCredentialStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	if err := s.Add(mkCred(1, 10, "laptop")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Add(mkCred(2, 20, "phone")); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Reload from disk.
	s2, err := OpenCredentialStore(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Len() != 2 {
		t.Fatalf("want 2, got %d", s2.Len())
	}
	// Newest first.
	got := s2.List()
	if got[0].Label != "phone" || got[1].Label != "laptop" {
		t.Fatalf("order: %q,%q", got[0].Label, got[1].Label)
	}
	// List returns a copy.
	got[0].Label = "mutated"
	if s2.List()[0].Label != "phone" {
		t.Fatal("List did not return a copy")
	}
}

func TestOpenCredentialStoreCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCredentialStore(p); err == nil {
		t.Fatal("want error for corrupt json")
	}
}

func TestOpenCredentialStoreReadError(t *testing.T) {
	// A directory path makes os.ReadFile fail with a non-NotExist error.
	dir := t.TempDir()
	if _, err := OpenCredentialStore(dir); err == nil {
		t.Fatal("want error reading a directory as a file")
	}
}

func TestAddDuplicate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	if err := s.Add(mkCred(1, 10, "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(mkCred(1, 99, "dup")); err == nil {
		t.Fatal("want duplicate error")
	}
}

func TestGetByID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	_ = s.Add(mkCred(7, 70, "x"))
	c, ok := s.GetByID([]byte{7})
	if !ok || c.Label != "x" {
		t.Fatalf("get hit: %v %q", ok, c.Label)
	}
	if _, ok := s.GetByID([]byte{99}); ok {
		t.Fatal("get miss should be false")
	}
}

func TestUpdateSignCount(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	_ = s.Add(mkCred(3, 30, "x"))
	if err := s.UpdateSignCount([]byte{3}, 42); err != nil {
		t.Fatalf("update: %v", err)
	}
	c, _ := s.GetByID([]byte{3})
	if c.SignCount != 42 {
		t.Fatalf("want 42, got %d", c.SignCount)
	}
	if err := s.UpdateSignCount([]byte{99}, 1); err == nil {
		t.Fatal("want not-found error")
	}
}

func TestDelete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	_ = s.Add(mkCred(1, 10, "a"))
	_ = s.Add(mkCred(2, 20, "b"))
	if err := s.Delete([]byte{1}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("want 1 left, got %d", s.Len())
	}
	if _, ok := s.GetByID([]byte{1}); ok {
		t.Fatal("deleted cred still present")
	}
	if err := s.Delete([]byte{99}); err == nil {
		t.Fatal("want not-found error")
	}
}

// persistError points the store at a path whose parent cannot be created
// (a regular file stands where a directory is needed), so persistLocked's
// MkdirAll fails.
func badPathStore(t *testing.T) *CredentialStore {
	t.Helper()
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &CredentialStore{Path: filepath.Join(f, "sub", "credentials.json")}
}

func TestAddPersistError(t *testing.T) {
	s := badPathStore(t)
	if err := s.Add(mkCred(1, 10, "a")); err == nil {
		t.Fatal("want persist error")
	}
}

func TestUpdateAndDeletePersistError(t *testing.T) {
	// Add cleanly first, then swap to a bad path so the write-back fails.
	p := filepath.Join(t.TempDir(), "credentials.json")
	s, _ := OpenCredentialStore(p)
	_ = s.Add(mkCred(1, 10, "a"))

	bad := badPathStore(t)
	s.Path = bad.Path
	if err := s.UpdateSignCount([]byte{1}, 5); err == nil {
		t.Fatal("want update persist error")
	}
	if err := s.Delete([]byte{1}); err == nil {
		t.Fatal("want delete persist error")
	}
}
