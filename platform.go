package bend2will

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mode says how bend2will should relate to the platform's service manager.
type Mode int

const (
	// ModeAuto uses the service manager if this process looks like it was
	// started by one, and runs as an ordinary foreground process otherwise. This
	// is the default and is what makes one binary usable interactively and as an
	// installed service.
	ModeAuto Mode = iota

	// ModeService insists on being started by the service manager, and fails
	// otherwise. Installed services always pass this, so a misconfigured service
	// reports an error instead of silently running unsupervised.
	ModeService

	// ModeConsole never talks to the service manager, whatever the environment
	// says. Useful for running under a supervisor that is not a service manager,
	// and for tests.
	ModeConsole
)

func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeService:
		return "service"
	case ModeConsole:
		return "console"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// ParseMode maps a configuration string onto a Mode.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ModeAuto, nil
	case "service", "svc", "daemon":
		return ModeService, nil
	case "console", "cmd", "foreground", "fg":
		return ModeConsole, nil
	default:
		return ModeAuto, fmt.Errorf("bend2will: unknown mode %q (want auto, service or console)", s)
	}
}

// SuperviseFunc is the body of a supervised service. Connect runs it, on
// whichever thread the platform dictates, and its return value becomes the
// service's exit status.
type SuperviseFunc func(ctx context.Context) error

// Stopper is the platform's way of asking the supervisor to wind down.
//
// A service manager initiates a stop in the only way it has: by sending a
// control message to a process that is already running. The supervisor is the
// only thing that can act on that, so the platform is handed a Stopper and the
// supervisor is the only thing that calls Stop.
//
// Stop is safe to call any number of times, from any goroutine, including after
// the supervisor has already exited.
type Stopper struct {
	once sync.Once
	ch   chan struct{}
}

// NewStopper returns a Stopper that has not been triggered.
func NewStopper() *Stopper { return &Stopper{ch: make(chan struct{})} }

// Stop requests shutdown. It never blocks and is idempotent.
func (s *Stopper) Stop() { s.once.Do(func() { close(s.ch) }) }

// Done returns a channel closed when Stop is first called.
func (s *Stopper) Done() <-chan struct{} { return s.ch }

// Stopped reports whether Stop has been called.
func (s *Stopper) Stopped() bool {
	select {
	case <-s.ch:
		return true
	default:
		return false
	}
}

// ServiceStatus is a snapshot of a service's state, as reported by the platform.
type ServiceStatus struct {
	Name    string
	State   Status
	Detail  string
	MainPID int
}

func (s *ServiceStatus) String() string {
	pid := ""
	if s.MainPID > 0 {
		pid = fmt.Sprintf(" pid=%d", s.MainPID)
	}
	if s.Detail != "" {
		return fmt.Sprintf("%s: %s%s (%s)", s.Name, s.State, pid, s.Detail)
	}
	return fmt.Sprintf("%s: %s%s", s.Name, s.State, pid)
}

// Definition is a generated service definition: where it belongs, what goes in
// it, and what the operator still has to do by hand.
type Definition struct {
	// Kind is "unit" on systemd, "plist" on launchd, or "service" on Windows.
	Kind string
	// Path is where the definition should be installed.
	Path string
	// Contents is the definition itself.
	Contents string
	// Instructions is a short human-readable list of the remaining manual steps.
	Instructions []string
}

// InstallOptions tunes an install.
type InstallOptions struct {
	// UnitDir overrides the directory the definition is written to. It exists for
	// testing and for unusual layouts; the default is the platform's own.
	UnitDir string
	// DryRun validates everything and returns the Definition without touching the
	// filesystem or the service manager.
	DryRun bool
	// Enable asks for the service to be started at boot where the platform
	// supports it.
	Enable bool
	// Now asks for the service to be started immediately where supported.
	Now bool

	WindowsServiceAccount          string
	WindowsDelayedAutoStart        bool
	WindowsServiceRestartOnFailure bool
	WindowsServiceRestartDelay     time.Duration
}

func (o InstallOptions) Validate(platformName string) error {
	if !o.hasWindowsOptions() || platformName == "windows service control manager" {
		return nil
	}
	return fmt.Errorf("bend2will: Windows service options require the Windows Service Control Manager, got %s", platformName)
}

func (o InstallOptions) hasWindowsOptions() bool {
	return o.WindowsServiceAccount != "" || o.WindowsDelayedAutoStart ||
		o.WindowsServiceRestartOnFailure || o.WindowsServiceRestartDelay != 0
}

type installDefinitionWithOptions interface {
	definitionWithOptions(*Config, InstallOptions) (Definition, error)
}

func DefinitionForInstall(platform Platform, cfg *Config, opts InstallOptions) (Definition, error) {
	if err := opts.Validate(platform.Name()); err != nil {
		return Definition{}, err
	}
	if p, ok := platform.(installDefinitionWithOptions); ok {
		return p.definitionWithOptions(cfg, opts)
	}
	return platform.Definition(cfg)
}

// Platform is everything that differs between operating systems.
//
// The runtime -- supervision, restart, logging, configuration -- is entirely
// portable. A Platform only translates status into the native vocabulary of the
// operating system, and knows how to write, install and remove a service
// definition.
//
// Implement this interface if you need to teach bend2will about an init system
// it does not know; pass the result to RunWithPlatform.
type Platform interface {
	// Name identifies the platform, for logs and diagnostics.
	Name() string

	// Connect prepares the platform before supervision starts.
	//
	// It returns handled=true when this process is genuinely being managed by
	// the platform's service manager and supervision should proceed through
	// ReportStatus. It returns handled=false when the service manager is not
	// involved, in which case the caller supervises as an ordinary process.
	// An error is a real failure that should stop startup.
	//
	// sup is the supervision loop. A platform that owns the calling thread --
	// the Windows SCM does -- must arrange for sup to run, and must call
	// stopper.Stop() when its service manager asks for a stop.
	Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (handled bool, err error)

	// ReportStatus publishes a lifecycle transition. Errors are logged and
	// otherwise ignored: a service that is running must not die because its
	// status could not be reported.
	ReportStatus(status Status) error

	// Definition renders the service definition for this platform.
	Definition(cfg *Config) (Definition, error)

	// Install writes the service definition and registers it.
	Install(ctx context.Context, cfg *Config, opts InstallOptions) error

	// Uninstall removes the service definition and deregisters it.
	Uninstall(ctx context.Context, name string, purge bool) error

	// Start and Stop control an installed service.
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error

	// Status reports the current state of an installed service.
	Status(ctx context.Context, name string) (ServiceStatus, error)

	// Close releases any resources held by the platform.
	Close() error
}

// Detect returns the Platform for the operating system this binary was built
// for. It is defined per GOOS, in detect_*.go, because the constructors it
// selects between live behind mutually exclusive build tags and Go's type
// checker resolves those tags before the linker does.
func Detect() Platform { return newPlatform() }

// requireDependency renders a dependency list for a unit file or plist,
// preserving the order the operator wrote and dropping duplicates.
func requireDependency(deps []string, sep string) string {
	seen := make(map[string]bool, len(deps))
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return strings.Join(out, sep)
}

// sortedKeys returns map keys in a stable order, for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
