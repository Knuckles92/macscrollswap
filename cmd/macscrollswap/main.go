// Command macscrollswap is the entrypoint for both the background daemon and
// the CLI used to control it.
//
// The daemon subcommand must be invoked via the mainthread package so the
// global hotkeys can be delivered on the main OS thread. All other subcommands
// are simple one-shot RPC clients that talk to the daemon over a Unix-domain
// socket and never need the main-thread event loop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.design/x/hotkey/mainthread"

	"macscrollswap/internal/config"
	"macscrollswap/internal/ctlsock"
	"macscrollswap/internal/daemon"
	"macscrollswap/internal/launchd"
)

// version is set at link time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "daemon":
		// Must run inside mainthread.Init so macOS global hotkeys are
		// delivered on the main thread.
		mainthread.Init(runDaemon)
	case "status", "st":
		cmdStatus(os.Args[2:])
	case "pause":
		cmdSimple(ctlsock.MethodPause, "paused")
	case "resume":
		cmdSimple(ctlsock.MethodResume, "resumed")
	case "stop", "shutdown":
		cmdStop()
	case "restart":
		cmdRestart()
	case "direction":
		cmdDirection(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "install":
		exitOnErr(launchd.Install(), "install LaunchAgent")
		fmt.Println("LaunchAgent installed and loaded. The daemon will start at login.")
	case "uninstall":
		exitOnErr(launchd.Uninstall(), "uninstall LaunchAgent")
		fmt.Println("LaunchAgent uninstalled.")
	case "config":
		cmdConfig(os.Args[2:])
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func runDaemon() {
	// Set up structured logging to a file ASAP so we capture daemon startup.
	logFile, err := setupLogging()
	if err != nil {
		// Fall back to stderr; launchd captures stdout/stderr.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		slog.Error("could not open log file; logging to stderr", "err", err)
	} else {
		defer logFile.Close()
	}

	cfg, isNew, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	if isNew {
		slog.Info("creating default config; capturing baseline scroll setting")
	}

	release, err := daemon.AcquireLock()
	if err != nil {
		slog.Error("acquire lock", "err", err)
		os.Exit(1)
	}
	defer release()

	daemon.Version = version
	d, err := daemon.New(cfg, daemon.WithLogger(slog.Default()))
	if err != nil {
		slog.Error("initialize daemon", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := d.Run(ctx); err != nil {
		slog.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

func setupLogging() (*os.File, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, err
	}
	logPath := cfg.LogPath
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	handler := slog.NewTextHandler(io.MultiWriter(f), &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return f, nil
}

// --- CLI subcommands ---

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit status as JSON")
	_ = fs.Parse(args)

	c := mustClient()
	resp, err := c.Call(ctlsock.MethodStatus, nil)
	if err != nil {
		exitOnErr(err, "reach daemon")
	}
	var st daemon.StatusResponse
	if err := resp.DecodeResult(&st); err != nil {
		exitOnErr(err, "decode status")
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return
	}

	fmt.Println("macscrollswap status")
	fmt.Println("──────────────────────────────────────────")
	fmt.Printf("  version              %s\n", st.Version)
	fmt.Printf("  state                %s\n", statusState(st))
	fmt.Printf("  paused               %t\n", st.Paused)
	fmt.Printf("  bluetooth mouse      %s\n", yesNo(st.MouseConnected))
	if st.CurrentNaturalScroll != nil {
		fmt.Printf("  current natural      %s\n", onOff(*st.CurrentNaturalScroll))
	}
	fmt.Printf("  when connected       %s\n", onOff(st.WhenConnected))
	fmt.Printf("  when disconnected    %s\n", onOff(st.WhenDisconnected))
	fmt.Printf("  control socket       %s\n", st.Socket)
}

func statusState(st daemon.StatusResponse) string {
	if st.Paused {
		return "PAUSED"
	}
	if st.MouseConnected {
		return "MOUSE-CONNECTED"
	}
	return "DISCONNECTED"
}

func cmdSimple(method, label string) {
	c := mustClient()
	resp, err := c.Call(method, nil)
	exitOnErr(err, "reach daemon")
	if resp.Error != "" {
		exitOnErr(errors.New(resp.Error), label)
	}
	fmt.Printf("ok: daemon %s\n", label)
}

// cmdStop asks a running daemon to shut down cleanly via RPC. If the daemon
// was launched by launchd with KeepAlive=true, launchd will restart it
// immediately — in that case use `macscrollswap restart` or unload the agent.
func cmdStop() {
	c := mustClient()
	resp, err := c.Call(ctlsock.MethodShutdown, nil)
	exitOnErr(err, "reach daemon")
	if resp.Error != "" {
		exitOnErr(errors.New(resp.Error), "stop")
	}
	fmt.Println("daemon shutting down")
	// If launchd owns the daemon, warn the user it'll come back.
	if st, err := launchd.Status(); err == nil && st.Installed && st.Loaded {
		fmt.Println("note: LaunchAgent is still loaded (KeepAlive) — it will restart shortly.")
		fmt.Println("      Use `macscrollswap restart` to bounce it, or `macscrollswap uninstall` to stop for good.")
	}
}

// cmdRestart bounces a launchd-managed daemon via launchctl kickstart. If the
// daemon is not launchd-managed, it stops it and asks the user to start it
// again manually (a process cannot reliably re-spawn itself after exit).
func cmdRestart() {
	st, err := launchd.Status()
	exitOnErr(err, "check LaunchAgent")
	if !st.Installed {
		// Fall back to a plain stop with guidance.
		c := mustClient()
		_, callErr := c.Call(ctlsock.MethodShutdown, nil)
		exitOnErr(callErr, "reach daemon")
		fmt.Println("daemon stopped. LaunchAgent is not installed — run `macscrollswap daemon` to start again.")
		return
	}
	// `launchctl kickstart -k` kills the current instance and starts a fresh
	// one. The service target is gui/<uid>/<label>.
	uid := os.Getuid()
	target := fmt.Sprintf("gui/%d/%s", uid, launchd.Label)
	out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput()
	if err != nil {
		exitOnErr(fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out))), "launchctl kickstart")
	}
	fmt.Println("daemon restarted")
}

func cmdDirection(args []string) {
	if len(args) == 0 {
		// Just print current direction values.
		c := mustClient()
		resp, err := c.Call(ctlsock.MethodGetDirection, nil)
		exitOnErr(err, "reach daemon")
		var dr daemon.DirectionResponse
		if err := resp.DecodeResult(&dr); err != nil {
			exitOnErr(err, "decode direction")
		}
		fmt.Println("macscrollswap direction")
		fmt.Printf("  when connected      %s\n", onOff(dr.WhenConnected))
		fmt.Printf("  when disconnected   %s\n", onOff(dr.WhenDisconnected))
		return
	}

	fs := flag.NewFlagSet("direction", flag.ExitOnError)
	connected := fs.String("connected", "", "set natural scrolling value when a BT mouse is connected (on|off)")
	disconnected := fs.String("disconnected", "", "set natural scrolling value when no BT mouse is connected (on|off)")
	swap := fs.Bool("swap", false, "swap the two values (also exposed as a hotkey)")
	_ = fs.Parse(args)

	c := mustClient()

	if *swap {
		resp, err := c.Call(ctlsock.MethodSwapDirection, nil)
		exitOnErr(err, "reach daemon")
		var dr daemon.DirectionResponse
		if err := resp.DecodeResult(&dr); err != nil {
			exitOnErr(err, "swap direction")
		}
		fmt.Printf("swapped: connected=%s disconnected=%s\n", onOff(dr.WhenConnected), onOff(dr.WhenDisconnected))
		return
	}

	if *connected != "" {
		v, err := parseOnOff(*connected)
		exitOnErr(err, "--connected")
		params := map[string]any{"value": v}
		resp, err := c.Call(ctlsock.MethodSetConnected, params)
		exitOnErr(err, "reach daemon")
		var dr daemon.DirectionResponse
		if err := resp.DecodeResult(&dr); err != nil {
			exitOnErr(err, "set --connected")
		}
		fmt.Printf("when-connected set to %s\n", onOff(dr.WhenConnected))
	}
	if *disconnected != "" {
		v, err := parseOnOff(*disconnected)
		exitOnErr(err, "--disconnected")
		params := map[string]any{"value": v}
		resp, err := c.Call(ctlsock.MethodSetDisconnect, params)
		exitOnErr(err, "reach daemon")
		var dr daemon.DirectionResponse
		if err := resp.DecodeResult(&dr); err != nil {
			exitOnErr(err, "set --disconnected")
		}
		fmt.Printf("when-disconnected set to %s\n", onOff(dr.WhenDisconnected))
	}
}

func cmdConfig(args []string) {
	cfg, _, err := config.Load()
	exitOnErr(err, "load config")
	if len(args) > 0 && args[0] == "path" {
		p, err := config.Path()
		exitOnErr(err, "config path")
		fmt.Println(p)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cfg)
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "y":
		return true, nil
	case "off", "false", "0", "no", "n":
		return false, nil
	}
	return false, fmt.Errorf("expected on|off, got %q", s)
}

func onOff(b bool) string {
	if b {
		return "ON (natural)"
	}
	return "OFF (traditional)"
}

func yesNo(b bool) string {
	if b {
		return "connected"
	}
	return "disconnected"
}

func mustClient() *ctlsock.Client {
	cfg, _, err := config.Load()
	if err != nil {
		exitOnErr(err, "load config")
	}
	return ctlsock.NewClient(cfg.SocketPath)
}

func exitOnErr(err error, action string) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "macscrollswap: %s failed: %v\n", action, err)
	os.Exit(1)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `macscrollswap %s — automatically toggle macOS natural scrolling based on Bluetooth mouse state.

Usage:
  macscrollswap <command> [flags]

Commands:
  daemon                        run the background daemon (must be invoked via launchd)
  status                        show daemon state and current direction (alias: st)
  pause                         pause automatic swapping (alias of pause RPC)
  resume                        resume automatic swapping
  stop                          shut the running daemon down (alias: shutdown)
  restart                       bounce the daemon (launchctl kickstart if launchd-managed)
  direction                     show or set scroll direction targets
    direction --connected on|off        natural scrolling value when BT mouse is connected
    direction --disconnected on|off     natural scrolling value when no BT mouse is connected
    direction --swap                    swap the connected/disconnected values
  config                        print current config as JSON
  config path                   print the path to the config file
  install                       install + load the user LaunchAgent
  uninstall                     unload + remove the user LaunchAgent
  version                       print the version

Default hotkeys (configurable in the config file):
  ctrl+opt+cmd+s                toggle pause
  ctrl+opt+cmd+d                swap connected/disconnected target values

All subcommands except daemon and install/uninstall require the daemon to be
  running. Install it with `+"`macscrollswap install`"+`.
`, version)
}
