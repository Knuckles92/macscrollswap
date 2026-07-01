package daemon

import (
	"encoding/json"
	"log/slog"
	"testing"

	"macscrollswap/internal/config"
	"macscrollswap/internal/notify"
)

// fakeScroller records every Set call and returns a configurable current
// value from Get.
type fakeScroller struct {
	current bool
	sets    []bool
	err     error
}

func (f *fakeScroller) Get() (bool, error) { return f.current, nil }
func (f *fakeScroller) Set(natural bool) error {
	if f.err != nil {
		return f.err
	}
	f.sets = append(f.sets, natural)
	f.current = natural
	return nil
}

func newTestDaemon(t *testing.T, scroll Scroller, note notify.Notifier, cfg *config.Config) *Daemon {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{
			ScrollNaturalWhenConnected:    false,
			ScrollNaturalWhenDisconnected: true,
			BaselineCaptured:              true,
		}
	}
	cfg.SocketPath = "/tmp/macscrollswap-test-ignored.sock"
	// Redirect any Save() calls to a temp file so tests never mutate the
	// user's real config on disk.
	cfg.SetPathForTest(t.TempDir() + "/config.json")
	d := &Daemon{
		cfg:    cfg,
		scroll: scroll,
		notify: note,
		log:    slog.Default(),
	}
	return d
}

func TestApplyWritesWhenTargetChanges(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, nil)

	// disconnected -> whenDisconnected=true; current=false -> should Set(true)
	d.apply()
	if len(scroll.sets) != 1 || !scroll.sets[0] {
		t.Errorf("expected one Set(true), got %v", scroll.sets)
	}

	// Applying again with same target should be a no-op.
	d.apply()
	if len(scroll.sets) != 1 {
		t.Errorf("expected no extra Set on identical target, got %v", scroll.sets)
	}

	// Mark mouse connected -> whenConnected=false -> Set(false).
	d.mu.Lock()
	d.mouseConnected = true
	d.mu.Unlock()
	d.apply()
	if len(scroll.sets) != 2 || scroll.sets[1] {
		t.Errorf("expected Set(false) on connect, got %v", scroll.sets)
	}
}

func TestApplyWritesWhenSystemDrifts(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: true}
	cfg := &config.Config{
		ScrollNaturalWhenConnected:    false,
		ScrollNaturalWhenDisconnected: false,
		BaselineCaptured:              true,
	}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, cfg)

	// Daemon thinks it already applied OFF, and both targets are OFF, so the
	// in-memory shortcut would skip — but the system still has natural ON.
	d.mu.Lock()
	d.appliedKnown = true
	d.appliedNatural = false
	d.mouseConnected = true
	d.mu.Unlock()
	d.apply()
	if len(scroll.sets) != 1 || scroll.sets[0] {
		t.Errorf("expected Set(false) to repair drift, got %v", scroll.sets)
	}
}

func TestApplyNoOpWhenPaused(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, nil)

	d.mu.Lock()
	d.paused = true
	d.mouseConnected = true
	d.mu.Unlock()
	d.apply()
	if len(scroll.sets) != 0 {
		t.Errorf("paused daemon must not Set, got %v", scroll.sets)
	}
}

func TestOnMouseChangeAppliesAndNotifies(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	rec := &notify.RecordingNotifier{}
	cfg := &config.Config{
		ScrollNaturalWhenConnected:    false,
		ScrollNaturalWhenDisconnected: true,
		BaselineCaptured:              true,
	}
	d := newTestDaemon(t, scroll, rec, cfg)

	// Connect: should Set(false) and notify.
	d.onMouseChange(true)
	if len(scroll.sets) != 1 || scroll.sets[0] {
		t.Errorf("connect: expected Set(false), got %v", scroll.sets)
	}
	calls := rec.Calls()
	if len(calls) == 0 {
		t.Errorf("connect: expected a notification")
	}

	// Disconnect: should Set(true) and notify.
	d.onMouseChange(false)
	if len(scroll.sets) != 2 || !scroll.sets[1] {
		t.Errorf("disconnect: expected Set(true), got %v", scroll.sets)
	}
}

func TestOnMouseChangePausedDoesNotApplyButNotifies(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	rec := &notify.RecordingNotifier{}
	d := newTestDaemon(t, scroll, rec, nil)

	d.mu.Lock()
	d.paused = true
	d.mu.Unlock()
	d.onMouseChange(true)
	if len(scroll.sets) != 0 {
		t.Errorf("paused: expected no Set, got %v", scroll.sets)
	}
	calls := rec.Calls()
	if len(calls) == 0 {
		t.Errorf("paused: expected a '(paused)' notification")
	}
}

func TestHotkeyPauseToggleFlipsPaused(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	rec := &notify.RecordingNotifier{}
	d := newTestDaemon(t, scroll, rec, nil)
	if d.handleStatus().Paused {
		t.Fatalf("initial state should not be paused")
	}
	d.onHotkeyPauseToggle()
	if !d.handleStatus().Paused {
		t.Errorf("after toggle, expected paused")
	}
	if rec.Beeps != 1 {
		t.Errorf("expected one beep on pause toggle, got %d", rec.Beeps)
	}
	// Toggling back to running should resume + apply.
	d.onHotkeyPauseToggle()
	if d.handleStatus().Paused {
		t.Errorf("expected resumed")
	}
}

func TestHotkeyDirectionSwapSwapsValues(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	rec := &notify.RecordingNotifier{}
	cfg := &config.Config{
		ScrollNaturalWhenConnected:    false,
		ScrollNaturalWhenDisconnected: true,
		BaselineCaptured:              true,
	}
	// Point configPath at temp file so Save() doesn't touch the real config.
	cfg.SocketPath = "/tmp/macscrollswap-test-ignored.sock"
	d := newTestDaemon(t, scroll, rec, cfg)

	d.onHotkeyDirectionSwap()
	if cfg.ScrollNaturalWhenConnected != true || cfg.ScrollNaturalWhenDisconnected != false {
		t.Errorf("after swap: connected=%v disconnected=%v",
			cfg.ScrollNaturalWhenConnected, cfg.ScrollNaturalWhenDisconnected)
	}
	if rec.Beeps != 1 {
		t.Errorf("expected one beep, got %d", rec.Beeps)
	}
}

func TestRPCStatusReflectsState(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, nil)
	d.mu.Lock()
	d.mouseConnected = true
	d.appliedKnown = true
	d.appliedNatural = false
	d.mu.Unlock()
	st := d.handleStatus()
	if !st.MouseConnected {
		t.Errorf("expected mouse connected")
	}
	if st.CurrentNaturalScroll == nil || *st.CurrentNaturalScroll {
		t.Errorf("expected current natural=false")
	}
}

func TestRPCSetDirectionPersistsAndApplies(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	cfg := &config.Config{
		ScrollNaturalWhenConnected:    false,
		ScrollNaturalWhenDisconnected: true,
		BaselineCaptured:              true,
	}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, cfg)

	params, _ := json.Marshal(struct {
		Value bool `json:"value"`
	}{Value: true})
	res, err := d.handle(ctlsockMethodSetConnected, params)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	dr, ok := res.(DirectionResponse)
	if !ok {
		t.Fatalf("expected DirectionResponse, got %T", res)
	}
	if !dr.WhenConnected {
		t.Errorf("expected when-connected=true")
	}
	if cfg.ScrollNaturalWhenConnected != true {
		t.Errorf("config not updated")
	}
}

func TestRPCUnknownMethodErrors(t *testing.T) {
	t.Parallel()
	scroll := &fakeScroller{current: false}
	d := newTestDaemon(t, scroll, notify.NoopNotifier{}, nil)
	if _, err := d.handle("Banana", nil); err == nil {
		t.Errorf("expected error on unknown method")
	}
}

func TestOnOffHelper(t *testing.T) {
	t.Parallel()
	if onOff(true) != "ON" {
		t.Errorf("onOff(true) wrong")
	}
	if onOff(false) != "OFF" {
		t.Errorf("onOff(false) wrong")
	}
}

// Reuse the constant from the ctlsock package by importing the method name as
// a local string so the test file does not need to import ctlsock.
const ctlsockMethodSetConnected = "SetConnected"
