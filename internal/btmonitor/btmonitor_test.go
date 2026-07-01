package btmonitor

import "testing"

// miniBLEMousePlist is a minimal ioreg -a -r -c IOHIDDevice XML plist
// containing a single BLE mouse entry shaped like a Logitech M240, used to
// regression-test the full parse path. macOS reports BLE mice with
// Transport="Bluetooth Low Energy" — this must be detected as connected.
const miniBLEMousePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>Manufacturer</key>
		<string>Logitech</string>
		<key>PrimaryUsage</key>
		<integer>2</integer>
		<key>PrimaryUsagePage</key>
		<integer>1</integer>
		<key>Product</key>
		<string>LOGI M240</string>
		<key>Transport</key>
		<string>Bluetooth Low Energy</string>
	</dict>
	<dict>
		<key>Manufacturer</key>
		<string>Apple Inc.</string>
		<key>PrimaryUsage</key>
		<integer>6</integer>
		<key>PrimaryUsagePage</key>
		<integer>1</integer>
		<key>Product</key>
		<string>Magic Keyboard</string>
		<key>Transport</key>
		<string>Bluetooth</string>
	</dict>
</array>
</plist>
`

// plistWithDataBlock reproduces the real-world failure that prompted the
// custom parser: howett.net/plist dropped top-level dicts that contained a
// multi-line <data> (base64) ReportDescriptor value, which is what a real
// Logitech M240 ioreg entry looks like. Our encoding/xml-based parser must
// correctly skip the <data> block and still detect the mouse.
const plistWithDataBlock = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>Product</key>
		<string>LOGI M240</string>
		<key>PrimaryUsage</key>
		<integer>2</integer>
		<key>PrimaryUsagePage</key>
		<integer>1</integer>
		<key>Transport</key>
		<string>Bluetooth Low Energy</string>
		<key>Elements</key>
		<array>
			<dict>
				<key>ElementCookie</key>
				<integer>4</integer>
			</dict>
		</array>
		<key>ReportDescriptor</key>
		<data>
		BQEJAqEBhQIJAaEAlQN1ARUAJQEFCRkBKQOBApUNgQMFARYB+Cb/BzYB+Eb/
		B1UNZRN1DJUCCTAJMYEGFYElfzUARQB1CJUBCTiBBpUBgQPAwAZD/woCAqEB
		hRGVE3UIFQAm/wAJAoEACQKRAMA=
		</data>
	</dict>
</array>
</plist>
`

func TestParseEmpty(t *testing.T) {
	t.Parallel()
	ok, err := parseBluetoothMouse(nil)
	if err != nil || ok {
		t.Errorf("empty input should yield (false, nil), got (%v, %v)", ok, err)
	}
}

func TestParseBLEMouseRegression(t *testing.T) {
	t.Parallel()
	ok, err := parseBluetoothMouse([]byte(miniBLEMousePlist))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok {
		t.Errorf("BLE mouse plist should be detected as connected")
	}
}

func TestParseHandlesDataBlock(t *testing.T) {
	t.Parallel()
	// Regression: howett.net/plist dropped this entry because of the
	// multi-line <data> ReportDescriptor. encoding/xml must detect the mouse.
	entries, err := parseTopDicts([]byte(plistWithDataBlock))
	if err != nil {
		t.Fatalf("parseTopDicts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 top-level dict, got %d", len(entries))
	}
	if entries[0].Product != "LOGI M240" {
		t.Errorf("expected LOGI M240, got %q", entries[0].Product)
	}
	ok, err := parseBluetoothMouse([]byte(plistWithDataBlock))
	if err != nil || !ok {
		t.Errorf("expected data-block mouse to be detected, got ok=%v err=%v", ok, err)
	}
}

func TestIsBluetoothMouse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		e    monitorEntry
		want bool
	}{
		{"Classic BT mouse", monitorEntry{Transport: "Bluetooth", PrimaryUsagePage: 1, PrimaryUsage: 2}, true},
		{"BLE mouse (Logitech M240)", monitorEntry{Transport: "Bluetooth Low Energy", PrimaryUsagePage: 1, PrimaryUsage: 2}, true},
		{"BT keyboard (not mouse)", monitorEntry{Transport: "Bluetooth", PrimaryUsagePage: 1, PrimaryUsage: 6}, false},
		{"USB mouse", monitorEntry{Transport: "USB", PrimaryUsagePage: 1, PrimaryUsage: 2}, false},
		{"No transport", monitorEntry{Transport: "", PrimaryUsagePage: 1, PrimaryUsage: 2}, false},
		{"Audio transport", monitorEntry{Transport: "Audio", PrimaryUsagePage: 1, PrimaryUsage: 2}, false},
	}
	for _, tc := range cases {
		got := isBluetoothMouse(tc.e)
		if got != tc.want {
			t.Errorf("%s: isBluetoothMouse(%+v) = %v, want %v", tc.name, tc.e, got, tc.want)
		}
	}
}

func TestIsBluetoothTransport(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"Bluetooth":            true,
		"Bluetooth Low Energy": true,
		"bluetooth low energy": true,
		"BLUETOOTH":            true,
		"  Bluetooth  ":        true,
		"USB":                  false,
		"Audio":                false,
		"":                     false,
		"FIFO":                 false,
		"Bluetoothed_HID":      true, // prefix match — lenient on purpose
	}
	for in, want := range cases {
		got := isBluetoothTransport(in)
		if got != want {
			t.Errorf("isBluetoothTransport(%q) = %v, want %v", in, got, want)
		}
	}
}
