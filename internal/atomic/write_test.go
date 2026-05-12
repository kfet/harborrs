package atomic

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b", "c.txt")
	if err := WriteFile(p, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	// overwrite
	if err := WriteFile(p, []byte("world")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(p)
	if string(got) != "world" {
		t.Fatalf("got %q", got)
	}
	// no leftover tmp files
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestWriteFileMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := WriteFileMode(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
}

func TestWriteFileFrom(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := WriteFileFrom(p, bytes.NewReader([]byte("stream"))); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "stream" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteFileMkdirFail(t *testing.T) {
	dir := t.TempDir()
	// Create a file where a directory is expected.
	conflict := filepath.Join(dir, "x")
	if err := os.WriteFile(conflict, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteFile(filepath.Join(conflict, "y", "z.txt"), []byte("v"))
	if err == nil {
		t.Fatal("expected error")
	}
	err = WriteFileFrom(filepath.Join(conflict, "y", "z.txt"), bytes.NewReader(nil))
	if err == nil {
		t.Fatal("expected error")
	}
}

// errReader yields an error after n bytes.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

func TestWriteFileFromCopyError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	want := errors.New("boom")
	err := WriteFileFrom(p, errReader{err: want})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	// no leftover
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

// Cover the rename-failure path: pre-create dest as a directory so rename over
// it fails on most filesystems.
func TestWriteFileRenameFail(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "blocker")
	if err := os.MkdirAll(filepath.Join(dest, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dest, []byte("x")); err == nil {
		t.Fatal("expected rename to fail")
	}
	if err := WriteFileFrom(dest, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected rename to fail")
	}
}

// Ensure syncDir tolerates a path that doesn't exist after the rename succeeds
// — exercised by removing the file's directory mid-flight is hard; instead
// call syncDir directly with a bogus path to cover the open-error branch.
func TestSyncDirOpenErr(t *testing.T) {
	if err := syncDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected open err")
	}
}

var _ io.Reader = errReader{}

func TestCreateTempFailure(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ro")
	if err := os.MkdirAll(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sub, 0o755)
	err := WriteFile(filepath.Join(sub, "f"), []byte("x"))
	if err == nil {
		t.Fatal("expected CreateTemp failure")
	}
}

func TestChmodFailure(t *testing.T) {
	orig := fileChmod
	defer func() { fileChmod = orig }()
	want := errors.New("chmod-boom")
	fileChmod = func(*os.File, os.FileMode) error { return want }
	dir := t.TempDir()
	err := WriteFile(filepath.Join(dir, "f"), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "chmod-boom") {
		t.Fatalf("err = %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestSyncFailure(t *testing.T) {
	orig := fileSync
	defer func() { fileSync = orig }()
	want := errors.New("sync-boom")
	fileSync = func(*os.File) error { return want }
	dir := t.TempDir()
	err := WriteFile(filepath.Join(dir, "f"), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "sync-boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloseFailure(t *testing.T) {
	orig := fileClose
	defer func() { fileClose = orig }()
	want := errors.New("close-boom")
	calls := 0
	fileClose = func(f *os.File) error {
		calls++
		if calls == 1 {
			// Real close so the fd is released, then return our error.
			_ = f.Close()
			return want
		}
		return f.Close()
	}
	dir := t.TempDir()
	err := WriteFile(filepath.Join(dir, "f"), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "close-boom") {
		t.Fatalf("err = %v", err)
	}
}
