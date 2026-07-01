// Package notify provides lightweight macOS user feedback (Notification Center
// banners + system beep) used when the daemon changes state in response to a
// hotkey press or a Bluetooth connect/disconnect event.
//
// All notifications are best-effort: a failure to display a banner is logged
// but never blocks the daemon's main work.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Notifier sends user-facing notifications. The default implementation shells
// out to osascript; it can be swapped out in tests.
type Notifier interface {
	// Notify shows a banner with the given title and message.
	Notify(title, message string) error
	// Beep plays the system alert sound.
	Beep() error
}

// OSNotifier notifies via osascript on macOS. No-op when not on darwin / when
// osascript is missing.
type OSNotifier struct{}

// New returns the default OS notifier.
func New() Notifier { return &OSNotifier{} }

// Notify implements Notifier by shelling out to AppleScript.
func (OSNotifier) Notify(title, message string) error {
	title = sanitize(title)
	message = sanitize(message)
	// Using display notification (quiet, banner style) — does not steal focus.
	script := fmt.Sprintf(`display notification %q with title %q sound name "Frog"`, message, title)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

// Beep implements Notifier.
func (OSNotifier) Beep() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "osascript", "-e", `beep`).Run()
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// NoopNotifier is a Notifier that does nothing. Used in tests.
type NoopNotifier struct{}

// Notify implements Notifier.
func (NoopNotifier) Notify(string, string) error { return nil }

// Beep implements Notifier.
func (NoopNotifier) Beep() error { return nil }

// RecordingNotifier records every call for assertions in tests.
type RecordingNotifier struct {
	mu       sync.Mutex
	Notifies []NotifyCall
	Beeps    int
}

// NotifyCall is a recorded Notify invocation.
type NotifyCall struct {
	Title, Message string
}

// Notify implements Notifier.
func (r *RecordingNotifier) Notify(title, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Notifies = append(r.Notifies, NotifyCall{Title: title, Message: message})
	return nil
}

// Beep implements Notifier.
func (r *RecordingNotifier) Beep() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Beeps++
	return nil
}

// Calls returns a copy of recorded Notify calls.
func (r *RecordingNotifier) Calls() []NotifyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NotifyCall, len(r.Notifies))
	copy(out, r.Notifies)
	return out
}
