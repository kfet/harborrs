// Package config holds the on-disk configuration for harborrs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/kfet/harborrs/internal/atomic"
	"github.com/kfet/harborrs/internal/auth"
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
	return Config{Listen: ":8088", UI: UIConfig{Theme: "auto"}}
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
		c.UI.Theme = "auto"
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
