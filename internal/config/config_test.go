package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in  time.Duration
		out time.Duration
	}{
		{3 * time.Second, 3 * time.Second},
		{1500 * time.Millisecond, 1500 * time.Millisecond},
	}
	for _, tc := range cases {
		d := Duration(tc.in)
		b, err := json.Marshal(&d)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got Duration
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if time.Duration(got) != tc.out {
			t.Errorf("got %s, want %s", time.Duration(got), tc.out)
		}
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	t.Parallel()
	// Path() depends on UserConfigDir; we just exercise Default() and check
	// the default fields are populated as expected.
	cfg := Default()
	if cfg.ScrollNaturalWhenConnected {
		t.Errorf("default ScrollNaturalWhenConnected should be false")
	}
	if !cfg.ScrollNaturalWhenDisconnected {
		t.Errorf("default ScrollNaturalWhenDisconnected should be true")
	}
	if cfg.HotkeyPause == "" || cfg.HotkeyDirection == "" {
		t.Errorf("hotkeys must default to non-empty strings")
	}
	if cfg.PollInterval == 0 {
		t.Errorf("poll interval must be non-zero")
	}
}
