package hotkey

import (
	"testing"

	"golang.design/x/hotkey"
)

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		mods    []hotkey.Modifier
		key     hotkey.Key
		wantErr bool
	}{
		{"ctrl+opt+cmd+s", []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModOption, hotkey.ModCmd}, hotkey.KeyS, false},
		{"Ctrl+Opt+Cmd+S", []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModOption, hotkey.ModCmd}, hotkey.KeyS, false},
		{"shift+ctrl+d", []hotkey.Modifier{hotkey.ModShift, hotkey.ModCtrl}, hotkey.KeyD, false},
		{"command+option+0", []hotkey.Modifier{hotkey.ModCmd, hotkey.ModOption}, hotkey.Key0, false},
		{"alt+m", []hotkey.Modifier{hotkey.ModOption}, hotkey.KeyM, false},
		{"s", nil, 0, true},
		{"banana+x", nil, 0, true},
		{"ctrl+banana", nil, 0, true},
		{"ctrl+", nil, 0, true},
	}
	for _, tc := range cases {
		mods, key, err := Parse(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): want error, got mods=%v key=%v", tc.in, mods, key)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", tc.in, err)
			continue
		}
		if key != tc.key {
			t.Errorf("Parse(%q): key = %v, want %v", tc.in, key, tc.key)
		}
		if len(mods) != len(tc.mods) {
			t.Errorf("Parse(%q): %d mods, want %d", tc.in, len(mods), len(tc.mods))
			continue
		}
		for i := range mods {
			if mods[i] != tc.mods[i] {
				t.Errorf("Parse(%q): mod[%d] = %v, want %v", tc.in, i, mods[i], tc.mods[i])
			}
		}
	}
}
