package bend2will

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StopMethod selects how the supervisor asks a child process to exit.
type StopMethod int

const (
	// StopSignal delivers a termination signal to the child only. The supervisor
	// does not wait for the child to exit before falling back to StopKill.
	StopSignal StopMethod = iota

	// StopKill terminates the process tree unconditionally.
	StopKill

	// StopExec runs a user-supplied shutdown command after signalling, and waits
	// for it to finish before escalating to StopKill.
	StopExec
)

func (m StopMethod) String() string {
	switch m {
	case StopSignal:
		return "signal"
	case StopKill:
		return "kill"
	case StopExec:
		return "exec"
	default:
		return fmt.Sprintf("StopMethod(%d)", int(m))
	}
}

// ParseStopMethod maps a configuration string onto a StopMethod.
//
// The signal spellings are accepted as aliases of signal, because
// stop=sigterm is what people reach for out of habit.
func ParseStopMethod(s string) (StopMethod, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "signal", "sig", "term", "sigterm":
		return StopSignal, nil
	case "kill", "terminate", "brutal":
		return StopKill, nil
	case "exec", "command", "cmd":
		return StopExec, nil
	default:
		return StopSignal, fmt.Errorf("unknown stop method %q (want signal, kill or exec)", s)
	}
}

// Backoff describes the delay applied after a child exits.
type Backoff struct {
	// Initial is the delay after the first consecutive failure.
	Initial time.Duration
	// Max caps the delay. Zero means MaxBackoff.
	Max time.Duration
	// ResetAfter is how long a child must stay alive before the counter is
	// cleared and the delay drops back to Initial. Zero means forever.
	ResetAfter time.Duration
	// Factor multiplies the delay after each failure. Values below 1 are treated
	// as 1 (no backoff).
	Factor float64
}

const (
	defaultBackoffInitial = time.Second
	defaultBackoffMax     = time.Minute
	defaultBackoffReset   = 10 * time.Minute
	defaultBackoffFactor  = 2.0
)

func (b Backoff) withDefaults() Backoff {
	if b.Initial <= 0 {
		b.Initial = defaultBackoffInitial
	}
	if b.Max <= 0 {
		b.Max = defaultBackoffMax
	}
	if b.Max < b.Initial {
		b.Max = b.Initial
	}
	if b.ResetAfter <= 0 {
		b.ResetAfter = defaultBackoffReset
	}
	if b.Factor < 1 {
		b.Factor = defaultBackoffFactor
	}
	return b
}

// delay returns the wait after the given (1-based) consecutive failure.
func (b Backoff) delay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := b.Initial
	for i := 1; i < failures; i++ {
		next := time.Duration(float64(d) * b.Factor)
		if next <= d || next > b.Max {
			// Guard against overflow and against overshooting Max.
			return b.Max
		}
		d = next
	}
	if d > b.Max {
		d = b.Max
	}
	return d
}

// Config describes one supervised program and how it should be run.
//
// The zero value is not useful; start from DefaultConfig and adjust, or use
// Config.Quick which fills in everything from a command line.
type Config struct {
	// Name identifies the service. It is required.
	Name string

	// DisplayName is a human-readable label. Defaults to Name.
	DisplayName string

	// Description is a human-readable description.
	Description string

	// Command is the program to run. Required.
	Command string

	// Args are the arguments passed to Command. They are passed through
	// verbatim: no shell is involved, so quoting and redirection are the
	// caller's responsibility.
	Args []string

	// Env holds extra environment variables, in "KEY=VALUE" form. Entries that
	// already exist in Config.Env replace the inherited value.
	Env []string

	// Dir is the working directory for the child. Empty means inherit.
	Dir string

	// User and Group switch the child to a different user when bend2will runs
	// as root. They are ignored when not running as root, and are unsupported
	// on Windows.
	User  string
	Group string

	// Mode says how to relate to the platform's service manager: auto, service
	// or console. See Mode. The empty string means auto.
	Mode string

	// LogPath appends log output to this file. "-" means stderr, "@" means the
	// platform's system log (syslog / Event Log / ASL). Empty means inherit the
	// supervisor's own stderr, which is usually what a service manager wants.
	//
	// The special prefix ">" truncates the file instead of appending.
	LogPath     string
	LogMaxBytes int64

	// LogLevel is one of: trace, debug, info, warn, error.
	LogLevel string

	// Restart controls what happens when the child exits.
	Restart bool
	Backoff Backoff

	// StartTimeout bounds how long a single start attempt may take, including
	// the ReadyCommand. Zero means no bound.
	StartTimeout time.Duration

	// StopTimeout bounds how long a graceful stop may take before the process
	// tree is killed.
	StopTimeout time.Duration

	// StopMethod selects the stop strategy. The zero value is StopSignal.
	StopMethod StopMethod

	// StopCommand is run when StopMethod is StopExec. The literal token %PID% is
	// replaced with the child's process id.
	StopCommand string

	// StopSignalName overrides the termination signal sent on a graceful stop.
	// Defaults to SIGTERM on Unix and CTRL_BREAK_EVENT on Windows.
	StopSignalName string

	// Priority is the scheduling class of the child: normal, high, idle or
	// realtime. Lowering a child's priority always works; raising it requires
	// privileges and fails the start on Unix.
	Priority string

	// ReadyCommand is run once after the child starts; when it exits zero the
	// service is reported as running. Empty means "report running immediately".
	ReadyCommand string

	// ReadyInterval is how often the supervisor re-checks the child while it is
	// waiting for ReadyCommand to succeed. Defaults to 500ms.
	ReadyInterval time.Duration

	// Dependencies are platform-specific units or services that must start
	// first.
	Dependencies []string
}

// DefaultConfig returns a Config with sensible defaults for every optional
// field. Name and Command must still be set.
func DefaultConfig(name, command string) Config {
	return Config{
		Name:          name,
		DisplayName:   name,
		Command:       command,
		Restart:       true,
		StopMethod:    StopSignal,
		StopTimeout:   15 * time.Second,
		StartTimeout:  60 * time.Second,
		ReadyInterval: 500 * time.Millisecond,
		LogLevel:      "info",
		Backoff:       Backoff{}.withDefaults(),
	}
}

// Quick is the short form used by the handful-of-lines API. It derives a Config
// from a name and a command line, expanding the leading element of args as the
// command and passing the rest through.
func Quick(name string, args ...string) Config {
	c := DefaultConfig(name, "")
	if len(args) > 0 {
		c.Command = args[0]
		c.Args = append([]string(nil), args[1:]...)
	}
	return c
}

// ErrNotConfigured is returned when a Config is missing a required field.
var ErrNotConfigured = errors.New("bend2will: configuration is incomplete")

// Validate reports the first problem with the Config, or nil.
func (c *Config) Validate() error {
	c.applyDefaults()

	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: Name is required", ErrNotConfigured)
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("%w: Command is required", ErrNotConfigured)
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		return err
	}
	if c.LogMaxBytes < 0 {
		return errors.New("bend2will: LogMaxBytes must not be negative")
	}
	if c.StopMethod == StopExec && strings.TrimSpace(c.StopCommand) == "" {
		return errors.New("bend2will: stop method \"exec\" requires a StopCommand")
	}
	if c.StopTimeout < 0 {
		return errors.New("bend2will: StopTimeout must not be negative")
	}
	if c.ReadyInterval < 0 {
		return errors.New("bend2will: ReadyInterval must not be negative")
	}
	for _, kv := range c.Env {
		if !strings.Contains(kv, "=") {
			return fmt.Errorf("bend2will: malformed environment entry %q (want KEY=VALUE)", kv)
		}
	}
	return nil
}

// applyDefaults fills in every unset optional field in place.
func (c *Config) applyDefaults() {
	if c.DisplayName == "" {
		c.DisplayName = c.Name
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.ReadyInterval <= 0 {
		c.ReadyInterval = 500 * time.Millisecond
	}
	if c.StopTimeout == 0 {
		c.StopTimeout = 15 * time.Second
	}
	if c.StartTimeout == 0 {
		c.StartTimeout = 60 * time.Second
	}
	if strings.HasPrefix(c.LogPath, ">") {
		c.LogPath = strings.TrimSpace(c.LogPath[1:])
	}
	c.Backoff = c.Backoff.withDefaults()
}

// LogFileMode returns the file mode used when creating the log file.
func (c *Config) LogFileMode() os.FileMode {
	if c.User != "" {
		return 0o640
	}
	return 0o644
}

// CommandLine renders the command and arguments as a single string, for logs and
// for platforms that need a command line rather than an argv vector (Windows).
func (c *Config) CommandLine() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, quoteArg(c.Command))
	for _, a := range c.Args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// quoteArg quotes a single argument using Windows conventions, which is the
// strictest of the three target platforms.
func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		n := 0
		for i < len(s) && s[i] == '\\' {
			i++
			n++
		}
		if i == len(s) {
			b.WriteString(strings.Repeat(`\`, n*2))
			break
		}
		if s[i] == '"' {
			b.WriteString(strings.Repeat(`\`, n*2+1))
		} else {
			b.WriteString(strings.Repeat(`\`, n))
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// resolveCommand makes Command absolute when possible, so that a service whose
// working directory later disappears can still be started. It is best effort:
// an unresolvable command is left alone for the OS loader to report on.
func (c *Config) resolveCommand() {
	if c.Command == "" {
		return
	}
	if filepath.IsAbs(c.Command) {
		return
	}
	if looksLikePath(c.Command) {
		if abs, err := filepath.Abs(c.Command); err == nil {
			c.Command = abs
		}
		return
	}
	// Bare name: consult PATH the same way a shell would.
	if found, err := lookPath(c.Command); err == nil && found != "" {
		c.Command = found
	}
}

func looksLikePath(s string) bool {
	if strings.ContainsAny(s, `/\`) {
		return true
	}
	if len(s) > 4 && strings.EqualFold(s[len(s)-4:], ".exe") {
		return true
	}
	return false
}
