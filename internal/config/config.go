// Package config holds the on-disk configuration for harborrs and the
// concrete OPMLProvider used by the UI and Reader API.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kfet/harborrs/internal/atomic"
	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/store"
)

// Config is the on-disk configuration loaded from `config.json` in the
// data dir. v0.1 is plain JSON; switching to TOML is a v0.2 conversation.
type Config struct {
	Listen string      `json:"listen"`
	Auth   auth.Config `json:"auth"`
	UI     UIConfig    `json:"ui"`
}

// UIConfig governs the web UI presentation.
type UIConfig struct {
	Theme  string `json:"theme,omitempty"`
	Secure bool   `json:"secure,omitempty"`
}

// Default returns a Config populated with sensible defaults.
func Default() Config {
	return Config{Listen: ":8088", UI: UIConfig{Theme: "light"}}
}

// Load reads the config from path. Missing → Default(), no error.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, err
	}
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("config: parse: %w", err)
	}
	if c.Listen == "" {
		c.Listen = ":8088"
	}
	if c.UI.Theme == "" {
		c.UI.Theme = "light"
	}
	return c, nil
}

// Hookable for testing.
var jsonMarshalIndent = json.MarshalIndent

// Save atomically writes the config.
func Save(path string, c Config) error {
	data, err := jsonMarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomic.WriteFileMode(path, data, 0o600)
}

// FileOPML is a concrete store.OPMLProvider that reads/writes the OPML on
// disk via the atomic helper.
type FileOPML struct {
	Path string
	mu   sync.Mutex
}

// Load reads the OPML file. Missing → empty *store.OPML.
func (f *FileOPML) Load() (*store.OPML, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, err := store.ReadOPML(f.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &store.OPML{}, nil
		}
		return nil, err
	}
	return o, nil
}

// Save writes the OPML atomically.
func (f *FileOPML) Save(o *store.OPML) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return o.WriteOPML(f.Path)
}

// NewFileOPML returns a FileOPML for a data dir.
func NewFileOPML(dataDir string) *FileOPML {
	return &FileOPML{Path: filepath.Join(dataDir, "subscriptions.opml")}
}
