# macscrollswap

A small macOS daemon (Go) that automatically toggles **natural scrolling**
on or off based on whether a Bluetooth mouse is currently connected.

- When a Bluetooth mouse **connects** → set natural scrolling to your configured
  "connected" value (default: **OFF**).
- When it **disconnects** → set natural scrolling to your configured
  "disconnected" value (default: captured from your system setting on first
  launch).
- Pause/resume, configure target values, and watch status from a CLI.
- Global keyboard shortcuts (configurable) for pause and direction-swap.
- Visual feedback via macOS Notification Center on state changes.

> I realized that macOS doesn't ship a way to keep natural scrolling on for the trackpad but off for an external Bluetooth mouse. This tool fills that gap.



## Requirements

- macOS (developed on Apple Silicon; Intel should work)
- Go 1.26+ (only needed to build from source)
- `CGO_ENABLED=1` (the Makefile sets this; the scroller and hotkey code use cgo)



## Build

```sh
make build       # produces ./bin/macscrollswap
make test        # unit tests
make lint        # go vet + golangci-lint (if installed)
make run-daemon  # build and run the daemon in the foreground
```



## Install

After building, install the user LaunchAgent (runs at login, kept alive):

```sh
make install-local            # copies the binary to ~/.local/bin
macscrollswap install         # writes + loads ~/Library/LaunchAgents/com.macscrollswap.plist
```

The daemon will start immediately and at every login. To remove:

```sh
macscrollswap uninstall
```



## Updating after code changes

After editing the source, rebuild, copy the binary to the same path the
LaunchAgent uses, then bounce the daemon:

```sh
make build
make install-local            # or: cp bin/macscrollswap ~/.local/bin/macscrollswap
macscrollswap restart
macscrollswap status          # confirm it came back healthy
```

`restart` runs `launchctl kickstart -k`, which kills the old process and
starts a fresh one with the updated binary. Use this whenever the LaunchAgent
is installed (the normal setup).


| Goal                               | Command                                                     |
| ---------------------------------- | ----------------------------------------------------------- |
| Rebuild + restart (usual workflow) | `make build && make install-local && macscrollswap restart` |
| Check status                       | `macscrollswap status`                                      |
| Stop (without LaunchAgent)         | `macscrollswap stop`                                        |
| Run in foreground (debugging)      | `macscrollswap daemon`                                      |
| Re-enable auto-start at login      | `macscrollswap install`                                     |


If `restart` says the LaunchAgent is not installed, run `macscrollswap install`
once, then use `restart` as usual.

**Note:** With the LaunchAgent installed (`KeepAlive`), `macscrollswap stop`
shuts the daemon down but launchd restarts it immediately. Use `restart` to
bounce it, or `uninstall` to stop it for good.

## CLI

Most subcommands talk to the running daemon over a Unix socket. These work
without the daemon: `daemon`, `install`, `uninstall`, `config`, `config path`,
and `version`.

```sh
macscrollswap daemon                                # run daemon (foreground; usually launched by launchd)
macscrollswap status                                # show daemon state (alias: st)
macscrollswap status --json                         # same, as JSON
macscrollswap restart                               # bounce daemon after a rebuild (launchd-managed)
macscrollswap stop                                  # shut down the running daemon (alias: shutdown)
macscrollswap pause | resume                        # pause/resume auto-swapping
macscrollswap direction                             # show current target values
macscrollswap direction --connected on|off          # value to apply when a BT mouse is connected
macscrollswap direction --disconnected on|off       # value to apply when no BT mouse is connected
macscrollswap direction --swap                      # swap the connected/disconnected values
macscrollswap config                                # print current config as JSON
macscrollswap config path                           # print the path to the config file
macscrollswap version                               # aliases: -v, --version
```



### Default hotkeys (configurable)


| Combo            | Action                              |
| ---------------- | ----------------------------------- |
| `ctrl+opt+cmd+s` | Toggle pause/resume                 |
| `ctrl+opt+cmd+d` | Swap connected/disconnected targets |


Hotkey strings are lowercase, `+`-separated, with at least one modifier
(`ctrl`, `shift`, `opt`/`alt`, `cmd`) and a key (`a`–`z`, `0`–`9`, `space`,
`esc`, `return`, `tab`, arrow keys, `f1`–`f5`).

Hotkey registration is best-effort: if a combo fails to register (e.g. already
taken by another app), the daemon keeps running and logs a warning.

## Config

JSON, at `~/Library/Application Support/macscrollswap/config.json`:

```json
{
  "scroll_natural_when_connected":    false,
  "scroll_natural_when_disconnected": true,
  "hotkey_pause":                     "ctrl+opt+cmd+s",
  "hotkey_direction":                 "ctrl+opt+cmd+d",
  "poll_interval":                    "3s"
}
```

After first run, the saved file also includes `socket_path`, `log_path`, and
`baseline_captured` (absolute paths and internal flags — you normally do not
need to edit these).

On first launch, `scroll_natural_when_disconnected` is set to whatever value
`com.apple.swipescrolldirection` currently holds, so existing trackpad users
keep their preference.

While **paused**, connect/disconnect events are still detected and notified,
but scroll direction is not changed until you resume.

## How it works

- **Single instance**: the daemon acquires an exclusive lock at
  `~/Library/Application Support/macscrollswap/daemon.lock`.
- **Bluetooth mouse detection**: polls `ioreg -a -r -c IOHIDDevice` every few
  seconds and looks for HID devices whose `Transport` starts with `Bluetooth`
  (covers both Classic and Bluetooth Low Energy) and whose
  `PrimaryUsagePage`/`PrimaryUsage` is `1`/`2` (Generic Desktop → mouse). This
  catches Apple Magic Mouse and third-party Bluetooth mice alike.
- **Setting natural scrolling**: calls the private CoreGraphics SPI
  `CGSSetSwipeScrollDirection` (via `_CGSDefaultConnection`) so the change takes
  effect on the live input system immediately, then persists the value to
  `com.apple.swipescrolldirection` in `NSGlobalDomain` via CoreFoundation
  (`CFPreferences`) so it survives reboots, and posts a
  `SwipeScrollDirectionDidChangeNotification` distributed notification so the
  System Settings Mouse/Trackpad panes redraw to match if they're open.
- **Hotkeys**: registered via Carbon `RegisterEventHotKey`
  (`golang.design/x/hotkey`). On macOS this requires the daemon to run an
  NSApplication event loop on the main thread, handled by
  `golang.design/x/hotkey/mainthread`.
- **IPC**: JSON-RPC over a Unix-domain socket at
  `~/Library/Application Support/macscrollswap/daemon.sock`.
- **Notifications**: best-effort `osascript display notification` (with the
  "Frog" sound) on connect/disconnect, pause/resume, and direction changes.
  Hotkey actions also play a system beep. A failed notification is logged,
  never fatal.
- **Startup**: on launch the daemon applies the scroll setting for the current
  mouse state immediately, without waiting for a connect/disconnect transition.



## Logging

Structured logs are written to
`~/Library/Logs/macscrollswap/daemon.log`.
launchd also captures stdout/stderr to `daemon.stdout.log` /
`daemon.stderr.log` in the same directory.

## Limitations / known caveats

- Applying the scroll direction relies on a private, undocumented CoreGraphics
  SPI (`CGSSetSwipeScrollDirection`). It works reliably on current macOS, but
  Apple could change or remove it in a future release; if that happens the
  on-disk preference still updates, but the change may not take effect until the
  next login.
- Detection is Bluetooth-only; USB-receiver wireless mice (Logitech Unifying,
  etc.) are intentionally ignored per the project scope.
- The macOS APIs require running the daemon from a regular Aqua session;
  running it over SSH is not supported.



## Project layout

```
cmd/macscrollswap/      CLI entrypoint + subcommand dispatch
internal/
  btmonitor/            Bluetooth mouse polling watcher
  config/               JSON config load/save + defaults
  ctlsock/              JSON-RPC over Unix socket (server + client)
  daemon/               State machine, lifecycle, RPC handlers
  hotkey/               Global keyboard shortcut manager
  launchd/              LaunchAgent install/uninstall
  notify/               macOS Notification Center + beep
  scroller/             Read/write com.apple.swipescrolldirection
```



## License

Released under the [MIT License](LICENSE).
