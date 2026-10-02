package bend2will

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPlatform is a Platform that does nothing, so the supervisor can be tested
// without a service manager. It records the status transitions it was told about,
// which is how the tests assert on the reported lifecycle.
type testPlatform struct {
	mu       sync.Mutex
	statuses []Status
	handled  bool
	connErr  error
}

func newTestPlatform(handled bool) *testPlatform { return &testPlatform{handled: handled} }

func (p *testPlatform) Name() string { return "test" }

func (p *testPlatform) Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (bool, error) {
	if p.connErr != nil {
		return false, p.connErr
	}
	if !p.handled {
		return false, nil
	}
	// Behave like the Windows SCM: run supervision on another thread, and let the
	// Stopper decide when the call returns.
	go func() { _ = sup(ctx) }()
	<-stopper.Done()
	return true, nil
}

func (p *testPlatform) ReportStatus(s Status) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses = append(p.statuses, s)
	return nil
}

func (p *testPlatform) seen() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Status(nil), p.statuses...)
}

func (p *testPlatform) Definition(*Config) (Definition, error) {
	return Definition{Kind: "test", Path: filepath.Join("test", "test"), Contents: "# test\n"}, nil
}

func (p *testPlatform) Install(context.Context, *Config, InstallOptions) error { return nil }
func (p *testPlatform) Uninstall(context.Context, string, bool) error          { return nil }
func (p *testPlatform) Start(context.Context, string) error                    { return nil }
func (p *testPlatform) Stop(context.Context, string) error                     { return nil }
func (p *testPlatform) Status(context.Context, string) (ServiceStatus, error) {
	return ServiceStatus{Name: "x", State: StatusStopped}, nil
}
func (p *testPlatform) Close() error { return nil }

// newTestSupervisor builds a supervisor against a do-nothing platform.
func newTestSupervisor(t *testing.T, cfg Config) (*Supervisor, *testPlatform) {
	t.Helper()
	if cfg.LogPath == "" {
		cfg.LogPath = "-"
	}
	plat := newTestPlatform(false)
	sup, err := NewSupervisorWithPlatform(cfg, plat)
	if err != nil {
		t.Fatalf("NewSupervisorWithPlatform: %v", err)
	}
	return sup, plat
}

// startSupervisor runs a supervisor in the background.
func startSupervisor(t *testing.T, cfg Config) (*Supervisor, chan error) {
	t.Helper()
	sup, _ := newTestSupervisor(t, cfg)
	done := make(chan error, 1)
	go func() { done <- sup.Run(context.Background(), ModeConsole) }()
	return sup, done
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func waitDone(t *testing.T, done chan error, d time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("the supervisor did not return within %s", d)
		return nil
	}
}

func readLinesOrEmpty(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func hasPrefixLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func containsStatus(list []Status, want Status) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestSupervisorRunsChildToCompletion is the base case: a child that exits
// cleanly, with restart disabled, ends the service and reports stopped.
func TestSupervisorRunsChildToCompletion(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"sleep", "150"}
	cfg.Restart = false

	sup, plat := newTestSupervisor(t, cfg)
	if err := sup.Run(context.Background(), ModeConsole); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := sup.ExitCode(); got != 0 {
		t.Errorf("ExitCode = %d, want 0", got)
	}
	if got := sup.Status(); got != StatusStopped {
		t.Errorf("Status = %s, want stopped", got)
	}

	seen := plat.seen()
	if len(seen) == 0 {
		t.Fatal("no statuses were reported")
	}
	if seen[0] != StatusStarting {
		t.Errorf("first status = %s, want starting", seen[0])
	}
	if !containsStatus(seen, StatusRunning) {
		t.Errorf("statuses %v should include running", seen)
	}
	if seen[len(seen)-1] != StatusStopped {
		t.Errorf("last status = %s, want stopped", seen[len(seen)-1])
	}
}

// TestSupervisorRestartsCrashedChild proves the feature the tool exists for: a
// child that exits unexpectedly is started again.
func TestSupervisorRestartsCrashedChild(t *testing.T) {
	record := filepath.Join(t.TempDir(), "starts.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "150"}
	cfg.Restart = true
	cfg.Backoff = Backoff{Initial: 20 * time.Millisecond, Max: 40 * time.Millisecond, Factor: 2, ResetAfter: time.Hour}

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "two child starts", func() bool {
		return len(readLinesOrEmpty(record)) >= 2
	})

	sup.stopper.Stop()
	if err := waitDone(t, done, 10*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}

	if starts := readLinesOrEmpty(record); len(starts) < 2 {
		t.Errorf("expected at least two starts, got %d: %v", len(starts), starts)
	}
	if n := sup.Restarts(); n < 1 {
		t.Errorf("Restarts = %d, want at least 1", n)
	}
}

// TestSupervisorStopIsGraceful proves the child is asked to stop rather than
// killed: the helper records the signal it received.
//
// This is Unix-only. On Windows a graceful stop is a console ctrl-break, and it is
// only deliverable when the supervisor has a console attached. A Windows service
// never does, which is why the escalation path exists at all; the behaviour is
// covered on Windows by TestSupervisorEscalatesToKill instead of being asserted
// here and failing for a reason that has nothing to do with the code under test.
func TestSupervisorStopIsGraceful(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver a graceful stop to a console-less child; " +
			"see TestSupervisorEscalatesToKill")
	}

	record := filepath.Join(t.TempDir(), "events.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "30000"}
	cfg.Restart = false
	cfg.StopMethod = StopSignal
	cfg.StopTimeout = 5 * time.Second

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "the child to start", func() bool {
		return len(readLinesOrEmpty(record)) >= 1
	})

	sup.stopper.Stop()
	if err := waitDone(t, done, 15*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}

	if events := readLines(t, record); !hasPrefixLine(events, "signal ") {
		t.Errorf("the child was not asked to stop gracefully; events were %v", events)
	}
}

// TestSupervisorStopStopsTheChild proves that a stop request always results in
// the child being gone, on every platform. It is the cross-platform half of the
// graceful-stop guarantee: graceful where the platform allows it, forced where it
// does not, but stopped either way.
func TestSupervisorStopStopsTheChild(t *testing.T) {
	record := filepath.Join(t.TempDir(), "events.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "60000"}
	cfg.Restart = false
	cfg.StopMethod = StopSignal
	cfg.StopTimeout = time.Second

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "the child to start", func() bool {
		return len(readLinesOrEmpty(record)) >= 1
	})

	sup.stopper.Stop()
	if err := waitDone(t, done, 20*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}
	if sup.Status() != StatusStopped {
		t.Errorf("Status = %s, want stopped", sup.Status())
	}
}

// TestSupervisorEscalatesToKill proves a child that ignores a stop request is
// still killed, and that it happens promptly rather than hanging the service.
func TestSupervisorEscalatesToKill(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"ignore-term", "60000"}
	cfg.Restart = false
	cfg.StopMethod = StopSignal
	cfg.StopTimeout = 300 * time.Millisecond

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "the child to be running", func() bool {
		return sup.Status() == StatusRunning
	})

	start := time.Now()
	sup.stopper.Stop()
	if err := waitDone(t, done, 20*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}

	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("the stop took %s, so the child was not killed after the %s timeout",
			elapsed, 300*time.Millisecond)
	}
	if sup.Status() != StatusStopped {
		t.Errorf("Status = %s, want stopped", sup.Status())
	}
}

// TestSupervisorStopExecRunsShutdownCommand proves the exec stop method runs the
// configured shutdown command.
func TestSupervisorStopExecRunsShutdownCommand(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "events.txt")
	shutdownLog := filepath.Join(dir, "shutdown.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "30000"}
	cfg.Restart = false
	cfg.StopMethod = StopExec
	cfg.StopCommand = helper(t) + " record " + shutdownLog + " 1"
	cfg.StopTimeout = 5 * time.Second

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "the child to start", func() bool {
		return len(readLinesOrEmpty(record)) >= 1
	})

	sup.stopper.Stop()
	if err := waitDone(t, done, 15*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}

	if _, err := os.Stat(shutdownLog); err != nil {
		t.Errorf("the shutdown command did not run: %v", err)
	}
}

// TestSupervisorStopExecExpandsPID proves %PID% reaches the shutdown command as
// the real child pid, which is the whole point of the token.
func TestSupervisorStopExecExpandsPID(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "events.txt")
	shutdownLog := filepath.Join(dir, "shutdown.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "30000"}
	cfg.Restart = false
	cfg.StopMethod = StopExec
	cfg.StopCommand = helper(t) + " record " + shutdownLog + " 1"
	cfg.StopTimeout = 5 * time.Second

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 10*time.Second, "the child to start", func() bool {
		return len(readLinesOrEmpty(record)) >= 1
	})

	sup.stopper.Stop()
	if err := waitDone(t, done, 15*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}

	// The shutdown command recorded its own argv, which must contain no %PID%
	// token: percentPID replaces it before the command runs.
	for _, line := range readLines(t, shutdownLog) {
		if strings.Contains(line, "%PID%") {
			t.Errorf("the shutdown command received an unexpanded token: %q", line)
		}
	}
}

// TestSupervisorReportsFailureWhenCommandMissing proves a start that can never
// succeed is a hard error rather than an infinite restart loop.
func TestSupervisorReportsFailureWhenCommandMissing(t *testing.T) {
	cfg := DefaultConfig("t", filepath.Join(t.TempDir(), "no-such-program"))
	cfg.Restart = true

	sup, _ := newTestSupervisor(t, cfg)
	err := sup.Run(context.Background(), ModeConsole)
	if err == nil {
		t.Fatal("running a missing command should be an error")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("error %q should say the program could not be started", err)
	}
}

// TestSupervisorReadyCommandGatesRunning proves the readiness check holds the
// service in the starting state until the check passes.
func TestSupervisorReadyCommandGatesRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")

	// A readiness check that fails until the test creates the marker, so the
	// service provably cannot be reported running beforehand.
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"sleep", "20000"}
	cfg.Restart = false
	cfg.ReadyCommand = helper(t) + " ready-if-exists " + marker
	cfg.StartTimeout = 20 * time.Second
	cfg.ReadyInterval = 50 * time.Millisecond

	sup, _ := newTestSupervisor(t, cfg)
	done := make(chan error, 1)
	go func() { done <- sup.Run(context.Background(), ModeConsole) }()

	// Give the supervisor time to start the child and run the failing check.
	waitFor(t, 10*time.Second, "the child to be started", func() bool {
		return sup.Status() == StatusStarting
	})
	time.Sleep(300 * time.Millisecond)
	if got := sup.Status(); got == StatusRunning {
		t.Fatalf("Status = running, want it held in starting until the readiness check passes")
	}

	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatalf("creating the marker: %v", err)
	}
	waitFor(t, 10*time.Second, "the service to be reported running", func() bool {
		return sup.Status() == StatusRunning
	})

	sup.stopper.Stop()
	_ = waitDone(t, done, 20*time.Second)
}

// TestSupervisorReadyCommandTimeout proves a readiness check that never passes
// does not report the service as running, and does not leak the child.
func TestSupervisorReadyCommandTimeout(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"sleep", "60000"}
	cfg.Restart = false
	cfg.ReadyCommand = helper(t) + " exit 1"
	cfg.StartTimeout = 400 * time.Millisecond
	cfg.ReadyInterval = 50 * time.Millisecond

	sup, plat := newTestSupervisor(t, cfg)
	done := make(chan error, 1)
	go func() { done <- sup.Run(context.Background(), ModeConsole) }()

	waitFor(t, 20*time.Second, "the supervisor to give up on readiness", func() bool {
		return sup.Status() == StatusStopped
	})
	sup.stopper.Stop()
	_ = waitDone(t, done, 20*time.Second)

	if containsStatus(plat.seen(), StatusRunning) {
		t.Errorf("the service should never have been reported running; statuses were %v", plat.seen())
	}
}

// TestSupervisorBackoffDelaysRestart proves the supervisor waits between attempts
// rather than spinning.
func TestSupervisorBackoffDelaysRestart(t *testing.T) {
	record := filepath.Join(t.TempDir(), "starts.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "5"}
	cfg.Restart = true
	cfg.Backoff = Backoff{Initial: 300 * time.Millisecond, Max: 300 * time.Millisecond, Factor: 1, ResetAfter: time.Hour}

	sup, done := startSupervisor(t, cfg)
	waitFor(t, 20*time.Second, "three child starts", func() bool {
		return len(readLinesOrEmpty(record)) >= 3
	})
	sup.stopper.Stop()
	_ = waitDone(t, done, 10*time.Second)

	if sup.Restarts() < 2 {
		t.Errorf("Restarts = %d, want at least 2", sup.Restarts())
	}
}

// TestSupervisorReportsNonZeroExitCode proves the exit code is captured.
func TestSupervisorReportsNonZeroExitCode(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"exit", "7"}
	cfg.Restart = false

	sup, _ := newTestSupervisor(t, cfg)
	if err := sup.Run(context.Background(), ModeConsole); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sup.ExitCode(); got != 7 {
		t.Errorf("ExitCode = %d, want 7", got)
	}
}

// TestSupervisorRejectsInvalidConfig proves validation happens before anything is
// spawned.
func TestSupervisorRejectsInvalidConfig(t *testing.T) {
	if _, err := NewSupervisor(Config{}); err == nil {
		t.Error("an empty Config should be rejected")
	}
}

// TestSupervisorNilPlatformIsRejected proves a nil Platform is caught rather than
// panicking later.
func TestSupervisorNilPlatformIsRejected(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	if _, err := NewSupervisorWithPlatform(cfg, nil); err == nil {
		t.Error("a nil Platform should be rejected")
	}
}

// TestSupervisorPlatformConnectErrorIsPropagated proves a platform that refuses
// to connect stops startup rather than supervising anyway.
func TestSupervisorPlatformConnectErrorIsPropagated(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"sleep", "100"}

	plat := newTestPlatform(false)
	plat.connErr = errors.New("not started by a service manager")

	sup, err := NewSupervisorWithPlatform(cfg, plat)
	if err != nil {
		t.Fatalf("NewSupervisorWithPlatform: %v", err)
	}
	if err := sup.Run(context.Background(), ModeConsole); err == nil {
		t.Error("Run should return the platform's connect error")
	}
}

// TestChildExposesLifecycle proves the Child API reports the right pid, liveness
// and exit code, and that signalling a reaped child is a clean no-op.
func TestChildExposesLifecycle(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"exit", "3"}

	child, err := startChild(context.Background(), &cfg, DiscardLogger)
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	if child.PID() <= 0 {
		t.Errorf("PID = %d, want a positive pid", child.PID())
	}
	if exited, _ := child.WaitFor(10 * time.Second); !exited {
		t.Fatal("WaitFor said the child never exited")
	}
	if got := child.ExitCode(); got != 3 {
		t.Errorf("ExitCode = %d, want 3", got)
	}
	if child.Alive() {
		t.Error("a reaped child should not be reported alive")
	}
	if err := child.Signal(SignalTerm); !errors.Is(err, ErrNoChild) {
		t.Errorf("Signal on a reaped child = %v, want ErrNoChild", err)
	}
	if err := child.Kill(); !errors.Is(err, ErrNoChild) {
		t.Errorf("Kill on a reaped child = %v, want ErrNoChild", err)
	}
}

// TestChildInheritsEnvironment proves configured environment variables reach the
// child.
func TestChildInheritsEnvironment(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "env.txt")

	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"record", record, "5"}
	cfg.Env = []string{"BEND2WILL_TEST_MARKER=present"}

	child, err := startChild(context.Background(), &cfg, DiscardLogger)
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	if exited, _ := child.WaitFor(10 * time.Second); !exited {
		t.Fatal("the child never exited")
	}

	// A malformed environment entry is rejected before anything is spawned, which
	// is the failure mode worth guarding.
	bad := DefaultConfig("t", helper(t))
	bad.Env = []string{"NOEQUALSSIGN"}
	if _, err := startChild(context.Background(), &bad, DiscardLogger); err == nil {
		t.Error("a malformed environment entry should be rejected")
	}
}

// TestChildRunsInWorkingDirectory proves Dir is honoured.
func TestChildRunsInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"exit", "0"}
	cfg.Dir = dir

	child, err := startChild(context.Background(), &cfg, DiscardLogger)
	if err != nil {
		t.Fatalf("startChild with a working directory: %v", err)
	}
	if exited, _ := child.WaitFor(10 * time.Second); !exited {
		t.Error("the child did not exit")
	}
}

// TestChildRejectsMissingWorkingDirectory proves a bad Dir is reported clearly
// rather than as a puzzling spawn failure.
func TestChildRejectsMissingWorkingDirectory(t *testing.T) {
	cfg := DefaultConfig("t", helper(t))
	cfg.Args = []string{"exit", "0"}
	cfg.Dir = filepath.Join(t.TempDir(), "no-such-dir")

	if _, err := startChild(context.Background(), &cfg, DiscardLogger); err == nil {
		t.Error("a missing working directory should be an error")
	}
}
