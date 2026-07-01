// Package daemon contains the long-running macscrollswap daemon: it watches
// for Bluetooth mouse connect/disconnect events, applies the configured
// natural-scrolling value for the current device state, exposes a control
// RPC for the CLI, registers global hotkeys, and emits user-facing
// notifications on state transitions.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"macscrollswap/internal/btmonitor"
	"macscrollswap/internal/config"
	"macscrollswap/internal/ctlsock"
	"macscrollswap/internal/hotkey"
	"macscrollswap/internal/notify"
	"macscrollswap/internal/scroller"
)

// AppName is the human-friendly name used in notifications.
const AppName = "macscrollswap"

// Scroller is the subset of scroller.Scroller used by the daemon. Extracted
// as an interface so the state machine can be unit-tested with a fake.
type Scroller interface {
	Get() (bool, error)
	Set(natural bool) error
}

// Daemon coordinates the monitor, scroller, control socket, and hotkeys.
type Daemon struct {
	cfg     *config.Config
	scroll  Scroller
	monitor *btmonitor.Monitor
	notify  notify.Notifier
	log     *slog.Logger

	server *ctlsock.Server

	mu             sync.Mutex
	paused         bool
	mouseConnected bool
	appliedNatural bool
	appliedKnown   bool // whether appliedNatural has been initialized

	shutdownCh chan struct{}
}

// Option configures a Daemon.
type Option func(*Daemon)

// WithLogger overrides the default logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Daemon) { d.log = l }
}

// WithNotifier overrides the default notifier.
func WithNotifier(n notify.Notifier) Option {
	return func(d *Daemon) { d.notify = n }
}

// WithScroller overrides the default scroller. Intended for tests.
func WithScroller(s Scroller) Option {
	return func(d *Daemon) { d.scroll = s }
}

// New constructs a Daemon from the given config. It captures the user's
// baseline natural-scroll setting on first launch and persists it.
func New(cfg *config.Config, opts ...Option) (*Daemon, error) {
	scroll := scroller.New()

	// Capture baseline on first launch.
	if !cfg.BaselineCaptured {
		current, err := scroll.Get()
		if err != nil {
			return nil, fmt.Errorf("capture baseline: %w", err)
		}
		cfg.ScrollNaturalWhenDisconnected = current
		cfg.BaselineCaptured = true
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("save baseline config: %w", err)
		}
	}

	// If both targets ended up identical (e.g. from repeated direction swaps),
	// restore a useful default: keep the connected target and set disconnected
	// to the user's current system preference.
	if cfg.ScrollNaturalWhenConnected == cfg.ScrollNaturalWhenDisconnected {
		current, err := scroll.Get()
		if err != nil {
			return nil, fmt.Errorf("repair identical direction targets: %w", err)
		}
		cfg.ScrollNaturalWhenDisconnected = current
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("save repaired config: %w", err)
		}
	}

	d := &Daemon{
		cfg:        cfg,
		scroll:     scroll,
		notify:     notify.New(),
		log:        slog.Default(),
		shutdownCh: make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(d)
	}

	monitorInterval := time.Duration(cfg.PollInterval)
	d.monitor = btmonitor.New(
		btmonitor.WithLogger(d.log),
		btmonitor.WithInterval(monitorInterval),
		btmonitor.OnChange(d.onMouseChange),
	)

	d.server = ctlsock.NewServer(cfg.SocketPath, ctlsock.HandlerFunc(d.handle), d.log)
	return d, nil
}

// Run starts the daemon and blocks until ctx is canceled or SIGINT/SIGTERM is
// received.
func (d *Daemon) Run(ctx context.Context) error {
	d.log.Info("daemon starting",
		"socket", d.cfg.SocketPath,
		"poll_interval", time.Duration(d.cfg.PollInterval).String(),
		"when_connected", d.cfg.ScrollNaturalWhenConnected,
		"when_disconnected", d.cfg.ScrollNaturalWhenDisconnected,
	)

	if err := d.monitor.Start(); err != nil {
		return fmt.Errorf("start btmonitor: %w", err)
	}
	defer d.monitor.Stop()

	// Best-effort hotkey registration. Failure to register a hotkey should not
	// bring down the daemon; it's reported to the user via notification.
	hk := hotkey.NewManager(d.log)
	if err := hk.Register(d.cfg.HotkeyPause, d.onHotkeyPauseToggle); err != nil {
		d.log.Warn("register pause hotkey", "combo", d.cfg.HotkeyPause, "err", err)
		_ = d.notify.Notify(AppName, "Failed to register pause hotkey: "+err.Error())
	} else {
		d.log.Info("pause hotkey registered", "combo", d.cfg.HotkeyPause)
	}
	if err := hk.Register(d.cfg.HotkeyDirection, d.onHotkeyDirectionSwap); err != nil {
		d.log.Warn("register direction hotkey", "combo", d.cfg.HotkeyDirection, "err", err)
		_ = d.notify.Notify(AppName, "Failed to register direction hotkey: "+err.Error())
	} else {
		d.log.Info("direction hotkey registered", "combo", d.cfg.HotkeyDirection)
	}
	defer hk.UnregisterAll()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- d.server.ListenAndServe()
	}()

	// Initial apply so the very first state matches our config (rather than
	// waiting for the monitor's first transition).
	d.apply()

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	select {
	case <-ctx.Done():
		d.log.Info("daemon shutting down")
	case <-d.shutdownCh:
		d.log.Info("daemon shutting down (requested via RPC)")
	case err := <-serverErr:
		if err != nil {
			d.log.Error("control socket error", "err", err)
			cancel()
		}
	}

	_ = d.server.Close()
	return nil
}

// onMouseChange is called by the btmonitor on connect/disconnect transitions.
func (d *Daemon) onMouseChange(connected bool) {
	d.mu.Lock()
	prev := d.mouseConnected
	d.mouseConnected = connected
	paused := d.paused
	d.mu.Unlock()

	if prev == connected {
		return
	}

	d.log.Info("bluetooth mouse state changed", "connected", connected)
	if paused {
		// While paused, we do not change scroll behavior but still notify so
		// the user knows why nothing happened.
		if connected {
			_ = d.notify.Notify(AppName, "Bluetooth mouse connected (paused)")
		} else {
			_ = d.notify.Notify(AppName, "Bluetooth mouse disconnected (paused)")
		}
		return
	}

	d.apply()
	if connected {
		_ = d.notify.Notify(AppName, fmt.Sprintf("Bluetooth mouse connected — natural scrolling %s", onOff(d.cfg.ScrollNaturalWhenConnected)))
	} else {
		_ = d.notify.Notify(AppName, fmt.Sprintf("Bluetooth mouse disconnected — natural scrolling %s", onOff(d.cfg.ScrollNaturalWhenDisconnected)))
	}
}

// apply sets the scroll direction to match the current device state, unless
// the daemon is paused.
func (d *Daemon) apply() {
	d.mu.Lock()
	paused := d.paused
	mouseConnected := d.mouseConnected
	whenConnected := d.cfg.ScrollNaturalWhenConnected
	whenDisconnected := d.cfg.ScrollNaturalWhenDisconnected
	appliedKnown := d.appliedKnown
	applied := d.appliedNatural
	d.mu.Unlock()

	if paused {
		return
	}

	target := whenDisconnected
	if mouseConnected {
		target = whenConnected
	}
	if appliedKnown && applied == target {
		// Verify the on-disk value still matches. The user or another process
		// may have changed it since we last wrote, and we also need to write
		// when connect/disconnect targets happen to be equal.
		current, err := d.scroll.Get()
		if err == nil && current == target {
			return
		}
		if err != nil {
			d.log.Warn("verify scroll setting before skip", "err", err)
		}
	}

	if err := d.scroll.Set(target); err != nil {
		d.log.Error("apply scroll setting", "target", target, "err", err)
		_ = d.notify.Notify(AppName, "Failed to set natural scrolling: "+err.Error())
		return
	}

	d.mu.Lock()
	d.appliedNatural = target
	d.appliedKnown = true
	d.mu.Unlock()
	d.log.Info("applied natural scrolling", "value", target, "mouse_connected", mouseConnected)
}

// onHotkeyPauseToggle flips the paused state.
func (d *Daemon) onHotkeyPauseToggle() {
	d.mu.Lock()
	d.paused = !d.paused
	paused := d.paused
	d.mu.Unlock()
	_ = d.notify.Beep()
	if paused {
		_ = d.notify.Notify(AppName, "Paused — scroll direction will not change")
		d.log.Info("paused")
	} else {
		d.log.Info("resumed")
		_ = d.notify.Notify(AppName, "Resumed")
		d.apply()
	}
}

// onHotkeyDirectionSwap swaps the connected/disconnected target values and
// applies immediately.
func (d *Daemon) onHotkeyDirectionSwap() {
	d.mu.Lock()
	d.cfg.ScrollNaturalWhenConnected, d.cfg.ScrollNaturalWhenDisconnected =
		d.cfg.ScrollNaturalWhenDisconnected, d.cfg.ScrollNaturalWhenConnected
	wc := d.cfg.ScrollNaturalWhenConnected
	wd := d.cfg.ScrollNaturalWhenDisconnected
	d.mu.Unlock()
	// Reset appliedKnown so apply() always writes through once after a swap,
	// even if the resulting target happens to equal the previous one.
	d.mu.Lock()
	d.appliedKnown = false
	d.mu.Unlock()
	if err := d.cfg.Save(); err != nil {
		d.log.Error("save config after swap", "err", err)
	}
	_ = d.notify.Beep()
	_ = d.notify.Notify(AppName,
		fmt.Sprintf("Direction swapped — connected: %s / disconnected: %s", onOff(wc), onOff(wd)))
	d.log.Info("direction swapped", "when_connected", wc, "when_disconnected", wd)
	d.apply()
}

// RPC handler dispatch.
func (d *Daemon) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case ctlsock.MethodStatus:
		return d.handleStatus(), nil
	case ctlsock.MethodPause:
		return d.handleSetPaused(true), nil
	case ctlsock.MethodResume:
		return d.handleSetPaused(false), nil
	case ctlsock.MethodGetDirection:
		return d.handleGetDirection(), nil
	case ctlsock.MethodSetConnected:
		return d.handleSetTarget(params, true)
	case ctlsock.MethodSetDisconnect:
		return d.handleSetTarget(params, false)
	case ctlsock.MethodSwapDirection:
		d.onHotkeyDirectionSwap()
		return d.handleGetDirection(), nil
	case ctlsock.MethodShutdown:
		return d.handleShutdown(), nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

// ShutdownResponse is returned by MethodShutdown.
type ShutdownResponse struct {
	ShuttingDown bool `json:"shutting_down"`
}

func (d *Daemon) handleShutdown() ShutdownResponse {
	select {
	case d.shutdownCh <- struct{}{}:
	default:
	}
	return ShutdownResponse{ShuttingDown: true}
}

// StatusResponse is returned by MethodStatus.
type StatusResponse struct {
	Running              bool   `json:"running"`
	Paused               bool   `json:"paused"`
	MouseConnected       bool   `json:"mouse_connected"`
	CurrentNaturalScroll *bool  `json:"current_natural_scroll,omitempty"`
	WhenConnected        bool   `json:"scroll_natural_when_connected"`
	WhenDisconnected     bool   `json:"scroll_natural_when_disconnected"`
	Socket               string `json:"socket"`
	Version              string `json:"version"`
}

func (d *Daemon) handleStatus() StatusResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	var applied *bool
	if current, err := d.scroll.Get(); err == nil {
		applied = &current
	} else if d.appliedKnown {
		v := d.appliedNatural
		applied = &v
	}
	return StatusResponse{
		Running:              true,
		Paused:               d.paused,
		MouseConnected:       d.mouseConnected,
		CurrentNaturalScroll: applied,
		WhenConnected:        d.cfg.ScrollNaturalWhenConnected,
		WhenDisconnected:     d.cfg.ScrollNaturalWhenDisconnected,
		Socket:               d.cfg.SocketPath,
		Version:              Version,
	}
}

func (d *Daemon) handleSetPaused(p bool) StatusResponse {
	d.mu.Lock()
	d.paused = p
	d.mu.Unlock()
	_ = d.notify.Notify(AppName, map[bool]string{true: "Paused", false: "Resumed"}[p])
	if !p {
		d.apply()
	}
	return d.handleStatus()
}

func (d *Daemon) handleGetDirection() DirectionResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return DirectionResponse{
		WhenConnected:    d.cfg.ScrollNaturalWhenConnected,
		WhenDisconnected: d.cfg.ScrollNaturalWhenDisconnected,
	}
}

// DirectionResponse is returned by direction-related RPCs.
type DirectionResponse struct {
	WhenConnected    bool `json:"scroll_natural_when_connected"`
	WhenDisconnected bool `json:"scroll_natural_when_disconnected"`
}

func (d *Daemon) handleSetTarget(params json.RawMessage, connected bool) (any, error) {
	var p struct {
		Value bool `json:"value"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	d.mu.Lock()
	if connected {
		d.cfg.ScrollNaturalWhenConnected = p.Value
	} else {
		d.cfg.ScrollNaturalWhenDisconnected = p.Value
	}
	d.appliedKnown = false
	d.mu.Unlock()
	if err := d.cfg.Save(); err != nil {
		d.log.Error("save config", "err", err)
	}
	which := map[bool]string{true: "connected", false: "disconnected"}[connected]
	_ = d.notify.Notify(AppName, fmt.Sprintf("Direction updated — %s: %s", which, onOff(p.Value)))
	d.apply()
	return d.handleGetDirection(), nil
}

// AcquireLock obtains a single-instance lockfile. It returns a release func
// that must be called on shutdown.
func AcquireLock() (func(), error) {
	cfgDir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	lockPath, err := lockPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another instance of macscrollswap daemon is already running")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(lockPath)
	}, nil
}

func lockPath() (string, error) {
	cfgDir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return cfgDir + string(os.PathSeparator) + "daemon.lock", nil
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// Version is set at link time via -ldflags.
var Version = "dev"
