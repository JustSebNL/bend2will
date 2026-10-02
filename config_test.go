package bend2will

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBackoffDelayGrowsAndSaturates(t *testing.T) {
	b := Backoff{
		Initial:    time.Second,
		Max:        8 * time.Second,
		Factor:     2,
		ResetAfter: time.Minute,
	}
	want := []time.Duration{
		1 * time.Second, // first failure
		2 * time.Second, //
		4 * time.Second, //
		8 * time.Second, // reaches Max
		8 * time.Second, // saturated
		8 * time.Second, // still saturated
	}
	for i, w := range want {
		if got := b.delay(i + 1); got != w {
			t.Errorf("delay(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestBackoffDelayZeroForNoFailures(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Minute, Factor: 2}
	if got := b.delay(0); got != 0 {
		t.Errorf("delay(0) = %s, want 0", got)
	}
	if got := b.delay(-5); got != 0 {
		t.Errorf("delay(-5) = %s, want 0", got)
	}
}

// TestBackoffDoesNotOverflow guards the multiplication loop, which is where a
// naive exponential backoff overflows int64 and wraps negative.
func TestBackoffDoesNotOverflow(t *testing.T) {
	b := Backoff{Initial: time.Nanosecond, Max: time.Hour, Factor: 1.5}
	for i := 1; i <= 500; i++ {
		d := b.delay(i)
		if d < 0 {
			t.Fatalf("delay(%d) = %s, which is negative: the backoff overflowed", i, d)
		}
		if d > b.Max {
			t.Fatalf("delay(%d) = %s, which exceeds Max %s", i, d, b.Max)
		}
	}
}

func TestBackoffDefaultsAreApplied(t *testing.T) {
	b := Backoff{}.withDefaults()
	if b.Initial != time.Second {
		t.Errorf("Initial = %s, want 1s", b.Initial)
	}
	if b.Max != time.Minute {
		t.Errorf("Max = %s, want 1m", b.Max)
	}
	if b.ResetAfter != 10*time.Minute {
		t.Errorf("ResetAfter = %s, want 10m", b.ResetAfter)
	}
	if b.Factor != 2 {
		t.Errorf("Factor = %v, want 2", b.Factor)
	}
}

// TestBackoffMaxNeverBelowInitial covers a misconfiguration that would otherwise
// make the very first restart wait longer than the cap.
func TestBackoffMaxNeverBelowInitial(t *testing.T) {
	b := Backoff{Initial: 30 * time.Second, Max: time.Second, Factor: 2}.withDefaults()
	if b.Max < b.Initial {
		t.Errorf("Max %s is below Initial %s", b.Max, b.Initial)
	}
}

func TestBackoffFactorBelowOneIsClamped(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Hour, Factor: 0.5}.withDefaults()
	if b.Factor < 1 {
		t.Errorf("Factor = %v, want at least 1", b.Factor)
	}
	if got := b.delay(3); got <= b.delay(1) {
		t.Errorf("delay is not increasing: delay(1)=%s delay(3)=%s", b.delay(1), got)
	}
}

func TestStatusTransitions(t *testing.T) {
	legal := [][2]Status{
		{StatusUnknown, StatusStarting},
		{StatusUnknown, StatusRunning},
		{StatusUnknown, StatusStopped},
		{StatusStarting, StatusRunning},
		{StatusStarting, StatusStopping},
		{StatusRunning, StatusStopping},
		{StatusRunning, StatusStopped},
		{StatusStopping, StatusStopped},
		{StatusRunning, StatusFailed},
		// Staying put is always allowed.
		{StatusRunning, StatusRunning},
	}
	for _, p := range legal {
		if !p[0].CanTransitionTo(p[1]) {
			t.Errorf("%s -> %s should be legal", p[0], p[1])
		}
	}

	illegal := [][2]Status{
		{StatusUnknown, StatusStopping}, // nothing to stop
		{StatusStopped, StatusRunning},  // a stopped service does not come back
		{StatusStopped, StatusStarting},
		{StatusFailed, StatusRunning},
		{StatusRunning, StatusUnknown}, // unknown is not a state to move to
	}
	for _, p := range illegal {
		if p[0].CanTransitionTo(p[1]) {
			t.Errorf("%s -> %s should be illegal", p[0], p[1])
		}
	}
}

// TestStatusStartingToFailedIsLegal records a deliberate decision: a start that
// never completes is a failure, not just another attempt.
func TestStatusStartingToFailedIsLegal(t *testing.T) {
	if !StatusStarting.CanTransitionTo(StatusFailed) {
		t.Error("starting -> failed should be legal")
	}
}

func TestStatusTerminal(t *testing.T) {
	if !StatusStopped.IsTerminal() {
		t.Error("StatusStopped should be terminal")
	}
	if !StatusFailed.IsTerminal() {
		t.Error("StatusFailed should be terminal")
	}
	if StatusRunning.IsTerminal() {
		t.Error("StatusRunning should not be terminal")
	}
}

func TestStatusTrackerIgnoresIllegalTransition(t *testing.T) {
	var seen []Status
	tr := newStatusTracker(func(s Status) { seen = append(seen, s) })

	var illegal [][2]Status
	tr.set(StatusStarting, nil)
	tr.set(StatusRunning, nil)
	// Running -> Starting is not a legal move.
	ok := tr.set(StatusStarting, func(from, to Status) { illegal = append(illegal, [2]Status{from, to}) })

	if ok {
		t.Error("an illegal transition should not be applied")
	}
	if tr.current0() != StatusRunning {
		t.Errorf("current = %s, want running: an illegal transition must not move the state", tr.current0())
	}
	if len(illegal) != 1 || illegal[0][0] != StatusRunning || illegal[0][1] != StatusStarting {
		t.Errorf("illegal callback got %v, want one [running starting]", illegal)
	}
	if len(seen) != 2 {
		t.Errorf("emitted %v, want two transitions", seen)
	}
}

func TestStopperIsIdempotent(t *testing.T) {
	s := NewStopper()
	if s.Stopped() {
		t.Error("a fresh Stopper should not be stopped")
	}
	s.Stop()
	s.Stop() // must not panic on a closed channel
	s.Stop()

	if !s.Stopped() {
		t.Error("Stopper should be stopped")
	}
	select {
	case <-s.Done():
	default:
		t.Error("Done should be closed after Stop")
	}
}

func TestValidateRequiresNameAndCommand(t *testing.T) {
	c := Config{}
	if err := c.Validate(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Validate with nothing set = %v, want ErrNotConfigured", err)
	}

	c = Config{Command: "/bin/true"}
	if err := c.Validate(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Validate with no Name = %v, want ErrNotConfigured", err)
	}

	c = Config{Name: "svc"}
	if err := c.Validate(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Validate with no Command = %v, want ErrNotConfigured", err)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Config){
		"bad log level":    func(c *Config) { c.LogLevel = "shouty" },
		"bad env entry":    func(c *Config) { c.Env = []string{"NOEQUALS"} },
		"exec without cmd": func(c *Config) { c.StopMethod = StopExec },
		"negative timeout": func(c *Config) { c.StopTimeout = -time.Second },
	}
	for name, mutate := range cases {
		cfg := DefaultConfig("svc", "/bin/true")
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it, want an error", name)
		}
	}
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	cfg := DefaultConfig("svc", "/bin/true")
	cfg.Env = []string{"A=1", "B=2"}
	cfg.StopMethod = StopSignal
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected a good config: %v", err)
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := map[string]Level{
		"":        LevelInfo,
		"info":    LevelInfo,
		"INFO":    LevelInfo,
		" trace":  LevelTrace,
		"debug":   LevelDebug,
		"warn":    LevelWarn,
		"WARNING": LevelWarn,
		"error":   LevelError,
		"off":     LevelOff,
	}
	for in, want := range cases {
		got, err := ParseLogLevel(in)
		if err != nil {
			t.Errorf("ParseLogLevel(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLogLevel("nonsense"); err == nil {
		t.Error("ParseLogLevel should reject an unknown level")
	}
}

func TestParseStopMethod(t *testing.T) {
	cases := map[string]StopMethod{
		"":          StopSignal,
		"signal":    StopSignal,
		"SIGTERM":   StopSignal,
		"kill":      StopKill,
		"terminate": StopKill,
		"exec":      StopExec,
		"command":   StopExec,
		" Command":  StopExec,
	}
	for in, want := range cases {
		got, err := ParseStopMethod(in)
		if err != nil {
			t.Errorf("ParseStopMethod(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseStopMethod(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseStopMethod("nope"); err == nil {
		t.Error("ParseStopMethod should reject an unknown method")
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":        ModeAuto,
		"auto":    ModeAuto,
		"service": ModeService,
		"svc":     ModeService,
		"daemon":  ModeService,
		"console": ModeConsole,
		"CMD":     ModeConsole,
	}
	for in, want := range cases {
		got, err := ParseMode(in)
		if err != nil {
			t.Errorf("ParseMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseMode("sideways"); err == nil {
		t.Error("ParseMode should reject an unknown mode")
	}
}

func TestParsePriority(t *testing.T) {
	cases := map[string]Priority{
		"":         PriorityNormal,
		"normal":   PriorityNormal,
		"high":     PriorityHigh,
		"idle":     PriorityIdle,
		"low":      PriorityIdle,
		"realtime": PriorityRealtime,
		"real":     PriorityRealtime,
	}
	for in, want := range cases {
		got, err := ParsePriority(in)
		if err != nil {
			t.Errorf("ParsePriority(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePriority(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParsePriority("extreme"); err == nil {
		t.Error("ParsePriority should reject an unknown priority")
	}
}

func TestPercentPID(t *testing.T) {
	if got := percentPID("stop %PID% now", 4242); got != "stop 4242 now" {
		t.Errorf("percentPID = %q", got)
	}
	if got := percentPID("stop %PID% now", 0); got != "stop %PID% now" {
		t.Errorf("percentPID with no pid = %q, want the token left alone", got)
	}
	if got := percentPID("nothing to replace", 7); got != "nothing to replace" {
		t.Errorf("percentPID = %q", got)
	}
}

func TestServiceNameFor(t *testing.T) {
	cases := map[string]string{
		`/usr/bin/myapp`:                      "myapp",
		`C:\Program Files\my.exe`:             "my",
		`C:\Program Files\My App.EXE`:         "my-app",
		`postgres`:                            "postgres",
		`/usr/lib/postgresql/14/bin/postgres`: "postgres",
		``:                                    "service",
		`///`:                                 "service",
		`/usr/bin/my_app_2`:                   "my-app-2",
	}
	for in, want := range cases {
		if got := ServiceNameFor(in); got != want {
			t.Errorf("ServiceNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandLineQuotesArguments(t *testing.T) {
	c := DefaultConfig("svc", "/usr/bin/myapp")
	c.Args = []string{"--serve", "--name", "my app", `has"quote`, ""}

	got := c.CommandLine()
	for _, want := range []string{`--serve`, `--name`, `"my app"`, `""`} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandLine %q is missing %s", got, want)
		}
	}
}

// TestCommandLineRoundTripsThroughWindowsParser is in config_windows_test.go
// because commandLineToArgvW is defined in split_windows.go behind a build tag.

// TestSplitCommandLineRoundTrips covers the shell grammar, which is what
// configuration files, readiness commands and shutdown commands are written in.
func TestSplitCommandLineRoundTrips(t *testing.T) {
	cases := [][]string{
		{"a", "b", "c"},
		{"one two", "three"},
		{`with\ space`},
		{`trailing\`},
		{""},
		{"tab\there"},
	}
	for _, want := range cases {
		joined := shellJoin(want)
		got, err := SplitCommandLine(joined)
		if err != nil {
			t.Errorf("SplitCommandLine(%q): %v", joined, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("SplitCommandLine(%q) = %q, want %q", joined, got, want)
		}
	}
}

func TestSplitCommandLineRejectsUnterminatedQuote(t *testing.T) {
	if _, err := SplitCommandLine(`unterminated "quote`); err == nil {
		t.Error("SplitCommandLine should reject an unterminated quote")
	}
}

// shellJoin renders arguments using shell quoting, the inverse of
// SplitCommandLine. It is the test's own helper, not library code.
func shellJoin(args []string) string {
	var parts []string
	for _, a := range args {
		if a == "" {
			parts = append(parts, `""`)
			continue
		}
		if !strings.ContainsAny(a, " \t\\\"") {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a)+`"`)
	}
	return strings.Join(parts, " ")
}
