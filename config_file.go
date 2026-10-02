package bend2will

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// configFile is a parsed configuration file: an ordered set of sections, each a
// set of directives.
//
// The format is an INI subset, because it is the format operators already
// know and a new one would not be worth learning:
//
//	# a comment, or a ; comment
//	[myapp]
//	command = /usr/bin/myapp
//	args    = --serve --port 5432
//	restart = true
//
// A file may hold several services. LoadConfig returns the section matching the
// requested name; an empty name takes the first section, or a [default] section
// if there is one.
type configFile struct {
	path     string
	order    []string
	sections map[string]map[string]string
}

const defaultSectionName = "default"

// LoadConfigFile reads a configuration file and returns the section named
// serviceName.
//
// A section named "default" is used when serviceName is empty, or when no
// section matches and a "default" section exists. That lets a single-service
// configuration file omit the [name] header entirely.
func LoadConfigFile(path, serviceName string) (Config, error) {
	cf, err := parseConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	return cf.Config(serviceName)
}

// parseConfigFile reads and parses a configuration file.
func parseConfigFile(path string) (*configFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bend2will: open configuration file: %w", err)
	}
	defer f.Close()

	cf, err := parseConfig(f, path)
	if err != nil {
		return nil, err
	}
	return cf, nil
}

// parseConfig parses a configuration from a reader. path is used only in error
// messages.
func parseConfig(r io.Reader, path string) (*configFile, error) {
	cf := &configFile{path: path, sections: map[string]map[string]string{}}

	// A file with no [section] header at all is a single anonymous section, so
	// that the simplest possible file needs no header. The anonymous section is
	// created lazily, on the first directive, so that a file which does use
	// headers does not end up with a stray empty section shadowing them.
	var current string
	started := false
	ensure := func() {
		if !started {
			cf.ensure(current)
			started = true
		}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())

		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}

		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unterminated section header %q", path, lineNo, line)
			}
			current = strings.TrimSpace(line[1:end])
			if current == "" {
				return nil, fmt.Errorf("%s:%d: empty section name", path, lineNo)
			}
			// Discard an anonymous section that turned out to be only comments or
			// blank lines, rather than leaving an empty one to be picked later.
			started = false
			cf.ensure(current)
			started = true
			continue
		}

		ensure()

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			// A bare word is a reasonable shorthand for a boolean flag, which is
			// what people actually write.
			key, value = line, "true"
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			return nil, fmt.Errorf("%s:%d: directive with no name", path, lineNo)
		}

		s := cf.sections[current]
		if _, dup := s[key]; dup {
			if !repeatableDirectives[key] {
				return nil, fmt.Errorf("%s:%d: directive %q appears twice in section [%s]; "+
					"only %s may be repeated", path, lineNo, key, current, strings.Join(sortedRepeatables(), ", "))
			}
			// A repeatable directive accumulates. The values are joined with NUL,
			// which cannot occur in a configuration line, and the handler splits
			// them back apart.
			s[key] = s[key] + "\x00" + value
			continue
		}
		s[key] = value
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("bend2will: read %s: %w", path, err)
	}
	return cf, nil
}

// ensure returns the named section, creating it if necessary.
func (cf *configFile) ensure(name string) map[string]string {
	if s, ok := cf.sections[name]; ok {
		return s
	}
	s := map[string]string{}
	cf.sections[name] = s
	cf.order = append(cf.order, name)
	return s
}

// repeatableDirectives are the directives that may appear more than once in a
// section. Everything else appearing twice is a mistake and is reported rather
// than silently taking the last value, which is the failure that costs an
// afternoon.
var repeatableDirectives = map[string]bool{
	"env":        true,
	"after":      true,
	"depends_on": true,
}

// sortedRepeatables names the repeatable directives, for an error message.
func sortedRepeatables() []string {
	out := make([]string, 0, len(repeatableDirectives))
	for k := range repeatableDirectives {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Names returns the section names, in the order they appear in the file.
func (cf *configFile) Names() []string { return append([]string(nil), cf.order...) }

// Config turns a section into a Config.
//
// Every directive in the section is applied on top of cfg, so a caller can supply
// a base configuration and let the file override only what it mentions.
func (cf *configFile) Config(serviceName string) (Config, error) {
	section := serviceName
	if section == "" {
		// Prefer an explicit [default] section, then the first section that
		// actually holds directives, which for a file with no headers at all is
		// the anonymous one.
		if s, ok := cf.sections[defaultSectionName]; ok && len(s) > 0 {
			section = defaultSectionName
		} else {
			for _, name := range cf.order {
				if len(cf.sections[name]) > 0 {
					section = name
					break
				}
			}
		}
	}

	directives, ok := cf.sections[section]
	if !ok {
		return Config{}, fmt.Errorf("bend2will: %s has no section [%s]%s", cf.path, section, cf.available())
	}
	if len(directives) == 0 {
		return Config{}, fmt.Errorf("bend2will: %s section [%s] is empty", cf.path, section)
	}

	cfg := DefaultConfig(section, "")
	cfg.Mode = ""
	if err := applyDirectives(&cfg, directives, cf.path, section); err != nil {
		return cfg, err
	}
	// An anonymous section has no name of its own, so derive one from the command
	// the same way the command line does. Leaving it empty would make the Config
	// unusable, and Validate rightly refuses one.
	if cfg.Name == "" && cfg.Command != "" {
		cfg.Name = ServiceNameFor(cfg.Command)
	}
	// Validate applies the defaults, including stripping the ">" truncate prefix
	// from a log path, so the Config returned to a caller is always fully
	// populated rather than half-built.
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (cf *configFile) available() string {
	if len(cf.order) == 0 {
		return ""
	}
	return " (available: " + strings.Join(cf.order, ", ") + ")"
}

// directiveHandler applies one directive.
type directiveHandler func(cfg *Config, value string) error

// directives maps a configuration-file key to its meaning.
//
// Keys are lowercase and use underscores. The hyphenated spellings are accepted
// too, because that is what people type.
var directives = map[string]directiveHandler{
	"name":        func(c *Config, v string) error { c.Name = v; return nil },
	"displayname": func(c *Config, v string) error { c.DisplayName = v; return nil },
	"description": func(c *Config, v string) error { c.Description = v; return nil },

	"command":   func(c *Config, v string) error { c.Command = v; return nil },
	"args":      func(c *Config, v string) error { return setArgs(c, v) },
	"arguments": func(c *Config, v string) error { return setArgs(c, v) },
	"env":       func(c *Config, v string) error { c.Env = append(c.Env, splitAccumulated(v)...); return nil },
	"workdir":   func(c *Config, v string) error { c.Dir = v; return nil },
	"user":      func(c *Config, v string) error { c.User = v; return nil },
	"group":     func(c *Config, v string) error { c.Group = v; return nil },
	"priority":  func(c *Config, v string) error { c.Priority = v; return nil },

	"mode":  func(c *Config, v string) error { c.Mode = v; return nil },
	"log":   func(c *Config, v string) error { c.LogPath = v; return nil },
	"level": func(c *Config, v string) error { c.LogLevel = v; return nil },
	"log_max_bytes": func(c *Config, v string) error {
		maxBytes, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || maxBytes < 0 {
			return fmt.Errorf("bend2will: log_max_bytes must be a non-negative integer, got %q", v)
		}
		c.LogMaxBytes = maxBytes
		return nil
	},
	"debug": func(c *Config, v string) error { c.LogLevel = v; return nil },

	"restart": func(c *Config, v string) error { return setBool(c, v, &c.Restart) },
	"restart_initial_delay": func(c *Config, v string) error {
		return setDuration(c, v, &c.Backoff.Initial)
	},
	"restart_max_delay": func(c *Config, v string) error {
		return setDuration(c, v, &c.Backoff.Max)
	},
	"restart_reset_after": func(c *Config, v string) error {
		return setDuration(c, v, &c.Backoff.ResetAfter)
	},
	"restart_factor": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 1 {
			return fmt.Errorf("bend2will: restart_factor must be a number of at least 1, got %q", v)
		}
		c.Backoff.Factor = f
		return nil
	},

	"stop":          func(c *Config, v string) error { return setStopMethod(c, v) },
	"stop_command":  func(c *Config, v string) error { c.StopCommand = v; return nil },
	"stop_timeout":  func(c *Config, v string) error { return setDuration(c, v, &c.StopTimeout) },
	"stop_signal":   func(c *Config, v string) error { c.StopSignalName = v; return nil },
	"start_timeout": func(c *Config, v string) error { return setDuration(c, v, &c.StartTimeout) },

	"ready_command":  func(c *Config, v string) error { c.ReadyCommand = v; return nil },
	"ready_interval": func(c *Config, v string) error { return setDuration(c, v, &c.ReadyInterval) },

	"after": func(c *Config, v string) error {
		c.Dependencies = append(c.Dependencies, fields(joinAccumulated(v))...)
		return nil
	},
	"depends_on": func(c *Config, v string) error {
		c.Dependencies = append(c.Dependencies, fields(joinAccumulated(v))...)
		return nil
	},
}

// applyDirectives applies every directive in a section, in a defined order.
//
// The order matters: command and args must be set before anything that validates,
// and mode before anything that behaves differently per mode. Sorting the keys
// gives a stable, explainable order rather than Go's random map iteration.
func applyDirectives(cfg *Config, values map[string]string, path, section string) error {
	// Directives that must land before the rest, in this order. The command has to
	// be settled before anything that validates, and the arguments before the
	// command-line flag handling, so that a file mixing spellings still ends up
	// coherent.
	ordered := []string{"name", "command", "args", "arguments", "mode", "stop"}
	for _, key := range ordered {
		v, ok := values[key]
		if !ok {
			continue
		}
		if err := directives[key](cfg, v); err != nil {
			return annotate(err, path, section, key)
		}
	}

	rest := make([]string, 0, len(values))
	for k := range values {
		if contains(ordered, k) {
			continue
		}
		rest = append(rest, k)
	}
	sort.Strings(rest)

	for _, key := range rest {
		handler, ok := lookupDirective(key)
		if !ok {
			return fmt.Errorf("bend2will: %s section [%s]: unknown directive %q", path, section, key)
		}
		if err := handler(cfg, values[key]); err != nil {
			return annotate(err, path, section, key)
		}
	}
	return nil
}

// contains reports whether needle is in haystack.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// lookupDirective resolves a directive name, accepting dashes as well as
// underscores so that both `stop-timeout` and `stop_timeout` work.
func lookupDirective(key string) (directiveHandler, bool) {
	if h, ok := directives[key]; ok {
		return h, true
	}
	h, ok := directives[strings.ReplaceAll(key, "-", "_")]
	return h, ok
}

func annotate(err error, path, section, key string) error {
	return fmt.Errorf("bend2will: %s section [%s] directive %q: %w", path, section, key, err)
}

// argSeparator joins the supervised program's own arguments when they have to
// survive inside a single value.
//
// A Windows service's ImagePath is one command line, a systemd unit and a launchd
// plist each carry one string per argument, and a Windows login launcher is a
// .cmd line. In every one of those the child's argv has to be carried as a single
// value, and 0x1f (ASCII unit separator) is used because it cannot occur in a
// Windows path, a Unix path, or an argument a shell would accept. That makes the
// join and the split exactly reversible.
const argSeparator = "\x1f"

// SplitArguments parses an arguments setting into an argv.
//
// Three forms are accepted. A value containing the internal 0x1f separator is
// split on it, which is how a generated service definition, unit file or launcher
// hands the program's arguments back to the supervisor intact. Plain text is
// split with shell-like quoting, so
//
//	--port 5432 --name "my app"
//
// yields three arguments. A JSON array is accepted for the cases shell quoting
// cannot express, such as an argument that genuinely contains a backslash:
//
//	["--port", "5432", "--pattern", "C:\new\table"]
func SplitArguments(v string) ([]string, error) {
	if strings.Contains(v, argSeparator) {
		return strings.Split(v, argSeparator), nil
	}
	var cfg Config
	if err := setArgs(&cfg, v); err != nil {
		return nil, err
	}
	return cfg.Args, nil
}

// setArgs splits an arguments directive into an argv.
func setArgs(c *Config, v string) error {
	if strings.Contains(v, argSeparator) {
		// A generated definition, unit file or launcher hands the arguments back
		// as one separator-joined value.
		c.Args = strings.Split(v, argSeparator)
		return nil
	}
	if strings.TrimSpace(v) == "" {
		c.Args = nil
		return nil
	}
	// A JSON-style array is accepted too, for arguments that contain spaces and
	// would otherwise need shell quoting.
	if strings.HasPrefix(strings.TrimSpace(v), "[") {
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err != nil {
			return fmt.Errorf("expected a JSON array of strings, got %q", v)
		}
		c.Args = arr
		return nil
	}
	args, err := splitCommandLine(v)
	if err != nil {
		return err
	}
	c.Args = args
	return nil
}

// setBool parses a yes/no value.
func setBool(c *Config, v string, dst *bool) error {
	b, err := parseYesNo(v)
	if err != nil {
		return err
	}
	*dst = b
	return nil
}

// setDuration parses a duration into dst, accepting Go durations and bare
// numbers interpreted as seconds.
func setDuration(c *Config, v string, dst *time.Duration) error {
	d, err := parseDuration(v)
	if err != nil {
		return err
	}
	*dst = d
	return nil
}

func setStopMethod(c *Config, v string) error {
	m, err := ParseStopMethod(v)
	if err != nil {
		return err
	}
	c.StopMethod = m
	return nil
}

// parseYesNo accepts the many spellings of yes and no that appear in hand-written
// configuration files.
func parseYesNo(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "y", "yes", "true", "on", "enable", "enabled":
		return true, nil
	case "0", "n", "no", "false", "off", "disable", "disabled", "":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no, got %q", s)
	}
}

// parseDuration accepts a Go duration, or a bare number of seconds.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("expected a duration such as 30s, 5m or 1h, got %q", s)
	}
	return d, nil
}

// splitAccumulated undoes the NUL joining that repeated directives go through.
func splitAccumulated(v string) []string {
	return strings.Split(v, "\x00")
}

// joinAccumulated turns an accumulated value back into plain text, for a
// directive that itself splits on whitespace.
func joinAccumulated(v string) string {
	return strings.Join(splitAccumulated(v), " ")
}

// fields splits a whitespace- or comma-separated list.
func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '\n' || r == '\r'
	})
}
