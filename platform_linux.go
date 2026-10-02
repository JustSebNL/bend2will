//go:build linux

package bend2will

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// systemd unit layout. The defaults match a stock systemd install; every one of
// them can be overridden for a distribution that lays things out differently.
const (
	defaultUnitDir          = "/etc/systemd/system"
	defaultSyslogIdentifier = "bend2will"
	notifyAccessEnv         = "NOTIFY_SOCKET"
)

// linuxPlatform integrates with systemd.
//
// The runtime half uses the readiness protocol (sd_notify) over the socket
// systemd passes in $NOTIFY_SOCKET, which is the only supported way for a
// service to say "I am up now" without a Type=notify/Type=notify-reload unit.
// The management half shells out to systemctl and writes unit files.
type linuxPlatform struct {
	mu        sync.Mutex
	socket    string
	connected bool
	// unset is a template/instance pair, when this process was started as a
	// systemd template unit such as foo@.bar.service.
	unitName string
}

func newLinuxPlatform() Platform { return &linuxPlatform{} }

func (p *linuxPlatform) Name() string { return "systemd" }

// Connect looks for $NOTIFY_SOCKET.
//
// Its presence is systemd's promise that this process is a service and may
// report status. If it is absent and the operator asked for service mode, that
// is an error worth reporting; if they asked for auto, we simply run as an
// ordinary process.
func (p *linuxPlatform) Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (bool, error) {
	socket := os.Getenv(notifyAccessEnv)
	if socket == "" {
		if mode == ModeService {
			return false, fmt.Errorf("bend2will: %s is not set, so this process was not started by systemd; "+
				"start the generated unit with systemctl, or run with mode=console", notifyAccessEnv)
		}
		return false, nil
	}

	// systemd supports abstract sockets by prefixing the path with '@'.
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}

	p.mu.Lock()
	p.socket = socket
	p.connected = true
	p.unitName = os.Getenv("INVOCATION_ID") // present only inside a unit
	p.mu.Unlock()

	// Fall through: the supervisor drives the child, and ReportStatus now reaches
	// systemd. sup is not invoked here because systemd, unlike the Windows SCM,
	// wants the main thread to stay in the supervision loop.
	return true, nil
}

// ReportStatus sends a readiness-protocol message.
//
// READY=1 is sent once the child is up. STOPPING=1 is sent as soon as a stop is
// requested, which is what stops systemd from reporting the unit as failed
// during a clean shutdown. Everything else is logged at debug level, because
// systemd's Type=simple units ignore it.
func (p *linuxPlatform) ReportStatus(status Status) error {
	var msg string
	switch status {
	case StatusRunning:
		msg = "READY=1\nSTATUS=supervising " + status.String()
	case StatusStopping, StatusStopped:
		msg = "STOPPING=1\nSTATUS=" + status.String()
	case StatusStarting:
		msg = "STATUS=starting"
	case StatusFailed:
		msg = "STATUS=failed"
		// ERRNO is only meaningful for a non-zero exit; 1 keeps systemd from
		// treating the notification itself as an error.
		msg += "\nERRNO=1"
	default:
		msg = "STATUS=" + status.String()
	}
	return p.notify(msg)
}

// notify writes one datagram to the readiness socket.
func (p *linuxPlatform) notify(msg string) error {
	p.mu.Lock()
	socket := p.socket
	p.mu.Unlock()

	if socket == "" {
		return nil
	}

	addr, err := net.ResolveUnixAddr("unixgram", socket)
	if err != nil {
		return fmt.Errorf("bend2will: resolve %s=%q: %w", notifyAccessEnv, socket, err)
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("bend2will: dial %s: %w", socket, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("bend2will: write to %s: %w", socket, err)
	}
	return nil
}

// unitName returns the unit file name for a service.
func unitNameFor(name string) string {
	if strings.HasSuffix(name, ".service") {
		return name
	}
	return name + ".service"
}

// unitPath returns where a unit file lives, honouring InstallOptions.UnitDir.
func (p *linuxPlatform) unitPath(name, dir string) string {
	if dir == "" {
		dir = defaultUnitDir
	}
	return filepath.Join(dir, unitNameFor(name))
}

// Definition renders a Type=notify unit.
//
// Type=notify is the important part: it tells systemd to wait for READY=1 before
// considering the service started, which is exactly the semantics the Windows
// SCM gives for free.
func (p *linuxPlatform) Definition(cfg *Config) (Definition, error) {
	if err := cfg.Validate(); err != nil {
		return Definition{}, err
	}
	self, err := selfExePath()
	if err != nil {
		return Definition{}, fmt.Errorf("bend2will: locate own executable: %w", err)
	}

	unit := unitNameFor(cfg.Name)
	path := filepath.Join(defaultUnitDir, unit)

	// The unit runs bend2will in service mode, which is what makes the runtime
	// connect to $NOTIFY_SOCKET. argv[0] is the binary the unit executes; the
	// --command that follows is the program bend2will then supervises, which is a
	// different thing and was previously the source of a real bug.
	argv := []string{
		"service",
		"--name", cfg.Name,
		"--command", cfg.Command,
	}
	if len(cfg.Args) > 0 {
		argv = append(argv, "--arguments", strings.Join(cfg.Args, "\x1f"))
	}
	argv = append(argv, flattenConfigFlags(cfg, ModeService)...)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", firstLine(cfg.Description, "managed by bend2will"))
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", escapeUnit(cfg.DisplayName))
	if deps := requireDependency(cfg.Dependencies, " "); deps != "" {
		fmt.Fprintf(&b, "After=%s\n", deps)
		fmt.Fprintf(&b, "Wants=%s\n", deps)
	}
	fmt.Fprintf(&b, "\n[Service]\n")
	fmt.Fprintf(&b, "Type=notify\n")
	fmt.Fprintf(&b, "NotifyAccess=main\n")
	fmt.Fprintf(&b, "SyslogIdentifier=%s\n", defaultSyslogIdentifier)
	if cfg.LogLevel != "" {
		fmt.Fprintf(&b, "SyslogLevel=%s\n", syslogPriority(cfg.LogLevel))
	}
	if cfg.User != "" {
		fmt.Fprintf(&b, "User=%s\n", cfg.User)
	}
	if cfg.Group != "" {
		fmt.Fprintf(&b, "Group=%s\n", cfg.Group)
	}
	if cfg.Dir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", escapeUnit(cfg.Dir))
	}
	for _, kv := range cfg.Env {
		fmt.Fprintf(&b, "Environment=%q\n", escapeUnit(kv))
	}
	// Restart= applies to bend2will itself, never to the child. Restarting the
	// child is the supervisor's job and the backoff lives in one place, so
	// letting systemd do it as well would give two competing policies.
	//
	// on-failure covers the case where the supervisor itself dies: an unexpected
	// crash gets restarted, a clean exit (an operator stop, or Restart=false
	// running out of attempts) stays down.
	fmt.Fprintf(&b, "Restart=on-failure\n")
	fmt.Fprintf(&b, "RestartSec=5\n")
	if cfg.StartTimeout > 0 {
		fmt.Fprintf(&b, "TimeoutStartSec=%s\n", roundSeconds(cfg.StartTimeout+10*time.Second))
	}
	if cfg.StopTimeout > 0 {
		// Give the child time to exit on SIGTERM before systemd escalates.
		fmt.Fprintf(&b, "TimeoutStopSec=%s\n", roundSeconds(cfg.StopTimeout+5*time.Second))
	}
	fmt.Fprintf(&b, "KillMode=control-group\n")
	fmt.Fprintf(&b, "KillSignal=SIGTERM\n")
	fmt.Fprintf(&b, "SendSIGKILL=yes\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", execStartLine(self, argv))
	fmt.Fprintf(&b, "\n[Install]\n")
	fmt.Fprintf(&b, "WantedBy=multi-user.target\n")

	return Definition{
		Kind:     "unit",
		Path:     path,
		Contents: b.String(),
		Instructions: []string{
			fmt.Sprintf("systemctl daemon-reload"),
			fmt.Sprintf("systemctl enable --now %s", unit),
		},
	}, nil
}

// Install writes the unit file and reloads systemd.
func (p *linuxPlatform) Install(ctx context.Context, cfg *Config, opts InstallOptions) error {
	if err := opts.Validate(p.Name()); err != nil {
		return err
	}
	def, err := p.Definition(cfg)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return nil
	}

	target := p.unitPath(cfg.Name, opts.UnitDir)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("bend2will: create %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(def.Contents), 0o644); err != nil {
		return fmt.Errorf("bend2will: write %s: %w", target, err)
	}

	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if opts.Enable {
		if err := systemctl(ctx, "enable", unitNameFor(cfg.Name)); err != nil {
			return err
		}
	}
	if opts.Now {
		if err := systemctl(ctx, "start", unitNameFor(cfg.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall stops, disables and removes the unit.
func (p *linuxPlatform) Uninstall(ctx context.Context, name string, purge bool) error {
	unit := unitNameFor(name)
	// Stop first: removing a running unit leaves an orphaned process behind.
	_ = systemctl(ctx, "stop", unit)
	if purge {
		if err := systemctl(ctx, "disable", unit); err != nil {
			return err
		}
	}

	removed := false
	for _, dir := range []string{defaultUnitDir, "/etc/systemd/system", "/run/systemd/system", "/usr/lib/systemd/system"} {
		path := filepath.Join(dir, unit)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("bend2will: remove %s: %w", path, err)
		}
		removed = true
	}
	_ = removed
	_ = systemctl(ctx, "daemon-reload")
	return nil
}

func (p *linuxPlatform) Start(ctx context.Context, name string) error {
	return systemctl(ctx, "start", unitNameFor(name))
}

func (p *linuxPlatform) Stop(ctx context.Context, name string) error {
	return systemctl(ctx, "stop", unitNameFor(name))
}

// Status shells out to systemctl show, which is the only supported interface.
func (p *linuxPlatform) Status(ctx context.Context, name string) (ServiceStatus, error) {
	unit := unitNameFor(name)
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=LoadState", "--property=ActiveState", "--property=SubState",
		"--property=MainPID").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return ServiceStatus{}, fmt.Errorf("bend2will: systemctl show %s: %s", unit, strings.TrimSpace(string(ee.Stderr)))
		}
		return ServiceStatus{}, fmt.Errorf("bend2will: systemctl show %s: %w", unit, err)
	}

	fields := parseSystemdProperties(string(out))
	st := ServiceStatus{Name: name, Detail: fields["SubState"]}

	if fields["LoadState"] == "not-found" {
		st.State = StatusStopped
		st.Detail = "not installed"
		return st, nil
	}
	st.State = systemdState(fields["ActiveState"], fields["SubState"])
	if pid, err := strconv.Atoi(fields["MainPID"]); err == nil {
		st.MainPID = pid
	}
	return st, nil
}

func (p *linuxPlatform) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.socket = ""
	p.connected = false
	return nil
}

// systemctl runs systemctl and turns a non-zero exit into a readable error.
func systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("bend2will: systemctl %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("bend2will: systemctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func parseSystemdProperties(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

// systemdState maps ActiveState/SubState onto a lifecycle Status.
func systemdState(active, sub string) Status {
	switch active {
	case "active":
		if sub == "exited" || sub == "dead" {
			return StatusStopped
		}
		return StatusRunning
	case "activating", "reloading":
		return StatusStarting
	case "deactivating":
		return StatusStopping
	case "failed":
		return StatusFailed
	case "inactive":
		return StatusStopped
	default:
		return StatusUnknown
	}
}

// syslogPriority maps a bend2will log level onto a syslog priority name.
func syslogPriority(level string) string {
	l, err := ParseLogLevel(level)
	if err != nil {
		return "info"
	}
	switch l {
	case LevelTrace, LevelDebug:
		return "debug"
	case LevelWarn:
		return "warning"
	case LevelError:
		return "err"
	default:
		return "info"
	}
}

// execStartLine renders ExecStart, quoting every argument that needs it.
func execStartLine(self string, argv []string) string {
	parts := make([]string, 0, len(argv))
	parts = append(parts, quoteUnitArg(self))
	for _, a := range argv {
		parts = append(parts, quoteUnitArg(a))
	}
	return strings.Join(parts, " ")
}

// quoteUnitArg quotes a single ExecStart argument. systemd's parser understands
// single and double quotes; \x1f is a literal unit separator that cannot occur in
// a real argument, so the joined value round-trips through our own splitter.
func quoteUnitArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$%") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(s[i])
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// escapeUnit escapes a value for a unit-file assignment, where a trailing
// backslash would otherwise continue the line.
func escapeUnit(s string) string {
	return strings.ReplaceAll(s, `\`, `\\`)
}

// flattenConfigFlags renders the parts of cfg that the unit's ExecStart needs to
// reproduce, so the installed service behaves exactly like a foreground run.
func flattenConfigFlags(cfg *Config, mode Mode) []string {
	var out []string
	add := func(flag, value string) {
		if value != "" {
			out = append(out, "--"+flag, value)
		}
	}
	add("workdir", cfg.Dir)
	add("log", cfg.LogPath)
	add("level", cfg.LogLevel)
	if cfg.LogMaxBytes > 0 {
		add("log-max-bytes", fmt.Sprintf("%d", cfg.LogMaxBytes))
	}
	add("mode", mode.String())
	add("restart", boolFlag(cfg.Restart))
	add("priority", cfg.Priority)
	add("stop", cfg.StopMethod.String())
	add("stop-command", cfg.StopCommand)
	add("stop-timeout", cfg.StopTimeout.String())
	add("start-timeout", cfg.StartTimeout.String())
	add("ready-command", cfg.ReadyCommand)
	add("user", cfg.User)
	add("group", cfg.Group)
	for _, kv := range cfg.Env {
		add("env", kv)
	}
	for _, d := range cfg.Dependencies {
		add("after", d)
	}
	return out
}

func boolFlag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// roundSeconds rounds a duration up to whole seconds, which is the resolution
// systemd accepts in TimeoutStopSec.
func roundSeconds(d time.Duration) string {
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

func firstLine(s, fallback string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
