package bend2will

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// portablePlatform installs services into a per-user location instead of the
// system one.
//
// It is a decorator: the native backend is wrapped, not forked, so the Windows,
// systemd and launchd implementations stay unaware that a portable install
// exists and there is exactly one copy of each of them. Everything that has to
// know where files live is here.
//
// What "per-user" means differs by platform, and the difference is not cosmetic:
//
//   - Linux and macOS have genuine per-user service managers. A portable install
//     is a real service, started by the user's own systemd user manager or by
//     launchd for that user, with no administrator rights needed.
//   - Windows has no per-user service manager. The Service Control Manager is
//     machine-wide and needs administrator rights, so a portable install cannot
//     be a service. It is a launcher under the user's own profile that starts
//     bend2will when the user logs in. That is the closest honest equivalent, and
//     it is what portable mode does there rather than failing at the first
//     CreateService call.
type portablePlatform struct {
	// inner is the detected native backend.
	inner Platform
	// root is the directory the service definition and any state are kept in.
	root string
	// scope names the service-manager scope the management verbs act on, such as
	// the systemd user manager or a launchd user domain.
	scope string
}

// NewPortablePlatform wraps plat so that installs land under root.
//
// The scope string is passed to the native backend's management verbs: the user
// manager on Linux, the user domain on macOS. An empty scope means the backend's
// own default.
func NewPortablePlatform(plat Platform, root, scope string) Platform {
	if root == "" {
		root = "."
	}
	return &portablePlatform{inner: plat, root: root, scope: scope}
}

// PortableRoot returns the directory a portable install uses on this system.
//
// The locations follow each platform's own convention for per-user data, so a
// portable install lands where the operating system expects user state:
//
//	Windows  %LOCALAPPDATA%\obey
//	Linux    $XDG_CONFIG_HOME/obey/systemd, or ~/.config/obey/systemd
//	macOS    ~/Library/LaunchAgents
func PortableRoot() (root string, scope string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("bend2will: locate the home directory: %w", err)
	}

	switch hostOS() {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, CommandName), "", nil

	case "darwin":
		// LaunchAgents is the per-user equivalent of LaunchDaemons, and the only
		// directory launchctl will load a job from without root.
		return filepath.Join(home, "Library", "LaunchAgents"), "user/" + currentUserName(), nil

	default:
		// The systemd user manager reads unit files from a fixed location under
		// the user's config directory, so XDG_CONFIG_HOME is honoured when set.
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, CommandName, "systemd"), "user", nil
	}
}

// currentUserName returns the login name, used to build a systemd user scope and
// to report what a portable install belongs to.
func currentUserName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	return "user"
}

// Name identifies the platform, including that it is the portable edition.
func (p *portablePlatform) Name() string { return "portable/" + p.inner.Name() }

// Root returns the directory this platform writes into.
func (p *portablePlatform) Root() string { return p.root }

// IsUserService reports whether a portable install on this platform is a real
// service managed by a per-user service manager, as opposed to a launcher.
func (p *portablePlatform) IsUserService() bool { return portableIsUserService() }

// Connect and ReportStatus are pure runtime concerns with no file locations in
// them, so they pass straight through. A portable service is still a real service
// to whatever manager started it.
func (p *portablePlatform) Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (bool, error) {
	return p.inner.Connect(ctx, cfg, mode, sup, stopper)
}

func (p *portablePlatform) ReportStatus(status Status) error { return p.inner.ReportStatus(status) }

// Definition asks the native backend for the definition and then relocates it, so
// the two can never disagree about content.
func (p *portablePlatform) Definition(cfg *Config) (Definition, error) {
	if !portableIsUserService() {
		return portableLauncherDefinition(cfg, p.root)
	}
	def, err := p.inner.Definition(cfg)
	if err != nil {
		return def, err
	}
	def.Path = filepath.Join(p.root, filepath.Base(def.Path))
	def.Instructions = portableInstructions(p.inner.Name(), p.scope, cfg.Name, def)
	return def, nil
}

// Install writes the definition under the portable root and asks the native
// backend to register it, which is what actually makes the service exist.
func (p *portablePlatform) Install(ctx context.Context, cfg *Config, opts InstallOptions) error {
	if err := opts.Validate(p.Name()); err != nil {
		return err
	}
	if opts.UnitDir == "" {
		opts.UnitDir = p.root
	}
	if err := os.MkdirAll(opts.UnitDir, 0o755); err != nil {
		return fmt.Errorf("bend2will: create the portable directory %s: %w", opts.UnitDir, err)
	}

	if !portableIsUserService() {
		return portableInstallLauncher(cfg, opts.UnitDir)
	}
	return p.inner.Install(ctx, cfg, opts)
}

// Uninstall, Start, Stop and Status act in the portable scope.
func (p *portablePlatform) Uninstall(ctx context.Context, name string, purge bool) error {
	return p.inner.Uninstall(ctx, name, purge)
}

func (p *portablePlatform) Start(ctx context.Context, name string) error {
	return p.inner.Start(ctx, name)
}

func (p *portablePlatform) Stop(ctx context.Context, name string) error {
	return p.inner.Stop(ctx, name)
}

func (p *portablePlatform) Status(ctx context.Context, name string) (ServiceStatus, error) {
	return p.inner.Status(ctx, name)
}

func (p *portablePlatform) Close() error { return p.inner.Close() }

// portableLauncherDefinition renders a launcher for a platform with no per-user
// service manager.
//
// A Windows login is the closest thing to a per-user service trigger that needs
// no administrator rights, so a portable install is a script under the user's own
// profile that starts the supervisor. It is a real, working arrangement; it is
// simply not a service, and the instructions say so.
func portableLauncherDefinition(cfg *Config, root string) (Definition, error) {
	if err := cfg.Validate(); err != nil {
		return Definition{}, err
	}
	self, err := selfExePath()
	if err != nil {
		return Definition{}, fmt.Errorf("bend2will: locate own executable: %w", err)
	}
	dir := filepath.Join(root, cfg.Name)
	path := filepath.Join(dir, launcherName(cfg))

	return Definition{
		Kind:     "launcher",
		Path:     path,
		Contents: launcherScript(cfg, self),
		Instructions: []string{
			fmt.Sprintf("copy %s into the folder %s", path, startupFolder()),
			"to start it at logon without administrator rights",
			fmt.Sprintf("or run %s now", path),
		},
	}, nil
}

// portableInstallLauncher writes the launcher script.
func portableInstallLauncher(cfg *Config, root string) error {
	def, err := portableLauncherDefinition(cfg, root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(def.Path), 0o755); err != nil {
		return fmt.Errorf("bend2will: create %s: %w", filepath.Dir(def.Path), err)
	}
	if err := os.WriteFile(def.Path, []byte(def.Contents), 0o755); err != nil {
		return fmt.Errorf("bend2will: write %s: %w", def.Path, err)
	}
	return nil
}

// portableInstructions rewrites the manual steps a native backend suggests, since
// they name the system-wide tooling.
func portableInstructions(backend, scope, name string, def Definition) []string {
	switch {
	case strings.Contains(backend, "systemd"):
		return []string{
			"systemctl --user daemon-reload",
			fmt.Sprintf("systemctl --user enable --now %s.service", name),
			fmt.Sprintf("loginctl enable-linger %s  # keep it running after you log out", currentUserName()),
		}
	case strings.Contains(backend, "launchd"):
		return []string{
			fmt.Sprintf("launchctl bootstrap %s %s", scope, def.Path),
			fmt.Sprintf("launchctl load -w %s  # for older macOS", def.Path),
		}
	default:
		return []string{
			fmt.Sprintf("arrange for your init system to run %s", def.Path),
		}
	}
}
