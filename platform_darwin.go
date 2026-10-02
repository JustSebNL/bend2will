//go:build darwin

package bend2will

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// launchd job layout. LaunchDaemons is the system-wide location, which is the
// analogue of a Windows service or a systemd system unit.
const (
	defaultLaunchdDir    = "/Library/LaunchDaemons"
	defaultLaunchdDomain = "system"
	launchdLabelPrefix   = "com.bend2will."
)

// darwinPlatform integrates with launchd.
//
// Unlike systemd and the Windows SCM, launchd has no readiness protocol: it
// considers a job started as soon as it is bootstrapped. The supervisor therefore
// reports nothing at runtime, and the plist's KeepAlive policy is what decides
// whether a job that exits should come back.
type darwinPlatform struct{}

func newDarwinPlatform() Platform { return &darwinPlatform{} }

func (p *darwinPlatform) Name() string { return "launchd" }

// Connect is a no-op beyond mode checking.
//
// There is no environment variable that unambiguously says "you are a launchd
// job": a job started by launchctl inherits an ordinary environment. A useful
// heuristic is the full-service environment variable launchd sets for daemons,
// but it is not guaranteed, so the only reliable signal is an explicit mode.
func (p *darwinPlatform) Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (bool, error) {
	if mode == ModeService {
		if os.Getenv("LAUNCH_JOB_NAME") == "" {
			return false, errors.New("bend2will: this does not look like a launchd job " +
				"(LAUNCH_JOB_NAME is unset); install and load the generated plist, " +
				"or run with mode=console")
		}
	}
	return false, nil
}

// ReportStatus is a no-op: launchd infers liveness from the process and has no
// readiness notification.
func (p *darwinPlatform) ReportStatus(Status) error { return nil }

// labelFor derives a reverse-DNS job label from a service name.
func labelFor(name string) string {
	return launchdLabelPrefix + sanitiseTag(name)
}

func plistNameFor(name string) string { return name + ".plist" }

// Definition renders a LaunchDaemon plist.
func (p *darwinPlatform) Definition(cfg *Config) (Definition, error) {
	if err := cfg.Validate(); err != nil {
		return Definition{}, err
	}
	self, err := selfExePath()
	if err != nil {
		return Definition{}, fmt.Errorf("bend2will: locate own executable: %w", err)
	}
	label := labelFor(cfg.Name)

	// argv[0] is the binary launchd executes; --command is the program bend2will
	// then supervises, which is a different thing.
	argv := []string{
		self,
		"service",
		"--name", cfg.Name,
		"--label", label,
		"--command", cfg.Command,
	}
	if len(cfg.Args) > 0 {
		argv = append(argv, "--arguments", strings.Join(cfg.Args, "\x1f"))
	}
	argv = append(argv, flattenConfigFlags(cfg, ModeService)...)

	var b strings.Builder
	b.WriteString(xmlHeader)
	fmt.Fprintf(&b, "<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", plistEscape(label))

	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range argv {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", plistEscape(a))
	}
	b.WriteString("\t</array>\n")
	if cfg.Description != "" {
		fmt.Fprintf(&b, "\t<key>Description</key>\n\t<string>%s</string>\n", plistEscape(cfg.Description))
	}

	if cfg.LogPath != "" && cfg.LogPath != "-" {
		fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", plistEscape(cfg.LogPath))
		fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", plistEscape(cfg.LogPath))
	}
	if cfg.Dir != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", plistEscape(cfg.Dir))
	}
	if cfg.User != "" {
		fmt.Fprintf(&b, "\t<key>UserName</key>\n\t<string>%s</string>\n", plistEscape(cfg.User))
	}
	if cfg.Group != "" {
		fmt.Fprintf(&b, "\t<key>GroupName</key>\n\t<string>%s</string>\n", plistEscape(cfg.Group))
	}
	if len(cfg.Env) > 0 {
		fmt.Fprintf(&b, "\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range cfg.Env {
			k, v, _ := strings.Cut(kv, "=")
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", plistEscape(k), plistEscape(v))
		}
		b.WriteString("\t</dict>\n")
	}

	// KeepAlive is about bend2will, not about the child. Restarts of the child
	// are the supervisor's job; this only recovers a supervisor that died.
	// {"SuccessfulExit": false} means "restart unless it exited cleanly", which
	// stops an intentionally disabled service from being resurrected.
	fmt.Fprintf(&b, "\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	fmt.Fprintf(&b, "\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	fmt.Fprintf(&b, "\t<key>RunAtLoad</key>\n\t<true/>\n")
	if cfg.StopTimeout > 0 {
		fmt.Fprintf(&b, "\t<key>ExitTimeOut</key>\n\t<integer>%d</integer>\n", int(cfg.StopTimeout/time.Second)+5)
	}
	b.WriteString("</dict>\n</plist>\n")

	return Definition{
		Kind:     "plist",
		Path:     filepath.Join(defaultLaunchdDir, plistNameFor(cfg.Name)),
		Contents: b.String(),
		Instructions: []string{
			fmt.Sprintf("sudo chown root:wheel %s", filepath.Join(defaultLaunchdDir, plistNameFor(cfg.Name))),
			fmt.Sprintf("sudo chmod 644 %s", filepath.Join(defaultLaunchdDir, plistNameFor(cfg.Name))),
			fmt.Sprintf("sudo launchctl bootstrap %s %s", defaultLaunchdDomain,
				filepath.Join(defaultLaunchdDir, plistNameFor(cfg.Name))),
		},
	}, nil
}

// Install writes the plist and bootstraps the job.
func (p *darwinPlatform) Install(ctx context.Context, cfg *Config, opts InstallOptions) error {
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

	target := def.Path
	if opts.UnitDir != "" {
		target = filepath.Join(opts.UnitDir, plistNameFor(cfg.Name))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("bend2will: create %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(def.Contents), 0o644); err != nil {
		return fmt.Errorf("bend2will: write %s: %w", target, err)
	}

	// bootstrap fails with EEXIST if the job is already loaded, which is not an
	// error from the operator's point of view.
	if err := launchctl(ctx, "bootstrap", defaultLaunchdDomain, target); err != nil {
		return err
	}
	if opts.Enable || opts.Now {
		if err := launchctl(ctx, "enable", defaultLaunchdDomain+"/"+labelFor(cfg.Name)); err != nil {
			return err
		}
	}
	if !opts.Now {
		// The plist sets RunAtLoad, so bootstrapping already started it. Kick it
		// anyway when explicitly asked for.
		if opts.Enable {
			return launchctl(ctx, "kickstart", "system/"+labelFor(cfg.Name))
		}
	}
	return nil
}

// Uninstall unloads the job and removes the plist.
func (p *darwinPlatform) Uninstall(ctx context.Context, name string, purge bool) error {
	label := labelFor(name)
	// bootout of a job that was never loaded is not an error worth failing on.
	_ = launchctl(ctx, "bootout", defaultLaunchdDomain+"/"+label)
	if purge {
		_ = launchctl(ctx, "disable", defaultLaunchdDomain+"/"+label)
	}
	path := filepath.Join(defaultLaunchdDir, plistNameFor(name))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("bend2will: remove %s: %w", path, err)
	}
	return nil
}

func (p *darwinPlatform) Start(ctx context.Context, name string) error {
	return launchctl(ctx, "kickstart", defaultLaunchdDomain+"/"+labelFor(name))
}

func (p *darwinPlatform) Stop(ctx context.Context, name string) error {
	return launchctl(ctx, "kill", "SIGTERM", defaultLaunchdDomain+"/"+labelFor(name))
}

// Status parses `launchctl print`, which is the only machine-readable form.
func (p *darwinPlatform) Status(ctx context.Context, name string) (ServiceStatus, error) {
	target := defaultLaunchdDomain + "/" + labelFor(name)
	out, err := exec.CommandContext(ctx, "launchctl", "print", target).CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "Could not find service") || strings.Contains(text, "No such process") {
			return ServiceStatus{Name: name, State: StatusStopped, Detail: "not loaded"}, nil
		}
		return ServiceStatus{}, fmt.Errorf("bend2will: launchctl print %s: %s: %w",
			target, strings.TrimSpace(text), err)
	}
	return parseLaunchctlPrint(name, string(out)), nil
}

func (p *darwinPlatform) Close() error { return nil }

// parseLaunchctlPrint extracts the fields we care about from `launchctl print`
// output, which is an indented key = value listing.
func parseLaunchctlPrint(name, text string) ServiceStatus {
	st := ServiceStatus{Name: name}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			st.Detail = value
			st.State = launchdState(value)
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil {
				st.MainPID = pid
			}
		}
	}
	if st.State == StatusUnknown && st.Detail == "" {
		st.Detail = "unknown"
	}
	return st
}

func launchdState(s string) Status {
	switch s {
	case "running":
		return StatusRunning
	case "waiting", "not started":
		return StatusStopped
	case "starting", "running (pending)":
		return StatusStarting
	case "stopping":
		return StatusStopping
	default:
		return StatusUnknown
	}
}

// launchctl runs launchctl and turns a non-zero exit into a readable error.
func launchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("bend2will: launchctl %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("bend2will: launchctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// xmlHeader is the conventional preamble on a hand-maintained plist.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
`

// plistEscape escapes a value for XML character data. The apostrophe and
// ampersand are the only characters that strictly need it, but the quotes and
// angle brackets are escaped too so that a hostile value cannot break out of a
// tag.
func plistEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r < 0x20 && r != '\t' && r != '\n' {
				// Control characters are not representable in a plist.
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// flattenConfigFlags renders the parts of cfg that the plist's ProgramArguments
// need, so the installed job behaves exactly like a foreground run.
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
