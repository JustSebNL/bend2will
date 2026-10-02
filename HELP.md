# obey — full command reference

```
obey <command> [options]
```

## Commands

| Command | Alias | Description |
|---------|-------|-------------|
| `shoo` | `run` | Supervise a program in the foreground |
| `service` | — | Internal entry point invoked by Windows SCM |
| `bind` | `install` | Install a service with the platform's service manager |
| `release` | `uninstall` | Stop and remove a service |
| `serve` | `start` | Start an installed service |
| `terminate` | `stop` | Stop an installed service |
| `restart` | — | Stop, wait for, and start an installed service |
| `report` | `status` | Report an installed service's state |
| `show` | `print` | Show the service definition without applying it |
| `portable` | — | The portable edition: same commands, installed per-user |
| `version` | — | Print the version |
| `help` | — | Show help for a command |

---

## shoo (alias: run)

Supervise a program in the foreground. Translates Ctrl-C and SIGTERM into a graceful child shutdown.

```
obey shoo [options] [command [arguments...]]
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | Service name (defaults to the command's base name) |
| `-display-name string` | Human-readable name (defaults to -name) |
| `-description string` | One-line description of the service |
| `-command string` | Program to run (required unless -c supplies it) |
| `-arguments string` | Arguments for the program: quoted text, or a JSON array |
| `-workdir string` | Working directory for the program |
| `-user string` | Run the program as this user (Unix, needs root) |
| `-group string` | Run the program as this group (Unix, needs root) |
| `-priority string` | Scheduling priority: normal, high, idle or realtime |
| `-env value` | Set an environment variable, KEY=VALUE; repeatable |
| `-after value` | A unit or service that must start first; repeatable |
| `-log string` | Where to log: "" or - for stderr, @ for the system log, or a file path |
| `-level string` | Log level: trace, debug, info, warn, error or off |
| `-log-max-bytes int64` | Rotate file logs at this size and keep one `.1` backup; 0 disables rotation |
| `-mode string` | How to relate to the service manager: auto, service or console |
| `-restart string` | Restart the program when it exits: true or false |
| `-restart-initial-delay string` | Delay before the first restart (default 1s) |
| `-restart-max-delay string` | Upper bound on the restart delay (default 1m) |
| `-restart-reset-after string` | Uptime that resets the backoff (default 10m) |
| `-restart-factor string` | Multiplier per consecutive failure (default 2) |
| `-stop string` | How to stop the program: signal, kill or exec |
| `-stop-command string` | Command run by -stop exec; %PID% is replaced |
| `-stop-timeout string` | How long a graceful stop may take (default 15s) |
| `-stop-signal string` | Signal used for a graceful stop (default TERM) |
| `-start-timeout string` | How long a start may take (default 60s) |
| `-ready-command string` | Command that must succeed before running is reported |
| `-ready-interval string` | How often to retry the ready command (default 500ms) |
| `-c string` | Read the configuration from this file |
| `-config string` | Read the configuration from this file |

### Examples

```bash
# Simplest possible: supervise a program with no flags at all
obey shoo myapp /usr/bin/myapp --serve

# With explicit options
obey shoo myapp --command /usr/bin/myapp --arguments "--serve --port 5432" \
  --workdir /var/lib/myapp --restart true --log @

# From a configuration file
obey shoo -c /etc/myapp/obey.conf myapp
```

---

## bind (alias: install)

Install a service with the platform's service manager (SCM/systemd/launchd).

```
obey bind [options] [command [arguments...]]
```

### Options

All options from `shoo`, plus:

| Flag | Description |
|------|-------------|
| `-unit-dir string` | Directory to write the service definition into |
| `-enable` | Start the service at boot |
| `-now` | Start the service immediately |
| `-dry-run` | Report what would happen without changing anything |
| `-service-account string` | Windows built-in account: LocalSystem, LocalService or NetworkService |
| `-delayed-auto-start` | Windows automatic startup after regular automatic services |
| `-restart-service-on-failure` | Configure Windows SCM to restart the `obey` service on failure |
| `-service-restart-delay string` | Windows SCM restart delay (default 5s) |

### Examples

```bash
# Install a service
obey bind myapp --command /usr/bin/myapp --arguments "--serve"

# Install and start immediately
obey bind myapp --command /usr/bin/myapp --enable --now

# Preview without installing
obey bind myapp --command /usr/bin/myapp --dry-run

# Install to a custom directory
obey bind myapp --command /usr/bin/myapp --unit-dir /etc/systemd/system
```

---

## release (alias: uninstall)

Stop and remove an installed service.

```
obey release [options] <service>
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | The service to operate on, instead of a positional argument |
| `-dry-run` | Report what would happen without changing anything |
| `--purge` | Also remove the service's saved data |

### Examples

```bash
# Remove a service
obey release myapp

# Remove and purge saved data
obey release myapp --purge

# Preview without removing
obey release myapp --dry-run
```

---

## serve (alias: start)

Start an installed service.

```
obey serve [options] <service>
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | The service to operate on, instead of a positional argument |
| `-dry-run` | Report what would happen without changing anything |

### Examples

```bash
# Start a service
obey serve myapp

# Preview without starting
obey serve myapp --dry-run
```

---

## terminate (alias: stop)

Stop an installed service.

```
obey terminate [options] <service>
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | The service to operate on, instead of a positional argument |
| `-dry-run` | Report what would happen without changing anything |

### Examples

```bash
# Stop a service
obey terminate myapp

# Preview without stopping
obey terminate myapp --dry-run
```

---

## restart

Stop an installed service, wait for it to stop, start it again, and wait for it to report running. Both waits are bounded to 30 seconds.

```
obey restart [options] <service>
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | The service to operate on, instead of a positional argument |
| `-dry-run` | Report what would happen without changing anything |

### Example

```bash
obey restart myapp
```

---

## report (alias: status)

Report an installed service's state.

```
obey report [options] <service>
```

### Options

| Flag | Description |
|------|-------------|
| `-name string` | The service to operate on, instead of a positional argument |
| `-dry-run` | Report what would happen without changing anything |

### Examples

```bash
# Check a service's state
obey report myapp
```

---

## show (alias: print)

Show the generated service definition without applying it.

```
obey show [options] [command [arguments...]]
```

### Options

All options from `shoo`, plus:

| Flag | Description |
|------|-------------|
| `-with-instructions` | Include the manual steps after the definition (default true) |

### Examples

```bash
# Show the systemd unit file
obey show myapp --command /usr/bin/myapp --arguments "--serve"

# Show without manual steps
obey show myapp --command /usr/bin/myapp --with-instructions=false
```

---

## portable

The portable edition: the same commands, installed per-user without root.

```
obey portable <command> [options]
```

### Examples

```bash
# Install a per-user service
obey portable bind myapp --command /usr/bin/myapp

# Start it
obey portable serve myapp

# Check its state
obey portable report myapp

# Remove it
obey portable release myapp
```

---

## Configuration file

Configuration files use an INI-like format:

```ini
# a comment, or a ; comment
[myapp]
command = /usr/bin/myapp
args    = --serve --port 5432
restart = true
workdir = /var/lib/myapp
user    = myapp
env     = PORT=5432
env     = MODE=production
after   = network-online.target
```

A file may hold several services. `obey shoo -c <file> <name>` reads the section matching `<name>`.

### Directives

| Directive | Description |
|-----------|-------------|
| `name` | Service name |
| `displayname` | Human-readable name |
| `description` | One-line description |
| `command` | Program to run |
| `args` | Arguments for the program |
| `arguments` | Alias for `args` |
| `env` | Environment variable, KEY=VALUE; repeatable |
| `workdir` | Working directory |
| `user` | Run as this user (Unix, needs root) |
| `group` | Run as this group (Unix, needs root) |
| `priority` | Scheduling priority: normal, high, idle or realtime |
| `mode` | auto, service or console |
| `log` | Log destination: "" or - for stderr, @ for system log, or a file path |
| `level` | Log level: trace, debug, info, warn, error or off |
| `log_max_bytes` | Rotate file logs at this size and keep one `.1` backup; 0 disables rotation |
| `debug` | Alias for `level` |
| `restart` | true or false |
| `restart_initial_delay` | Delay before the first restart |
| `restart_max_delay` | Upper bound on the restart delay |
| `restart_reset_after` | Uptime that resets the backoff |
| `restart_factor` | Multiplier per consecutive failure |
| `stop` | How to stop: signal, kill or exec |
| `stop_command` | Command run by stop=exec; %PID% is replaced |
| `stop_timeout` | How long a graceful stop may take |
| `stop_signal` | Signal used for a graceful stop |
| `start_timeout` | How long a start may take |
| `ready_command` | Command that must succeed before running is reported |
| `ready_interval` | How often to retry the ready command |
| `after` | A unit or service that must start first; repeatable |
| `depends_on` | Alias for `after` |

---

## Platforms

- **Windows**: Service Control Manager (SCM) via advapi32. Status reported with SetServiceStatus, stop requests arrive as SERVICE_CONTROL_STOP, process tree tracked in a job object.
- **Linux**: systemd. Generated unit is Type=notify, status reported over $NOTIFY_SOCKET.
- **macOS**: launchd. Generated plist is a LaunchDaemon; liveness inferred from the process.
- **Other**: POSIX sh wrapper for whatever init system the host has.

---

## Absorbing bend2will into another Go project

Clone the repo inside your project's `tools/` dir and add a few lines:

```bash
cd your-project/tools
git clone https://github.com/bend2will/bend2will.git
```

Then create `tools/bend2will/cmd/obey/main.go` (or use the existing one) and add to your `go.mod`:

```
replace github.com/bend2will/bend2will => ./tools/bend2will
```

Your `main.go`:

```go
package main

import (
    "os"
    "github.com/bend2will/bend2will"
)

func main() {
    if err := bend2will.Guard("myapp", os.Args...); err != nil {
        bend2will.Fatal(err)
    }
}
```

Build it:

```bash
go build -o bin/myservice ./tools/bend2will/cmd/obey
```

The resulting binary is a standalone service wrapper. It accepts all the same flags as the `obey` CLI and can be installed, started, stopped, and removed as a real service.

---

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Runtime failure |
| 2 | Usage error |
