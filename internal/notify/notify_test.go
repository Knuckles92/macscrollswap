package notify

import "testing"

func TestSanitizeEscapesAndTruncates(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`hello "world"`: `hello \"world\"`,
		"back\\slash":   `back\\slash`,
		"new\nline":     "new line",
	}
	for in, want := range cases {
		got := sanitize(in)
		if got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'x'
	}
	out := sanitize(string(long))
	if len(out) <= 200 {
		t.Errorf("expected truncation to ~200 chars + ellipsis")
	}
}

func TestRecordingNotifierCaptures(t *testing.T) {
	t.Parallel()
	r := &RecordingNotifier{}
	_ = r.Notify("title", "body")
	_ = r.Beep()
	_ = r.Beep()
	if len(r.Calls()) != 1 {
		t.Errorf("expected 1 notify, got %d", len(r.Calls()))
	}
	if r.Beeps != 2 {
		t.Errorf("expected 2 beeps, got %d", r.Beeps)
	}
}
