// Package scroller reads and writes the macOS natural-scroll setting
// (com.apple.swipescrolldirection in NSGlobalDomain).
//
// Reading and writing go through CoreFoundation's CFPreferences API so we
// touch the same preference domain System Settings uses, then broadcast a
// distributed notification so running applications pick up the change without
// needing to be restarted.
package scroller

// #cgo CFLAGS: -x objective-c -fobjc-arc
// #cgo LDFLAGS: -framework CoreFoundation -framework Foundation -framework CoreGraphics
//
// #import <CoreFoundation/CoreFoundation.h>
// #import <Foundation/Foundation.h>
//
// // Undocumented CoreGraphics (WindowServer) SPI. This is what actually
// // reconfigures the live input system so a scroll-direction change takes
// // effect immediately. Merely writing the com.apple.swipescrolldirection
// // preference (even with the notification below) updates the on-disk value
// // and the System Settings UI, but does NOT change scroll behavior until the
// // next login — the CGS call is the missing piece. See snosrap/
// // DynamicScrollDirection, which drives the identical mouse-availability use
// // case.
// extern int _CGSDefaultConnection(void);
// extern void CGSSetSwipeScrollDirection(int cid, bool dir);
//
// static bool readNaturalScroll(void) {
//     CFStringRef key = CFSTR("com.apple.swipescrolldirection");
//     CFPropertyListRef value = CFPreferencesCopyAppValue(key, kCFPreferencesAnyApplication);
//     if (value == NULL) {
//         // Key absent → modern macOS default is natural scrolling ON.
//         return true;
//     }
//     bool result = true;
//     if (CFGetTypeID(value) == CFBooleanGetTypeID()) {
//         result = CFBooleanGetValue((CFBooleanRef)value);
//     }
//     CFRelease(value);
//     return result;
// }
//
// static int writeNaturalScroll(bool natural) {
//     // 1. Actually change the live scroll direction. This is the step that
//     //    makes the change take effect immediately; without it, the behavior
//     //    only updates after logout even though the preference value changes.
//     CGSSetSwipeScrollDirection(_CGSDefaultConnection(), natural);
//
//     // 2. Persist the value to the global preferences plist so it survives
//     //    reboots and is reflected by `defaults read` and other readers.
//     CFStringRef key = CFSTR("com.apple.swipescrolldirection");
//     CFBooleanRef value = natural ? kCFBooleanTrue : kCFBooleanFalse;
//     CFPreferencesSetAppValue(key, value, kCFPreferencesAnyApplication);
//     if (!CFPreferencesAppSynchronize(kCFPreferencesAnyApplication)) {
//         return -1;
//     }
//
//     // 3. Notify so the System Settings Mouse/Trackpad panes redraw their
//     //    toggle to match, if open.
//     @autoreleasepool {
//         [[NSDistributedNotificationCenter defaultCenter]
//             postNotificationName:@"SwipeScrollDirectionDidChangeNotification"
//                           object:nil
//                       userInfo:nil
//                 deliverImmediately:YES];
//     }
//     return 0;
// }
import "C"

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrInvalidValue is returned when the on-disk value cannot be parsed.
var ErrInvalidValue = errors.New("invalid value for com.apple.swipescrolldirection")

// Scroller reads and writes the natural-scroll setting.
type Scroller struct{}

// New returns a Scroller backed by CFPreferences plus a distributed
// notification on write.
func New() *Scroller { return &Scroller{} }

// Get reads the current value of com.apple.swipescrolldirection.
// Returns true if natural scrolling is currently enabled.
func (Scroller) Get() (bool, error) {
	return bool(C.readNaturalScroll()), nil
}

// Set writes the value of com.apple.swipescrolldirection and flushes/
// notifies running applications.
func (Scroller) Set(natural bool) error {
	if rc := C.writeNaturalScroll(C.bool(natural)); rc != 0 {
		return fmt.Errorf("write scroll setting: CFPreferencesAppSynchronize failed")
	}
	return nil
}

// GetViaDefaults reads the scroll setting using the `defaults` CLI. Intended
// for tests and diagnostics; production code should use Get().
func (Scroller) GetViaDefaults() (bool, error) {
	out, err := exec.Command("defaults", "read", "NSGlobalDomain", "com.apple.swipescrolldirection").Output()
	if err != nil {
		// If the key has never been set, macOS defaults to natural scrolling ON.
		if strings.Contains(string(out), "not been set") || strings.Contains(string(out), "does not exist") {
			return true, nil
		}
		return false, fmt.Errorf("read scroll setting: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	b, err := parseBoolish(s)
	if err != nil {
		return false, fmt.Errorf("%w: %q", ErrInvalidValue, s)
	}
	return b, nil
}

// parseBoolish accepts the various forms `defaults read` may emit
// ("0", "1", "0\n", "true", "false", etc.) and returns a bool.
func parseBoolish(s string) (bool, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return false, ErrInvalidValue
	case "1", "true", "TRUE", "True", "YES", "yes", "Yes":
		return true, nil
	case "0", "false", "FALSE", "False", "NO", "no", "No":
		return false, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n != 0, nil
	}
	return false, ErrInvalidValue
}
