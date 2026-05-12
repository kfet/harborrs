// Package atomic provides atomic file write helpers built on tmp-file + fsync +
// rename, the only primitive stdlib gives us for crash-safe writes.
package atomic

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Hookable file operations — overridden in tests to exercise error paths that
// can't be reliably triggered by real OS calls (chmod / fsync / close on a
// freshly-created temp file we still own).
var (
	fileChmod = (*os.File).Chmod
	fileSync  = (*os.File).Sync
	fileClose = (*os.File).Close
)

// WriteFile writes data to path atomically. Permissions are 0o644.
func WriteFile(path string, data []byte) error {
	return WriteFileMode(path, data, 0o644)
}

// WriteFileMode is WriteFile with an explicit mode.
func WriteFileMode(path string, data []byte, mode os.FileMode) error {
	return writeAtomic(path, bytes.NewReader(data), mode)
}

// WriteFileFrom copies from r to path atomically with mode 0o644.
func WriteFileFrom(path string, r io.Reader) error {
	return writeAtomic(path, r, 0o644)
}

func writeAtomic(path string, r io.Reader, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomic: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomic: create tmp: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(err error, op string) error {
		_ = fileClose(tmp)
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic: %s: %w", op, err)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		return fail(err, "write tmp")
	}
	if err := fileChmod(tmp, mode); err != nil {
		return fail(err, "chmod tmp")
	}
	if err := fileSync(tmp); err != nil {
		return fail(err, "fsync tmp")
	}
	if err := fileClose(tmp); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic: close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic: rename: %w", err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("atomic: open dir: %w", err)
	}
	defer d.Close()
	// Some filesystems (e.g. tmpfs, some macOS configs) don't support dir
	// fsync; treat that as best-effort.
	_ = d.Sync()
	return nil
}
