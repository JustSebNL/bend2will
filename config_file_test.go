package bend2will

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestConfigFileFullSection(t *testing.T) {
	path := writeTemp(t, "obey.conf", `
# a comment
; another comment

[myapp]
command    = /usr/bin/myapp
args       = --serve --port 5432
restart    = yes
level      = debug
workdir    = /var/lib/myapp
user       = myapp
env        = PORT=5432
env        = MODE=production
after      = network-online.target postgresql.service
priority   = idle
	stop       = exec
	stop_command = /usr/local/bin/myapp-stop %PID%
stop_timeout = 45s
ready_command = /usr/local/bin/myapp-ready
restart_initial_delay = 2s
restart_max_delay = 30s
restart_factor = 3

[other]
command = /usr/bin/other
`)

	cfg, err := LoadConfigFile(path, "myapp")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Command", cfg.Command, "/usr/bin/myapp"},
		{"Dir", cfg.Dir, "/var/lib/myapp"},
		{"User", cfg.User, "myapp"},
		{"LogLevel", cfg.LogLevel, "debug"},
		{"Restart", cfg.Restart, true},
		{"Priority", cfg.Priority, "idle"},
		{"StopMethod", cfg.StopMethod, StopExec},
		{"StopCommand", cfg.StopCommand, "/usr/local/bin/myapp-stop %PID%"},
		{"StopTimeout", cfg.StopTimeout, 45 * time.Second},
		{"ReadyCommand", cfg.ReadyCommand, "/usr/local/bin/myapp-ready"},
		{"Backoff.Initial", cfg.Backoff.Initial, 2 * time.Second},
		{"Backoff.Max", cfg.Backoff.Max, 30 * time.Second},
		{"Backoff.Factor", cfg.Backoff.Factor, 3.0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if want := []string{"--serve", "--port", "5432"}; !equalStrings(cfg.Args, want) {
		t.Errorf("Args = %q, want %q", cfg.Args, want)
	}
	if want := []string{"PORT=5432", "MODE=production"}; !equalStrings(cfg.Env, want) {
		t.Errorf("Env = %q, want %q", cfg.Env, want)
	}
	if want := []string{"network-online.target", "postgresql.service"}; !equalStrings(cfg.Dependencies, want) {
		t.Errorf("Dependencies = %q, want %q", cfg.Dependencies, want)
	}
}

func TestConfigFileSectionsAreIndependent(t *testing.T) {
	path := writeTemp(t, "obey.conf", `
[alpha]
command = /bin/alpha
level  = warn

[beta]
command = /bin/beta
`)

	alpha, err := LoadConfigFile(path, "alpha")
	if err != nil {
		t.Fatalf("alpha: %v", err)
	}
	if alpha.Command != "/bin/alpha" {
		t.Errorf("alpha.Command = %q", alpha.Command)
	}
	if alpha.LogLevel != "warn" {
		t.Errorf("alpha.LogLevel = %q, want warn", alpha.LogLevel)
	}

	beta, err := LoadConfigFile(path, "beta")
	if err != nil {
		t.Fatalf("beta: %v", err)
	}
	if beta.Command != "/bin/beta" {
		t.Errorf("beta.Command = %q", beta.Command)
	}
	// beta does not set level, so it must get the default rather than alpha's.
	if beta.LogLevel != "info" {
		t.Errorf("beta.LogLevel = %q, want the default info", beta.LogLevel)
	}
}

func TestConfigFileMissingSectionIsAnError(t *testing.T) {
	path := writeTemp(t, "obey.conf", "[alpha]\ncommand = /bin/alpha\n")
	_, err := LoadConfigFile(path, "nope")
	if err == nil {
		t.Fatal("loading a missing section should fail")
	}
	// The message should list what is available, which is the difference between
	// a two-second and a two-minute diagnosis.
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("error should mention the available sections, got %q", err)
	}
}

func TestConfigFileHeaderlessIsASingleService(t *testing.T) {
	path := writeTemp(t, "obey.conf", "command = /usr/bin/thing\nrestart = no\n")

	cfg, err := LoadConfigFile(path, "")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Command != "/usr/bin/thing" {
		t.Errorf("Command = %q", cfg.Command)
	}
	// A file with no [name] header has no name of its own, so one is derived from
	// the command exactly as the command line derives it.
	if cfg.Name != "thing" {
		t.Errorf("Name = %q, want it derived from the command as %q", cfg.Name, "thing")
	}
	if cfg.Restart {
		t.Error("Restart should be false")
	}
}

func TestConfigFileDefaultSectionWinsForEmptyName(t *testing.T) {
	path := writeTemp(t, "obey.conf", `
[default]
command = /bin/default
level = error

[other]
command = /bin/other
`)
	cfg, err := LoadConfigFile(path, "")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Command != "/bin/default" {
		t.Errorf("Command = %q, want the default section", cfg.Command)
	}
}

func TestConfigFileErrors(t *testing.T) {
	cases := map[string]struct {
		content string
		wantErr string
	}{
		"unterminated section": {"[broken\ncommand = /bin/x\n", "unterminated section"},
		"empty section name":   {"[]\ncommand = /bin/x\n", "empty section"},
		"unknown directive":    {"[s]\ncommand = /bin/x\nnonsense = 1\n", "unknown directive"},
		"duplicate directive":  {"[s]\ncommand = /bin/x\ncommand = /bin/y\n", "twice"},
		"bad duration":         {"[s]\ncommand = /bin/x\nstop_timeout = soonish\n", "duration"},
		"bad boolean":          {"[s]\ncommand = /bin/x\nrestart = maybe\n", "yes or no"},
		"bad stop method":      {"[s]\ncommand = /bin/x\nstop = politely\n", "unknown stop method"},
		"exec without command": {"[s]\ncommand = /bin/x\nstop = exec\n", "StopCommand"},
		"empty section":        {"[s]\n[s2]\ncommand = /bin/x\n", "empty"},
		"bad factor":           {"[s]\ncommand = /bin/x\nrestart_factor = 0.5\n", "at least 1"},
	}

	for name, c := range cases {
		path := writeTemp(t, "bad.conf", c.content)
		_, err := LoadConfigFile(path, "s")
		if err == nil {
			t.Errorf("%s: expected an error, got none", name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error %q does not mention %q", name, err, c.wantErr)
		}
	}
}

// TestConfigFileExplicitlyNamedEmptySectionIsAnError covers naming a section that
// exists but holds nothing, which is a different mistake from naming one that
// does not exist at all.
func TestConfigFileExplicitlyNamedEmptySectionIsAnError(t *testing.T) {
	path := writeTemp(t, "obey.conf", "[s]\n\n[s2]\ncommand = /bin/x\n")
	_, err := LoadConfigFile(path, "s")
	if err == nil {
		t.Fatal("naming an empty section should fail")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should say the section is empty", err)
	}
}

func TestConfigFileQuotedValues(t *testing.T) {
	path := writeTemp(t, "obey.conf", `
[s]
command = "/path with spaces/thing"
description = 'single quoted'
args = ["--a", "--b c"]
`)
	cfg, err := LoadConfigFile(path, "s")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Command != "/path with spaces/thing" {
		t.Errorf("Command = %q", cfg.Command)
	}
	if cfg.Description != "single quoted" {
		t.Errorf("Description = %q", cfg.Description)
	}
	if want := []string{"--a", "--b c"}; !equalStrings(cfg.Args, want) {
		t.Errorf("Args = %q, want %q", cfg.Args, want)
	}
}

func TestConfigFileBareWordIsTrue(t *testing.T) {
	path := writeTemp(t, "obey.conf", "[s]\ncommand = /bin/x\nrestart\n")
	cfg, err := LoadConfigFile(path, "s")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if !cfg.Restart {
		t.Error("a bare directive should mean true")
	}
}

func TestConfigFileAcceptsDashesInKeys(t *testing.T) {
	path := writeTemp(t, "obey.conf", "[s]\ncommand = /bin/x\nstop-timeout = 20s\nready-command = /bin/ready\n")
	cfg, err := LoadConfigFile(path, "s")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.StopTimeout != 20*time.Second {
		t.Errorf("StopTimeout = %s, want 20s", cfg.StopTimeout)
	}
	if cfg.ReadyCommand != "/bin/ready" {
		t.Errorf("ReadyCommand = %q", cfg.ReadyCommand)
	}
}

func TestConfigFileRepeatedEnvIsNotADuplicate(t *testing.T) {
	// env is meant to repeat, which the duplicate-directive check must allow.
	path := writeTemp(t, "obey.conf", "[s]\ncommand = /bin/x\nenv = A=1\nenv = B=2\n")
	cfg, err := LoadConfigFile(path, "s")
	if err != nil {
		t.Fatalf("repeated env should be allowed: %v", err)
	}
	if len(cfg.Env) != 2 {
		t.Errorf("Env = %q, want two entries", cfg.Env)
	}
}

func TestConfigFileTruncationPrefixOnLog(t *testing.T) {
	path := writeTemp(t, "obey.conf", "[s]\ncommand = /bin/x\nlog = >/var/log/x.log\n")
	cfg, err := LoadConfigFile(path, "s")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.LogPath != "/var/log/x.log" {
		t.Errorf("LogPath = %q, want the > prefix stripped", cfg.LogPath)
	}
}

func TestConfigFileMissingFileIsAnError(t *testing.T) {
	_, err := LoadConfigFile(filepath.Join(t.TempDir(), "nope.conf"), "")
	if err == nil {
		t.Error("loading a missing file should fail")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
