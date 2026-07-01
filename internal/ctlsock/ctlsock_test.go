package ctlsock

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerClientRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	h := HandlerFunc(func(method string, params json.RawMessage) (any, error) {
		if method != MethodStatus {
			t.Errorf("unexpected method %q", method)
		}
		return map[string]any{"running": true, "paused": false}, nil
	})

	srv := NewServer(sock, h, nil)
	go func() { _ = srv.ListenAndServe() }()

	// Wait for socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := NewClient(sock)
	resp, err := c.Call(MethodStatus, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var out struct {
		Running bool `json:"running"`
		Paused  bool `json:"paused"`
	}
	if err := resp.DecodeResult(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Running || out.Paused {
		t.Errorf("unexpected result %+v", out)
	}
}

func TestServerErrorPropagated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	h := HandlerFunc(func(method string, _ json.RawMessage) (any, error) {
		return nil, errSentinel
	})
	srv := NewServer(sock, h, nil)
	go func() { _ = srv.ListenAndServe() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := NewClient(sock)
	resp, err := c.Call("Whatever", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Error == "" {
		t.Errorf("expected error in response")
	}
}

func TestSplitDir(t *testing.T) {
	t.Parallel()
	cases := map[string][2]string{
		"/a/b/c.sock": {"/a/b", "c.sock"},
		"a.sock":      {"", "a.sock"},
		"/a.sock":     {"/", "a.sock"},
	}
	for in, want := range cases {
		d, f := splitDir(in)
		if d != want[0] || f != want[1] {
			t.Errorf("splitDir(%q) = (%q,%q), want (%q,%q)", in, d, f, want[0], want[1])
		}
	}
}

// Verify DialTimeout errors when daemon is not running.
func TestClientDialFailsOnMissingSocket(t *testing.T) {
	t.Parallel()
	c := NewClient("/tmp/macscrollswap-does-not-exist-test.sock")
	_, err := c.Call(MethodStatus, nil)
	if err == nil {
		t.Fatalf("expected dial error")
	}
	if _, ok := err.(*net.OpError); ok {
		return
	}
	// Wrap of net.OpError is fine too.
}

var errSentinel = sentinelErr{}

type sentinelErr struct{}

func (sentinelErr) Error() string { return "boom" }
