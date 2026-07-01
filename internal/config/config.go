// Package config handles loading, saving, and defaulting of macscrollswap's
// persistent configuration. The config is stored as JSON under the user's
// per-application support directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Key is the NSGlobalDomain user defaults key that controls natural scrolling
// on macOS.
const Key = "com.apple.swipescrolldirection"

// Config is the persisted application configuration.
type Config struct {
	// ScrollNaturalWhenConnected is the value natural scrolling should be
	// set to while a Bluetooth mouse is connected.
	ScrollNaturalWhenConnected bool `json:"scroll_natural_when_connected"`

	// ScrollNaturalWhenDisconnected is the value natural scrolling should be
	// restored to while no Bluetooth mouse is connected. This defaults to
	// whatever the user had set at first launch.
	ScrollNaturalWhenDisconnected bool `json:"scroll_natural_when_disconnected"`

	// HotkeyPause is the key combo that toggles pause. Format:
	// modifier+modifier+...+key, e.g. "ctrl+opt+cmd+s".
	HotkeyPause string `json:"hotkey_pause"`

	// HotkeyDirection is the key combo that swaps the connected/disconnected
	// target values.
	HotkeyDirection string `json:"hotkey_direction"`

	// PollInterval is how often to poll ioreg for Bluetooth mouse state.
	PollInterval Duration `json:"poll_interval"`

	// SocketPath is the unix-domain socket path used for daemon <-> CLI IPC.
	SocketPath string `json:"socket_path"`

	// LogPath is where the daemon writes its structured log.
	LogPath string `json:"log_path"`

	// BaselineCaptured records that we've snapshotted the user's existing
	// natural-scroll setting on the first run.
	BaselineCaptured bool `json:"baseline_captured"`

	configPath string
}

// Duration is a time.Duration that (un)marshals as a JSON string like "2s".
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Default returns a Config populated with sane defaults for the current user.
// The default disconnected value is whatever the user currently has set in
// NSGlobalDomain (read via the scroller), so callers should overwrite it after
// capturing the baseline if BaselineCaptured is false.
func Default() *Config {
	return &Config{
		ScrollNaturalWhenConnected:    false,
		ScrollNaturalWhenDisconnected: true,
		HotkeyPause:                   "ctrl+opt+cmd+s",
		HotkeyDirection:               "ctrl+opt+cmd+d",
		PollInterval:                  Duration(3 * time.Second),
		SocketPath:                    defaultSocketPath(),
		LogPath:                       defaultLogPath(),
	}
}

// Load reads the config from disk. If no config exists, a default config is
// returned with IsNew=true.
func Load() (*Config, bool, error) {
	path, err := Path()
	if err != nil {
		return nil, false, err
	}

	cfg := Default()
	cfg.configPath = path

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out := *cfg
		return &out, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read config %s: %w", path, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.configPath = path
	out := *cfg
	return &out, false, nil
}

// Save writes the config to disk, creating parent directories as needed.
func (c *Config) Save() error {
	if c.configPath == "" {
		path, err := Path()
		if err != nil {
			return err
		}
		c.configPath = path
	}
	if err := os.MkdirAll(filepath.Dir(c.configPath), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(c.configPath, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", c.configPath, err)
	}
	return nil
}

// SetPathForTest overrides the on-disk location this config saves to. It is
// intended only for use from tests; production callers should rely on the
// default discovered by Path().
func (c *Config) SetPathForTest(path string) {
	c.configPath = path
}

// Path returns the absolute path to the config file.
func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "macscrollswap", "config.json"), nil
}

// Dir returns the directory holding the config and runtime files.
func Dir() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

func defaultSocketPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return "/tmp/macscrollswap.sock"
	}
	return filepath.Join(base, "macscrollswap", "daemon.sock")
}

func defaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "macscrollswap.log"
	}
	return filepath.Join(home, "Library", "Logs", "macscrollswap", "daemon.log")
}
