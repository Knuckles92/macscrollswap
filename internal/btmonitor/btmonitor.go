// Package btmonitor reports whether a Bluetooth pointing device (mouse) is
// currently connected to this Mac.
//
// Detection is done by polling `ioreg -a -r -c IOHIDDevice` (XML plist output)
// and filtering for entries whose Transport begins with "Bluetooth" (covers
// both "Bluetooth" Classic and "Bluetooth Low Energy" / BLE) and whose
// PrimaryUsage is 2 (mouse) with PrimaryUsagePage 1 (Generic Desktop). This
// reliably catches Apple Magic Mouse, Logitech M-series, and other Bluetooth
// mice.
//
// Polling is intentional: the Bluetooth/IORegistry notification APIs require
// non-trivial cgo work and live state that is harder to get right. A 2–3s
// polling interval gives sub-second-feeling UX for connect/disconnect events
// while keeping the implementation simple and robust across macOS versions.
package btmonitor

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PrimaryUsagePageGenericDesktop is the HID usage page for generic desktop
// controls (mouse, keyboard, joystick, etc.).
const PrimaryUsagePageGenericDesktop = 0x01

// PrimaryUsageMouse is the HID usage for a mouse.
const PrimaryUsageMouse = 0x02

// DefaultInterval is the polling interval used when none is configured.
const DefaultInterval = 3 * time.Second

// monitorEntry mirrors the IOHIDDevice plist fields we care about.
type monitorEntry struct {
	Transport        string
	PrimaryUsage     int
	PrimaryUsagePage int
	Product          string
}

// Monitor polls ioreg for Bluetooth mice and notifies subscribers of state
// changes via a callback.
type Monitor struct {
	interval time.Duration
	log      *slog.Logger

	mu        sync.Mutex
	connected bool
	running   bool
	stop      chan struct{}
	done      chan struct{}

	// onChange is invoked (from the polling goroutine) whenever the
	// connected-state changes. Receives the new state.
	onChange func(connected bool)
}

// Option configures a Monitor.
type Option func(*Monitor)

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(m *Monitor) { m.log = l }
}

// WithInterval overrides the polling interval.
func WithInterval(d time.Duration) Option {
	return func(m *Monitor) {
		if d > 0 {
			m.interval = d
		}
	}
}

// OnChange sets the callback invoked when connected-state changes.
// Must be set before calling Start.
func OnChange(fn func(connected bool)) Option {
	return func(m *Monitor) { m.onChange = fn }
}

// New constructs a Monitor.
func New(opts ...Option) *Monitor {
	m := &Monitor{
		interval: DefaultInterval,
		log:      slog.Default(),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// IsMouseConnected reports the current cached state without polling.
func (m *Monitor) IsMouseConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// Start begins polling. It immediately reports the current state to the
// onChange callback (regardless of any prior state) and then reports
// subsequent transitions. Calling Start twice returns an error.
func (m *Monitor) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("btmonitor: already running")
	}
	if m.onChange == nil {
		m.mu.Unlock()
		return errors.New("btmonitor: OnChange callback not set")
	}
	m.running = true
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	m.mu.Unlock()

	go m.loop()
	return nil
}

// Stop halts polling and blocks until the poll goroutine has exited.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	close(m.stop)
	m.running = false
	m.mu.Unlock()
	<-m.done
}

func (m *Monitor) loop() {
	defer close(m.done)

	// Initial check.
	current, err := PollMouseConnected(context.Background())
	if err != nil {
		m.log.Warn("btmonitor: initial poll failed", "err", err)
		current = false
	}
	m.mu.Lock()
	m.connected = current
	cb := m.onChange
	m.mu.Unlock()
	cb(current)

	t := time.NewTicker(m.interval)
	defer t.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			next, err := PollMouseConnected(context.Background())
			if err != nil {
				m.log.Warn("btmonitor: poll failed", "err", err)
				continue
			}
			m.mu.Lock()
			prev := m.connected
			m.connected = next
			cb := m.onChange
			m.mu.Unlock()
			if prev != next {
				cb(next)
			}
		}
	}
}

// PollMouseConnected shells out to ioreg and returns true if at least one
// Bluetooth HID device with mouse primary usage is currently connected.
func PollMouseConnected(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "ioreg", "-a", "-r", "-c", "IOHIDDevice")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("ioreg: %w", err)
	}
	return parseBluetoothMouse(out)
}

func parseBluetoothMouse(plistData []byte) (bool, error) {
	if len(plistData) == 0 {
		return false, nil
	}
	entries, err := parseTopDicts(plistData)
	if err != nil {
		return false, fmt.Errorf("parse ioreg XML: %w", err)
	}
	for _, e := range entries {
		if isBluetoothMouse(e) {
			return true, nil
		}
	}
	return false, nil
}

func isBluetoothMouse(e monitorEntry) bool {
	if !isBluetoothTransport(e.Transport) {
		return false
	}
	// Mice: PrimaryUsagePage=1 (Generic Desktop), PrimaryUsage=2 (Mouse).
	return e.PrimaryUsagePage == PrimaryUsagePageGenericDesktop && e.PrimaryUsage == PrimaryUsageMouse
}

// isBluetoothTransport reports whether the device's Transport property
// identifies it as a Bluetooth device. macOS reports both "Bluetooth" (Classic)
// and "Bluetooth Low Energy" (BLE) for Bluetooth peripherals, so we match on
// the "bluetooth" prefix (case-insensitive). This deliberately excludes USB,
// "FIFO", "Audio", etc.
func isBluetoothTransport(transport string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(transport)), "bluetooth")
}

// parseTopDicts walks an ioreg-style XML plist and returns one entry per
// top-level <dict> in the outer <array>, picking out only the keys we care
// about. Nested <dict> and <array> values (e.g. Elements, DeviceUsagePairs,
// the base64 <data> ReportDescriptor) are skipped entirely.
//
// We hand-roll this instead of using howett.net/plist because that library
// silently drops top-level dicts whose values include certain <data> blocks
// (observed with a Logitech M240's ReportDescriptor), causing us to miss
// real connected Bluetooth mice. encoding/xml handles <data> cleanly.
func parseTopDicts(data []byte) ([]monitorEntry, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []monitorEntry
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "dict" {
			continue
		}
		e, err := readDict(dec)
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
}

// readDict assumes the opening <dict> tag has already been consumed and reads
// until the matching </dict>, returning a flat monitorEntry.
func readDict(dec *xml.Decoder) (monitorEntry, error) {
	var e monitorEntry
	var pendingKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			return e, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return e, err
				}
				pendingKey = s
			case "dict", "array":
				// Skip nested structures (Elements, DeviceUsagePairs, etc.)
				if err := skipElement(dec); err != nil {
					return e, err
				}
				pendingKey = ""
			default:
				// Simple value: string, integer, true/false, data, real, etc.
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return e, err
				}
				assign(&e, pendingKey, s)
				pendingKey = ""
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return e, nil
			}
		}
	}
}

// skipElement consumes tokens until the currently-open element is closed.
// Called after the opening tag has already been read.
func skipElement(dec *xml.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			_ = t
			depth++
		case xml.EndElement:
			_ = t
			depth--
		}
	}
	return nil
}

func assign(e *monitorEntry, key, val string) {
	val = strings.TrimSpace(val)
	switch key {
	case "Product":
		e.Product = val
	case "Transport":
		e.Transport = val
	case "PrimaryUsage":
		e.PrimaryUsage = atoiSafe(val)
	case "PrimaryUsagePage":
		e.PrimaryUsagePage = atoiSafe(val)
	}
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}
