package scroller

import "testing"

func TestParseBoolish(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
		err  bool
	}{
		{"1", true, false},
		{"0", false, false},
		{"true", true, false},
		{"false", false, false},
		{"YES", true, false},
		{"NO", false, false},
		{"  1  ", true, false},
		{"2", true, false},
		{"", false, true},
		{"banana", false, true},
	}
	for _, tc := range cases {
		got, err := parseBoolish(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseBoolish(%q): expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBoolish(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBoolish(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
