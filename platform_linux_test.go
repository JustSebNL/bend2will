//go:build linux

package bend2will

import "testing"

// TestLinuxDefinition checks the generated systemd unit, which is the only
// artefact of a Linux install an operator ever reads by hand.
func TestLinuxDefinition(t *testing.T) {
	cfg := DefaultConfig("myapp", "/usr/bin/myapp")
	cfg.Args = []string{"--serve", "--port", "5432"}
	cfg.Description = "My application"
	cfg.Dependencies = []string{"network-online.target", "postgresql.service"}
	cfg.Dir = "/var/lib/myapp"
	cfg.User = "myapp"
	cfg.Env = []string{"PORT=5432"}
	cfg.LogLevel = "debug"

	def, err := newLinuxPlatform().Definition(&cfg)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if def.Kind != "unit" {
		t.Errorf("Kind = %q, want %q", def.Kind, "unit")
	}
	if def.Path != "/etc/systemd/system/myapp.service" {
		t.Errorf("Path = %q, want /etc/systemd/system/myapp.service", def.Path)
	}

	for _, want := range []string{
		"Type=notify",
		"NotifyAccess=main",
		"User=myapp",
		"WorkingDirectory=/var/lib/myapp",
		"Environment=\"PORT=5432\"",
		"After=network-online.target postgresql.service",
		"ExecStart=",
		"--command /usr/bin/myapp",
		"--mode service",
		"Restart=on-failure",
		"KillMode=control-group",
		"[Install]",
		"WantedBy=multi-user.target",
	} {
		if !containsStr(def.Contents, want) {
			t.Errorf("unit is missing %q\n---\n%s", want, def.Contents)
		}
	}

	// A Type=notify unit that also lets systemd restart the child would give two
	// competing restart policies, so the unit must not ask for that.
	if containsStr(def.Contents, "Restart=always") {
		t.Errorf("unit asks systemd to restart the child as well:\n%s", def.Contents)
	}
}

// TestLinuxDefinitionEscapesBackslash guards the case where a directory ends in a
// separator, which would otherwise continue the next line in a unit file.
func TestLinuxDefinitionEscapesBackslash(t *testing.T) {
	cfg := DefaultConfig("myapp", "/usr/bin/myapp")
	cfg.Dir = `/opt/myapp\`

	def, err := newLinuxPlatform().Definition(&cfg)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if !containsStr(def.Contents, `WorkingDirectory=/opt/myapp\\`) {
		t.Errorf("trailing backslash was not escaped:\n%s", def.Contents)
	}
}

func TestSystemdStateMapping(t *testing.T) {
	cases := []struct {
		active, sub string
		want        Status
	}{
		{"active", "running", StatusRunning},
		{"active", "exited", StatusStopped},
		{"activating", "start", StatusStarting},
		{"deactivating", "stop", StatusStopping},
		{"failed", "failed", StatusFailed},
		{"inactive", "dead", StatusStopped},
		{"nonsense", "", StatusUnknown},
	}
	for _, c := range cases {
		if got := systemdState(c.active, c.sub); got != c.want {
			t.Errorf("systemdState(%q, %q) = %v, want %v", c.active, c.sub, got, c.want)
		}
	}
}

func TestSyslogPriorityMapping(t *testing.T) {
	cases := map[string]string{
		"debug": "debug",
		"trace": "debug",
		"info":  "info",
		"":      "info",
		"warn":  "warning",
		"error": "err",
	}
	for in, want := range cases {
		if got := syslogPriority(in); got != want {
			t.Errorf("syslogPriority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoundSecondsRoundsUp(t *testing.T) {
	cases := map[string]int64{
		"1500ms": 2,
		"2s":     2,
		"0s":     1,
		"1ms":    1,
	}
	for in, want := range cases {
		if got := roundSeconds(mustDuration(t, in)); got != itoa(want) {
			t.Errorf("roundSeconds(%s) = %s, want %d", in, got, want)
		}
	}
}

func containsStr(haystack, needle string) bool {
	return len(needle) == 0 || indexOfStr(haystack, needle) >= 0
}

func indexOfStr(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
