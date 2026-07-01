// Package launchd manages the user LaunchAgent used to run the macscrollswap
// daemon at login and keep it alive.
package launchd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Label is the reverse-DNS launchd label.
const Label = "com.macscrollswap"

// PlistFilename is the file name inside ~/Library/LaunchAgents.
const PlistFilename = Label + ".plist"

// PlistPath returns the absolute path of the LaunchAgent plist.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", PlistFilename), nil
}

// ExecPath reports the absolute path to the macscrollswap binary that should
// be invoked by launchd. It prefers os.Executable, but if that returns
// os.ErrPermission it falls back to the binary on $PATH.
func ExecPath() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if _, statErr := os.Stat(exe); statErr == nil {
			return exe, nil
		}
	}
	if found, err := exec.LookPath("macscrollswap"); err == nil {
		return found, nil
	}
	return "", errors.New("could not locate macscrollswap binary")
}

// Install writes the LaunchAgent plist and loads it with launchctl. If the
// agent is already loaded, it is first unloaded so we apply fresh config.
func Install() error {
	agentsDir, err := agentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	exe, err := ExecPath()
	if err != nil {
		return err
	}
	logDir, _ := logDir()
	_ = os.MkdirAll(logDir, 0o755)

	p, err := PlistPath()
	if err != nil {
		return err
	}

	// Unload existing instance if present (ignore errors).
	_, _ = exec.Command("launchctl", "unload", p).CombinedOutput()

	plist := buildPlist(Label, exe, logDir)
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist %s: %w", p, err)
	}

	if out, err := exec.Command("launchctl", "load", p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall unloads the agent (if loaded) and removes the plist file.
func Uninstall() error {
	p, err := PlistPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr == nil {
		if out, err := exec.Command("launchctl", "unload", p).CombinedOutput(); err != nil {
			// Continue removing the file even if unload failed.
			_ = strings.TrimSpace(string(out))
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove plist %s: %w", p, err)
		}
	}
	return nil
}

// IsInstalled reports whether the LaunchAgent plist exists on disk.
func IsInstalled() (bool, error) {
	p, err := PlistPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// StatusInfo describes the current LaunchAgent state.
type StatusInfo struct {
	Installed bool   `json:"installed"`
	PlistPath string `json:"plist_path"`
	ExecPath  string `json:"exec_path"`
	Loaded    bool   `json:"loaded"`
}

// Status reports the current LaunchAgent state.
func Status() (*StatusInfo, error) {
	p, err := PlistPath()
	if err != nil {
		return nil, err
	}
	st := &StatusInfo{PlistPath: p}
	if exe, err := ExecPath(); err == nil {
		st.ExecPath = exe
	}
	if installed, err := IsInstalled(); err != nil {
		return nil, err
	} else {
		st.Installed = installed
	}
	if st.Installed {
		// `launchctl list <label>` prints a plist on success and errors on
		// not-loaded.
		if err := exec.Command("launchctl", "list", Label).Run(); err == nil {
			st.Loaded = true
		}
	}
	return st, nil
}

func buildPlist(label, exe, logDir string) string {
	const tmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{EXE}</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{LOGDIR}/daemon.stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{LOGDIR}/daemon.stderr.log</string>
</dict>
</plist>
`
	out := strings.ReplaceAll(tmpl, "{LABEL}", label)
	out = strings.ReplaceAll(out, "{EXE}", exe)
	out = strings.ReplaceAll(out, "{LOGDIR}", logDir)
	return out
}

func agentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

func logDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "macscrollswap"), nil
}
