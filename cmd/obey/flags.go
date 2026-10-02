package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/JustSebNL/bend2will"
)

// argSeparator joins the supervised program's own arguments when they have to
// survive inside a single string.
//
// A Windows service's ImagePath is one command line, and a systemd unit or a
// launchd plist is a list of strings, so the child's argv has to be carried as
// one value. 0x1f (ASCII unit separator) is used because it cannot occur in a
// Windows path, a Unix path, or an argument a shell would accept, which makes
// the join and the split exactly reversible.
const argSeparator = "\x1f"

// options collects every command-line setting before it becomes a Config.
//
// One struct serves all the subcommands: install and print need the whole
// configuration, status needs only a name, run needs everything. Parsing into a
// single place keeps that from turning into three half-parsers.
type options struct {
	name        string
	displayName string
	description string
	command     string
	args        string
	workdir     string
	user        string
	group       string
	priority    string

	env   []string
	after []string

	log         string
	level       string
	logMaxBytes int64
	mode        string

	restart        string
	backoffInitial string
	backoffMax     string
	backoffReset   string
	backoffFactor  string

	stop          string
	stopCommand   string
	stopTimeout   string
	stopSignal    string
	startTimeout  string
	readyCommand  string
	readyInterval string

	configFile string
	unitDir    string
	enable     bool
	now        bool
	dryRun     bool
	purge      bool

	windowsServiceAccount          string
	windowsDelayedAutoStart        bool
	windowsServiceRestartOnFailure bool
	windowsServiceRestartDelay     string
}

// register wires every flag onto fs.
func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.name, "name", "", "service name (defaults to the command's base name)")
	fs.StringVar(&o.displayName, "display-name", "", "human-readable name (defaults to -name)")
	fs.StringVar(&o.description, "description", "", "one-line description of the service")
	fs.StringVar(&o.command, "command", "", "program to run (required unless -c supplies it)")
	fs.StringVar(&o.args, "arguments", "", "arguments for the program: quoted text, or a JSON array")
	fs.StringVar(&o.workdir, "workdir", "", "working directory for the program")
	fs.StringVar(&o.user, "user", "", "run the program as this user (Unix, needs root)")
	fs.StringVar(&o.group, "group", "", "run the program as this group (Unix, needs root)")
	fs.StringVar(&o.priority, "priority", "", "scheduling priority: normal, high, idle or realtime")

	fs.Func("env", "set an environment variable, KEY=VALUE; repeatable", func(v string) error {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("expected KEY=VALUE, got %q", v)
		}
		o.env = append(o.env, v)
		return nil
	})
	fs.Func("after", "a unit or service that must start first; repeatable", func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("expected a dependency name")
		}
		o.after = append(o.after, v)
		return nil
	})

	fs.StringVar(&o.log, "log", "", `where to log: "" or - for stderr, @ for the system log, or a file path`)
	fs.StringVar(&o.level, "level", "", "log level: trace, debug, info, warn, error or off")
	fs.Int64Var(&o.logMaxBytes, "log-max-bytes", 0, "rotate file logs at this size and keep one .1 backup; 0 disables rotation")
	fs.StringVar(&o.mode, "mode", "", "how to relate to the service manager: auto, service or console")

	fs.StringVar(&o.restart, "restart", "", "restart the program when it exits: true or false")
	fs.StringVar(&o.backoffInitial, "restart-initial-delay", "", "delay before the first restart (default 1s)")
	fs.StringVar(&o.backoffMax, "restart-max-delay", "", "upper bound on the restart delay (default 1m)")
	fs.StringVar(&o.backoffReset, "restart-reset-after", "", "uptime that resets the backoff (default 10m)")
	fs.StringVar(&o.backoffFactor, "restart-factor", "", "multiplier per consecutive failure (default 2)")

	fs.StringVar(&o.stop, "stop", "", "how to stop the program: signal, kill or exec")
	fs.StringVar(&o.stopCommand, "stop-command", "", "command run by -stop exec; %PID% is replaced")
	fs.StringVar(&o.stopTimeout, "stop-timeout", "", "how long a graceful stop may take (default 15s)")
	fs.StringVar(&o.stopSignal, "stop-signal", "", "signal used for a graceful stop (default TERM)")
	fs.StringVar(&o.startTimeout, "start-timeout", "", "how long a start may take (default 60s)")
	fs.StringVar(&o.readyCommand, "ready-command", "", "command that must succeed before running is reported")
	fs.StringVar(&o.readyInterval, "ready-interval", "", "how often to retry the ready command (default 500ms)")

	fs.StringVar(&o.configFile, "c", "", "read the configuration from this file")
	fs.StringVar(&o.configFile, "config", "", "read the configuration from this file")
	fs.StringVar(&o.unitDir, "unit-dir", "", "directory to write the service definition into")
	fs.BoolVar(&o.enable, "enable", false, "start the service at boot")
	fs.BoolVar(&o.now, "now", false, "start the service immediately")
	fs.BoolVar(&o.dryRun, "dry-run", false, "report what would happen without changing anything")
	fs.BoolVar(&o.purge, "purge", false, "also remove the service's saved data")
	fs.StringVar(&o.windowsServiceAccount, "service-account", "", "Windows service account: LocalSystem, LocalService or NetworkService")
	fs.BoolVar(&o.windowsDelayedAutoStart, "delayed-auto-start", false, "start after other automatic Windows services")
	fs.BoolVar(&o.windowsServiceRestartOnFailure, "restart-service-on-failure", false, "ask Windows SCM to restart obey after a service failure")
	fs.StringVar(&o.windowsServiceRestartDelay, "service-restart-delay", "", "delay before Windows SCM restarts obey (default 5s)")
}

func (o *options) installOptions() (bend2will.InstallOptions, error) {
	options := bend2will.InstallOptions{
		UnitDir:                        o.unitDir,
		DryRun:                         o.dryRun,
		Enable:                         o.enable,
		Now:                            o.now,
		WindowsServiceAccount:          o.windowsServiceAccount,
		WindowsDelayedAutoStart:        o.windowsDelayedAutoStart,
		WindowsServiceRestartOnFailure: o.windowsServiceRestartOnFailure,
	}
	if o.windowsServiceRestartDelay != "" {
		delay, err := time.ParseDuration(o.windowsServiceRestartDelay)
		if err != nil {
			return options, fmt.Errorf("--service-restart-delay: %w", err)
		}
		options.WindowsServiceRestartDelay = delay
	}
	return options, nil
}

// given reports whether a flag was explicitly provided on the command line.
//
// This is what makes precedence work: a flag that was not given must leave the
// value that came from the configuration file alone, rather than overwriting it
// with a default.
func given(fs *flag.FlagSet, names ...string) bool {
	for _, n := range names {
		found := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == n {
				found = true
			}
		})
		if found {
			return true
		}
	}
	return false
}

// config assembles a Config from the flags and, when -c was given, from a
// configuration file.
//
// Precedence is flags over file over defaults, which is what an operator expects:
// the file records the service, and the command line adjusts it for this run.
func (o *options) config(fs *flag.FlagSet, args []string) (bend2will.Config, error) {
	// Seed from DefaultConfig rather than the zero Config, so that the CLI's
	// documented defaults apply: restart on, a 15s stop timeout, info logging.
	// Starting from the zero value would silently disable restart, which is the
	// one behaviour a service wrapper exists to provide.
	cfg := bend2will.DefaultConfig("", "")
	var err error

	if o.configFile != "" {
		// With -c, a bare positional argument names the section to read.
		name := o.name
		if name == "" && len(args) > 0 {
			name = args[0]
		}
		loaded, lerr := bend2will.LoadConfigFile(o.configFile, name)
		if lerr != nil {
			return cfg, lerr
		}
		cfg = loaded
	}

	// Positional arguments are the command and its parameters, which is the
	// natural way to type an ad-hoc run.
	if !given(fs, "command") && len(args) > 0 && cfg.Command == "" {
		cfg.Command = args[0]
		if len(args) > 1 && len(cfg.Args) == 0 {
			parsed, perr := bend2will.SplitCommandLine(strings.Join(args[1:], " "))
			if perr != nil {
				return cfg, perr
			}
			cfg.Args = parsed
		}
	}

	// Settle the service name once the command is known.
	switch {
	case given(fs, "name"):
		cfg.Name = o.name
	case cfg.Name != "":
		// From the configuration file.
	case given(fs, "command") && len(args) > 0:
		// An explicit --command with a bare argument: the argument names the
		// service, as in "obey install myapp --command /usr/bin/myapp".
		cfg.Name = args[0]
	case cfg.Command != "":
		cfg.Name = bend2will.ServiceNameFor(cfg.Command)
	}

	if given(fs, "command") {
		cfg.Command = o.command
	}
	if given(fs, "display-name") {
		cfg.DisplayName = o.displayName
	}
	if given(fs, "description") {
		cfg.Description = o.description
	}
	if given(fs, "workdir") {
		cfg.Dir = o.workdir
	}
	if given(fs, "user") {
		cfg.User = o.user
	}
	if given(fs, "group") {
		cfg.Group = o.group
	}
	if given(fs, "priority") {
		cfg.Priority = o.priority
	}
	if given(fs, "log") {
		cfg.LogPath = o.log
	}
	if given(fs, "level") {
		cfg.LogLevel = o.level
	}
	if given(fs, "log-max-bytes") {
		if o.logMaxBytes < 0 {
			return cfg, fmt.Errorf("--log-max-bytes must not be negative")
		}
		cfg.LogMaxBytes = o.logMaxBytes
	}
	if given(fs, "mode") {
		cfg.Mode = o.mode
	}
	if given(fs, "stop-command") {
		cfg.StopCommand = o.stopCommand
	}
	if given(fs, "stop-signal") {
		cfg.StopSignalName = o.stopSignal
	}
	if given(fs, "ready-command") {
		cfg.ReadyCommand = o.readyCommand
	}
	if given(fs, "arguments") {
		if cfg.Args, err = bend2will.SplitArguments(o.args); err != nil {
			return cfg, fmt.Errorf("--arguments: %w", err)
		}
	}
	if len(o.env) > 0 {
		cfg.Env = append(cfg.Env, o.env...)
	}
	if len(o.after) > 0 {
		cfg.Dependencies = append(cfg.Dependencies, o.after...)
	}

	if given(fs, "restart") {
		if cfg.Restart, err = parseYesNo(o.restart); err != nil {
			return cfg, fmt.Errorf("--restart: %w", err)
		}
	}
	if given(fs, "stop") {
		if cfg.StopMethod, err = bend2will.ParseStopMethod(o.stop); err != nil {
			return cfg, fmt.Errorf("--stop: %w", err)
		}
	}
	if given(fs, "restart-factor") {
		if cfg.Backoff.Factor, err = parseFactor(o.backoffFactor); err != nil {
			return cfg, fmt.Errorf("--restart-factor: %w", err)
		}
	}

	dur := func(name string, dst *time.Duration) {
		if !given(fs, name) || err != nil {
			return
		}
		d, perr := time.ParseDuration(o.durationValue(name))
		if perr != nil {
			err = fmt.Errorf("--%s: %w", name, perr)
			return
		}
		*dst = d
	}
	dur("stop-timeout", &cfg.StopTimeout)
	dur("start-timeout", &cfg.StartTimeout)
	dur("ready-interval", &cfg.ReadyInterval)
	dur("restart-initial-delay", &cfg.Backoff.Initial)
	dur("restart-max-delay", &cfg.Backoff.Max)
	dur("restart-reset-after", &cfg.Backoff.ResetAfter)
	if err != nil {
		return cfg, err
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// durationValue returns the string value of one of the duration flags.
func (o *options) durationValue(name string) string {
	switch name {
	case "stop-timeout":
		return o.stopTimeout
	case "start-timeout":
		return o.startTimeout
	case "ready-interval":
		return o.readyInterval
	case "restart-initial-delay":
		return o.backoffInitial
	case "restart-max-delay":
		return o.backoffMax
	case "restart-reset-after":
		return o.backoffReset
	default:
		return ""
	}
}

// parseYesNo accepts the spellings of yes and no that turn up in practice.
func parseYesNo(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "y", "yes", "true", "on":
		return true, nil
	case "0", "n", "no", "false", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false, got %q", s)
	}
}

// parseFactor parses the backoff multiplier, rejecting anything that would make
// the delay shrink.
func parseFactor(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("expected a number, got %q", s)
	}
	if f < 1 {
		return 0, fmt.Errorf("must be at least 1, got %g", f)
	}
	return f, nil
}

// newFlagSet builds a flag set that reports errors through errOut rather than
// calling os.Exit, so the whole CLI stays testable in-process.
func newFlagSet(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	return fs
}
