// Package hotkey wraps golang.design/x/hotkey to provide a small manager that
// parses key-combo strings (e.g. "ctrl+opt+cmd+s") and dispatches callbacks.
//
// On macOS, hotkey events are only delivered while an NSApplication event
// loop is running on the main OS thread. Callers must therefore launch the
// daemon via the mainthread package (see cmd/macscrollswap/main.go).
package hotkey

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"golang.design/x/hotkey"
)

// Manager tracks a set of registered global hotkeys.
type Manager struct {
	log *slog.Logger
	mu  sync.Mutex
	hks []*hotkey.Hotkey
}

// NewManager constructs a Manager.
func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{log: log}
}

// Register parses a key combo (e.g. "ctrl+opt+cmd+s"), registers it, and
// invokes fn on every keydown.
func (m *Manager) Register(combo string, fn func()) error {
	mods, key, err := Parse(combo)
	if err != nil {
		return err
	}
	hk := hotkey.New(mods, key)
	if err := hk.Register(); err != nil {
		return fmt.Errorf("register %s: %w", combo, err)
	}
	m.mu.Lock()
	m.hks = append(m.hks, hk)
	m.mu.Unlock()

	go func() {
		for range hk.Keydown() {
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.log.Error("hotkey callback panic", "combo", combo, "panic", r)
					}
				}()
				fn()
			}()
		}
	}()
	return nil
}

// UnregisterAll unregisters every previously registered hotkey.
func (m *Manager) UnregisterAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, hk := range m.hks {
		_ = hk.Unregister()
	}
	m.hks = nil
}

// Parse converts a combo string like "ctrl+opt+cmd+s" into a slice of
// modifiers and a key. At least one modifier is required.
func Parse(combo string) ([]hotkey.Modifier, hotkey.Key, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	if len(parts) < 2 {
		return nil, 0, fmt.Errorf("combo %q must contain at least one modifier and a key", combo)
	}
	var mods []hotkey.Modifier
	for _, p := range parts[:len(parts)-1] {
		switch strings.TrimSpace(p) {
		case "ctrl", "control":
			mods = append(mods, hotkey.ModCtrl)
		case "shift":
			mods = append(mods, hotkey.ModShift)
		case "opt", "option", "alt":
			mods = append(mods, hotkey.ModOption)
		case "cmd", "command":
			mods = append(mods, hotkey.ModCmd)
		default:
			return nil, 0, fmt.Errorf("unknown modifier %q in combo %q", p, combo)
		}
	}
	key, err := parseKey(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", combo, err)
	}
	return mods, key, nil
}

func parseKey(s string) (hotkey.Key, error) {
	switch s {
	case "a":
		return hotkey.KeyA, nil
	case "b":
		return hotkey.KeyB, nil
	case "c":
		return hotkey.KeyC, nil
	case "d":
		return hotkey.KeyD, nil
	case "e":
		return hotkey.KeyE, nil
	case "f":
		return hotkey.KeyF, nil
	case "g":
		return hotkey.KeyG, nil
	case "h":
		return hotkey.KeyH, nil
	case "i":
		return hotkey.KeyI, nil
	case "j":
		return hotkey.KeyJ, nil
	case "k":
		return hotkey.KeyK, nil
	case "l":
		return hotkey.KeyL, nil
	case "m":
		return hotkey.KeyM, nil
	case "n":
		return hotkey.KeyN, nil
	case "o":
		return hotkey.KeyO, nil
	case "p":
		return hotkey.KeyP, nil
	case "q":
		return hotkey.KeyQ, nil
	case "r":
		return hotkey.KeyR, nil
	case "s":
		return hotkey.KeyS, nil
	case "t":
		return hotkey.KeyT, nil
	case "u":
		return hotkey.KeyU, nil
	case "v":
		return hotkey.KeyV, nil
	case "w":
		return hotkey.KeyW, nil
	case "x":
		return hotkey.KeyX, nil
	case "y":
		return hotkey.KeyY, nil
	case "z":
		return hotkey.KeyZ, nil
	case "0":
		return hotkey.Key0, nil
	case "1":
		return hotkey.Key1, nil
	case "2":
		return hotkey.Key2, nil
	case "3":
		return hotkey.Key3, nil
	case "4":
		return hotkey.Key4, nil
	case "5":
		return hotkey.Key5, nil
	case "6":
		return hotkey.Key6, nil
	case "7":
		return hotkey.Key7, nil
	case "8":
		return hotkey.Key8, nil
	case "9":
		return hotkey.Key9, nil
	case "space":
		return hotkey.KeySpace, nil
	case "esc", "escape":
		return hotkey.KeyEscape, nil
	case "return", "enter":
		return hotkey.KeyReturn, nil
	case "tab":
		return hotkey.KeyTab, nil
	case "left":
		return hotkey.KeyLeft, nil
	case "right":
		return hotkey.KeyRight, nil
	case "up":
		return hotkey.KeyUp, nil
	case "down":
		return hotkey.KeyDown, nil
	case "f1":
		return hotkey.KeyF1, nil
	case "f2":
		return hotkey.KeyF2, nil
	case "f3":
		return hotkey.KeyF3, nil
	case "f4":
		return hotkey.KeyF4, nil
	case "f5":
		return hotkey.KeyF5, nil
	}
	return 0, fmt.Errorf("unsupported key %q (supported: a-z, 0-9, space, esc, return, tab, arrows, f1-f5)", s)
}
