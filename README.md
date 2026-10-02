# bend2will

Run and supervise an ordinary program as a managed service on Windows, Linux and macOS.

## Quick start

```go
func main() {
    return bend2will.Guard("myapp", os.Args...)
}
```

## Installation

```bash
go get github.com/JustSebNL/bend2will
```

## Usage

### As a library

```go
cfg := bend2will.DefaultConfig("myapp", "/usr/bin/myapp")
cfg.Restart = true
return bend2will.RunContext(ctx, cfg)
```

### As a CLI

```bash
obey run myapp /usr/bin/myapp --serve
obey install myapp --command /usr/bin/myapp --arguments --serve
obey status myapp
obey stop myapp
obey start myapp
obey restart myapp
obey uninstall myapp
obey print myapp --command /usr/bin/myapp
```

### Windows service options

Windows installs can select a built-in service account with `--service-account`: `LocalSystem` is the default, and `LocalService` or `NetworkService` are also supported. Custom accounts that require passwords are not accepted. `--delayed-auto-start` enables automatic startup after regular automatic services have started. `--restart-service-on-failure` asks the Windows Service Control Manager to restart the `obey` supervisor after a failure; its default delay is five seconds and can be changed with `--service-restart-delay`. This is separate from `--restart`, which governs the supervised program. File logs can be bounded with `--log-max-bytes`; Bend2Will keeps the current log and one `.1` backup, and a value of `0` disables rotation.

```powershell
obey install myapp --command 'C:\Program Files\MyApp\myapp.exe' --service-account LocalService --enable --delayed-auto-start --restart-service-on-failure --service-restart-delay 5s
```

### Portable edition

```bash
obey portable install myapp --command /usr/bin/myapp
obey portable status myapp
```

## Configuration

Configuration files use an INI-like format:

```ini
[myapp]
command = /usr/bin/myapp
args = --serve --port 5432
restart = true
workdir = /var/lib/myapp
user = myapp
env = PORT=5432
env = MODE=production
after = network-online.target
```

Load with:

```go
cfg, err := bend2will.LoadConfigFile("/etc/myapp/obey.conf", "myapp")
```

## Platforms

- **Windows**: Service Control Manager (SCM)
- **Linux**: systemd (Type=notify)
- **macOS**: launchd (LaunchDaemon)

## License

MIT
