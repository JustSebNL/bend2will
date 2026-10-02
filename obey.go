// Package bend2will runs and supervises an ordinary program as a managed
// service on Windows, Linux and macOS.
//
// The problem it solves is the one that has not gone away since 2000: a program
// that knows nothing about service managers cannot be started at boot, restarted
// when it crashes, or stopped cleanly when the machine shuts down. bend2will
// puts a thin supervisor in front of such a program and takes care of all of
// that, without the program having to change at all.
//
// # The handful of lines
//
// To make an existing program into a service, add two lines to its main:
//
//	func main() {
//	    return bend2will.Guard("myapp", os.Args...)
//	}
//
// Guard reads the usual flags, supervises the command line, and translates
// whatever the platform's service manager does into a clean start and stop. To
// use a configuration file, or to tweak the defaults:
//
//	cfg := bend2will.DefaultConfig("myapp", "/usr/bin/myapp")
//	cfg.Restart = true
//	return bend2will.RunContext(ctx, cfg)
//
// # As a library
//
// Everything is reachable directly. Supervisor is the loop, Config is the
// settings, Platform is the part that differs between operating systems, and
// Logger lets diagnostics be routed into an existing logging system.
//
// # Platforms
//
//   - Windows: the Service Control Manager, via advapi32. Status is reported with
//     SetServiceStatus, stop requests arrive as SERVICE_CONTROL_STOP, and the
//     whole process tree is tracked in a job object.
//   - Linux: systemd. The generated unit is Type=notify and status is reported
//     over $NOTIFY_SOCKET, so a service is not reported running until it is.
//   - macOS: launchd. The generated plist is a LaunchDaemon; launchd has no
//     readiness protocol, so liveness is inferred from the process.
//
// Anywhere else, supervision still works and install writes a POSIX sh wrapper
// for whatever init system the host has.
//
// # Dependencies
//
// None. Only the Go standard library.
package bend2will

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// GuardContext is the shortest useful entry point. It supervises cfg until the
// service should stop, then returns.
//
//	func main() {
//	    cfg := bend2will.DefaultConfig("myapp", "/usr/bin/myapp")
//	    if err := bend2will.GuardContext(ctx, cfg); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// Guard is the same thing with a background context and a command line instead
// of a Config, which is the true two-line form:
//
//	func main() {
//	    return bend2will.Guard("myapp", os.Args...)
//	}
func GuardContext(ctx context.Context, cfg Config) error {
	return RunContext(ctx, cfg)
}

// Guard supervises the command line it is given, under the service name it is
// given, and returns when the service should stop.
func Guard(name string, args ...string) error {
	return GuardContext(context.Background(), Quick(name, args...))
}

// RunContext supervises cfg until the service should stop, then returns.
//
// It detects the platform, connects to its service manager if there is one, and
// then supervises. The returned error is non-nil only when something went wrong
// that the caller should know about; a child that fails to start, or exits with
// a non-zero code, is reported through the log and the platform, not here.
func RunContext(ctx context.Context, cfg Config) error {
	return RunWithPlatformContext(ctx, cfg, Detect())
}

// Run is RunContext with a background context, for use as a main function.
func Run(cfg Config) error { return RunContext(context.Background(), cfg) }

// RunWithPlatformContext is RunContext with an explicit platform.
//
// Use it to teach bend2will about an init system it does not know, or to control
// the lifecycle of a Platform you built yourself. The platform is not closed.
func RunWithPlatformContext(ctx context.Context, cfg Config, plat Platform) error {
	if plat == nil {
		return errors.New("bend2will: platform must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	mode, err := ParseMode(cfg.Mode)
	if err != nil {
		return err
	}

	sup, err := NewSupervisorWithPlatform(cfg, plat)
	if err != nil {
		return err
	}
	defer func() {
		if err := plat.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			// Nothing useful can be done with a close failure at this point.
			_ = err
		}
	}()

	if err := sup.Run(ctx, mode); err != nil {
		return err
	}
	// A supervised program that was stopped cleanly is not a failure of the
	// supervisor, so the exit code is surfaced through the platform rather than
	// as an error. Callers that care can use Supervisor.ExitCode.
	return nil
}

// RunWithPlatform is RunWithPlatformContext with a background context.
func RunWithPlatform(cfg Config, plat Platform) error {
	return RunWithPlatformContext(context.Background(), cfg, plat)
}

// Version is the bend2will release version.
const Version = "1.0.0"

// CommandName is the name of the command-line tool.
const CommandName = "obey"

// ServiceNameFor derives a default service name from a program path, for the
// many cases where nothing better is available.
//
// The base name is lower-cased, because Windows service names and systemd unit
// names both behave better in lower case, and any non-alphanumeric run becomes a
// dash.
func ServiceNameFor(command string) string {
	base := command
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	// Lower-case before stripping the extension, or a Windows ".EXE" survives the
	// TrimSuffix and ends up as part of the service name.
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")

	var b strings.Builder
	lastDash := false
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "service"
	}
	return name
}

// Fatal prints a message to stderr and exits with a failure status. It exists so
// that the two-line form in the package documentation stays two lines.
func Fatal(args ...any) {
	fmt.Fprintln(os.Stderr, CommandName+":", fmt.Sprint(args...))
	os.Exit(1)
}
