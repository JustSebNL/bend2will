package bend2will

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Supervisor runs and watches a child process.
//
// It is safe for concurrent use, and it is the only part of bend2will that
// actually keeps a program alive. Its contract is deliberately small:
//
//   - Run blocks until the service should stop, then returns.
//   - A stop request makes Run stop the child, within StopTimeout, and return.
//   - When Restart is set, a child that exits unexpectedly is started again
//     after a backoff. A child that stays up for longer than Backoff.ResetAfter
//     resets that backoff.
//   - When Restart is not set, the child's exit ends the service.
//
// A Supervisor is single-use: create one, Run it, discard it.
type Supervisor struct {
	cfg   Config
	log   Logger
	plat  Platform
	owned bool // plat was created by us and must be closed

	tracker      *statusTracker
	mu           sync.Mutex
	seenStatuses []Status
	stopper      *Stopper
	restarts     int
	// lastStart is when the current child was launched, used to decide whether
	// it lived long enough to reset the backoff.
	lastStart time.Time
	// running is true between a successful start and the child's exit.
	running bool
	// exitCode is the child's exit code from the most recent run.
	exitCode int
	// exitErr is the error from reaping the most recent child.
	exitErr error
}

// NewSupervisor builds a Supervisor for cfg using the detected platform.
func NewSupervisor(cfg Config) (*Supervisor, error) {
	return NewSupervisorWithPlatform(cfg, Detect())
}

// NewSupervisorWithPlatform builds a Supervisor for cfg using plat.
//
// The platform is not closed by the Supervisor; the caller keeps ownership. Use
// it when you need a custom Platform implementation, or to share one platform
// across several supervisors.
func NewSupervisorWithPlatform(cfg Config, plat Platform) (*Supervisor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if plat == nil {
		return nil, errors.New("bend2will: platform must not be nil")
	}

	log, closer, err := NewLogger(cfg.Name, cfg.LogPath, cfg.LogLevel, cfg.LogMaxBytes)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		// The logger's file is closed when the supervisor is, via finalise.
		_ = closer
	}

	s := &Supervisor{
		cfg:  cfg,
		log:  log,
		plat: plat,
	}
	s.tracker = newStatusTracker(s.emit)
	return s, nil
}

// Config returns a copy of the configuration in force.
func (s *Supervisor) Config() Config { return s.cfg }

// Status returns the current lifecycle state.
func (s *Supervisor) Status() Status { return s.tracker.current0() }

// Restarts returns how many times the child has been restarted since Run began.
func (s *Supervisor) Restarts() int { return s.restarts }

// ExitCode returns the exit code of the most recent child, or -1 if none has run.
func (s *Supervisor) ExitCode() int { return s.exitCode }

// Name returns the service name.
func (s *Supervisor) Name() string { return s.cfg.Name }

// statusesSeen returns the lifecycle transitions reported so far, for tests and
// for diagnostics.
func (s *Supervisor) statusesSeen() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Status(nil), s.seenStatuses...)
}

// Run supervises the child until the service should stop.
//
// It is the SuperviseFunc handed to Platform.Connect, and it is also what a
// caller invokes directly when running in the foreground. Either way, the
// supervision happens in this goroutine unless the platform says otherwise.
func (s *Supervisor) Run(ctx context.Context, mode Mode) error {
	s.cfg.resolveCommand()

	s.stopper = NewStopper()
	if s.stopper.Stopped() {
		return nil
	}

	// Hand the platform a supervision function it can call on its own thread.
	// On Windows the SCM owns the main thread and calls this for us; everywhere
	// else we run it here.
	sup := func(c context.Context) error { return s.supervise(c) }

	handled, err := s.plat.Connect(ctx, &s.cfg, mode, sup, s.stopper)
	if err != nil {
		return err
	}
	if handled {
		// The platform ran sup on its own thread and has now returned.
		return nil
	}

	// Foreground: run in the foreground, and translate signals into a stop
	// request so that Ctrl-C, `systemctl stop` and `launchctl kill` all behave
	// identically.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := installSignalHandler()
	defer stopSignalHandler(sigCh)

	var sigOnce sync.Once
	go func() {
		select {
		case <-ctx.Done():
			s.stopper.Stop()
		case <-s.stopper.Done():
		}
	}()
	go func() {
		for sig := range sigCh {
			s.log.Log(LevelInfo, "received %s; stopping %s", sig, s.cfg.Name)
			// A second signal means the operator is no longer patient. Every wait in
			// the loop is already interruptible, so the second one only shortens
			// the wait; it cannot make the shutdown ruder.
			sigOnce.Do(s.stopper.Stop)
		}
	}()

	return sup(ctx)
}

// supervise is the supervision loop proper.
func (s *Supervisor) supervise(ctx context.Context) error {
	s.log.Log(LevelInfo, "%s starting: %s", s.cfg.Name, s.cfg.CommandLine())
	s.log.Log(LevelInfo, "platform=%s restart=%t stop=%s timeout=%s",
		s.plat.Name(), s.cfg.Restart, s.cfg.StopMethod, s.cfg.StopTimeout)

	for {
		// A stop requested before we even started wins.
		if s.stopper.Stopped() {
			break
		}

		if err := s.runOnce(ctx); err != nil {
			return err
		}

		// runOnce has returned: the child is gone.
		if !s.cfg.Restart {
			s.log.Log(LevelInfo, "%s exited with code %d; restart is disabled, so %s is stopping",
				commandName(s.cfg.Command), s.exitCode, s.cfg.Name)
			break
		}
		if s.stopper.Stopped() {
			break
		}

		// Did the child live long enough to earn a clean slate?
		if s.cfg.Backoff.ResetAfter > 0 && !s.lastStart.IsZero() &&
			time.Since(s.lastStart) >= s.cfg.Backoff.ResetAfter {
			if s.restarts > 0 {
				s.log.Log(LevelDebug, "child stayed up for %s; resetting the restart backoff",
					s.cfg.Backoff.ResetAfter)
			}
			s.restarts = 0
		}

		s.restarts++
		delay := s.cfg.Backoff.delay(s.restarts)
		s.log.Log(LevelWarn, "%s exited with code %d; restart %d in %s",
			commandName(s.cfg.Command), s.exitCode, s.restarts, delay)

		if !s.sleepCtx(ctx, delay) {
			// Woken by a stop request rather than by the timer.
			break
		}
	}

	s.transition(StatusStopped)
	s.log.Log(LevelInfo, "%s stopped", s.cfg.Name)
	return nil
}

// runOnce starts the child, waits for it to become ready, then watches it until
// it exits or a stop is requested. It returns an error only for failures that
// are not the child's fault, such as being unable to start it at all.
func (s *Supervisor) runOnce(ctx context.Context) error {
	s.transition(StatusStarting)

	child, err := startChild(ctx, &s.cfg, s.log)
	if err != nil {
		// A command that cannot be executed will never be executable, so this is
		// worth surfacing as a hard failure rather than restarting forever.
		return err
	}
	s.lastStart = time.Now()
	s.running = true
	s.exitCode = -1
	s.exitErr = nil

	defer func() { s.running = false }()

	if err := s.waitReady(ctx, child); err != nil {
		if errors.Is(err, errChildGone) {
			// The child is already gone, so there is nothing to stop and nothing
			// to reap beyond what startChild's goroutine already does. Fall through
			// to the exit handling rather than claiming the service ever ran.
			s.log.Log(LevelDebug, "%v", err)
			<-child.Done()
			s.exitCode, s.exitErr = child.ExitCode(), child.Wait()
			return nil
		}
		// The child never became ready but is still running, so stop it before
		// returning; otherwise a restart loop would leak processes.
		s.log.Log(LevelError, "%s did not become ready: %v", s.cfg.Name, err)
		s.stopChild(child)
		s.exitCode, s.exitErr = child.ExitCode(), child.Wait()
		return nil
	}

	s.transition(StatusRunning)

	// Watch the child, waking for a stop request or for its exit.
	stopped := s.watch(ctx, child)

	if stopped {
		s.transition(StatusStopping)
		s.stopChild(child)
	}

	// Wait for the reaper so that the exit code is available and no zombie is
	// left behind.
	<-child.Done()
	s.exitCode, s.exitErr = child.ExitCode(), child.Wait()

	switch {
	case s.exitCode == 0:
		s.log.Log(LevelInfo, "%s (%s) exited cleanly", s.cfg.Name, commandName(s.cfg.Command))
	default:
		s.log.Log(LevelWarn, "%s (%s) exited with code %d: %v",
			s.cfg.Name, commandName(s.cfg.Command), s.exitCode, s.exitErr)
	}
	return nil
}

// watch blocks until the child exits or a stop is requested, reporting which.
func (s *Supervisor) watch(ctx context.Context, child *Child) bool {
	select {
	case <-child.Done():
		return false
	case <-s.stopper.Done():
		return true
	case <-ctx.Done():
		return true
	}
}

// errChildGone says the child exited before it could be reported as ready.
var errChildGone = errors.New("the child exited before becoming ready")

// waitReady holds the service in the starting state until the child is up.
//
// With no ReadyCommand that means "the child process exists". A command that
// fails immediately is caught here rather than being reported as running and then
// restarted, which would report a healthy service that does not exist. A command
// that exits zero quickly is a legitimate short-lived run, so it is allowed
// through and the caller decides.
//
// With a ReadyCommand the command is polled until it exits zero or StartTimeout
// expires. That is the systemd ExecStartPost equivalent, and the reason a service
// is not reported as running before its database is actually listening.
func (s *Supervisor) waitReady(ctx context.Context, child *Child) error {
	if strings.TrimSpace(s.cfg.ReadyCommand) == "" {
		select {
		case <-child.Done():
			if child.ExitCode() != 0 {
				return fmt.Errorf("%w: %s exited with code %d",
					errChildGone, commandName(s.cfg.Command), child.ExitCode())
			}
			// Exited zero. Not an error: report it as having run, and let the
			// caller's exit handling take over.
			return nil
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}

	deadline := time.Now().Add(s.cfg.StartTimeout)
	interval := s.cfg.ReadyInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	s.log.Log(LevelInfo, "waiting for %q to succeed before reporting %s as running",
		s.cfg.ReadyCommand, s.cfg.Name)

	for attempt := 1; ; attempt++ {
		// Never let a hung readiness command outlive the service.
		runCtx, cancel := context.WithTimeout(ctx, remainingUntil(deadline))
		err := runOneShot(runCtx, s.cfg.ReadyCommand, s.cfg.Dir, s.cfg.Env)
		cancel()

		if err == nil {
			s.log.Log(LevelInfo, "%s is ready after %d check(s)", s.cfg.Name, attempt)
			return nil
		}
		if s.cfg.StartTimeout > 0 && !time.Now().Before(deadline) {
			return fmt.Errorf("readiness command %q did not succeed within %s: %w",
				s.cfg.ReadyCommand, s.cfg.StartTimeout, err)
		}
		s.log.Log(LevelDebug, "readiness check %d failed (%v); retrying in %s", attempt, err, interval)

		// Sleep, but wake immediately if the child dies or a stop arrives.
		t := time.NewTimer(interval)
		select {
		case <-child.Done():
			t.Stop()
			return fmt.Errorf("%s exited while waiting to become ready", commandName(s.cfg.Command))
		case <-s.stopper.Done():
			t.Stop()
			return errors.New("stop requested while waiting to become ready")
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// stopChild brings the child down, escalating to a kill once StopTimeout passes.
func (s *Supervisor) stopChild(child *Child) {
	if !child.Alive() {
		return
	}
	pid := child.PID()
	s.log.Log(LevelInfo, "stopping %s (pid %d)", s.cfg.Name, pid)

	sig, err := s.cfg.stopSignal()
	if err != nil {
		s.log.Log(LevelError, "%v; falling back to an immediate kill", err)
		sig = SignalKill
	}

	switch s.cfg.StopMethod {
	case StopKill:
		if err := child.Kill(); err != nil && !errors.Is(err, ErrNoChild) {
			s.log.Log(LevelWarn, "kill pid %d: %v", pid, err)
		}

	case StopExec:
		if sig != SignalKill {
			if err := child.Signal(sig); err != nil {
				s.log.Log(LevelDebug, "graceful signal to pid %d: %v", pid, err)
			}
		}
		cmd := percentPID(s.cfg.StopCommand, pid)
		runCtx, cancel := context.WithTimeout(context.Background(), s.cfg.StopTimeout)
		if err := runOneShot(runCtx, cmd, s.cfg.Dir, s.cfg.Env); err != nil {
			s.log.Log(LevelWarn, "shutdown command %q: %v", cmd, err)
		}
		cancel()

	default: // StopSignal
		if err := child.Signal(sig); err != nil {
			s.log.Log(LevelDebug, "graceful signal to pid %d: %v", pid, err)
		}
	}

	// Whatever the method, the child must actually go away.
	if exited, _ := child.WaitFor(s.cfg.StopTimeout); exited {
		s.log.Log(LevelDebug, "pid %d exited after a graceful stop", pid)
		return
	}

	if !hasProcessGroupSupport {
		s.log.Log(LevelWarn, "%s did not exit within %s; this platform cannot signal a process tree, "+
			"so the child was killed. Set stop=kill or increase stop-timeout.",
			s.cfg.Name, s.cfg.StopTimeout)
	}
	s.log.Log(LevelWarn, "%s did not exit within %s; killing", s.cfg.Name, s.cfg.StopTimeout)
	if err := child.Kill(); err != nil && !errors.Is(err, ErrNoChild) {
		s.log.Log(LevelWarn, "kill pid %d: %v", pid, err)
	}
	// Reap it so that no zombie is left behind.
	<-child.Done()
}

// sleepCtx waits for d, reporting false if a stop or cancellation arrived first.
func (s *Supervisor) sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return !s.stopper.Stopped()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.stopper.Done():
		return false
	case <-ctx.Done():
		return false
	}
}

// transition moves the lifecycle state, reporting it to the platform.
//
// An illegal transition is logged and ignored: a platform that reports the wrong
// state must not be able to wedge the supervisor.
func (s *Supervisor) transition(next Status) {
	s.tracker.set(next, func(from, to Status) {
		s.log.Log(LevelWarn, "ignoring an out-of-order status change: %s -> %s", from, to)
	})
}

// emit publishes a lifecycle transition to the platform and the log.
func (s *Supervisor) emit(next Status) {
	s.mu.Lock()
	s.seenStatuses = append(s.seenStatuses, next)
	s.mu.Unlock()
	s.log.Log(LevelDebug, "status: %s", next)
	if err := s.plat.ReportStatus(next); err != nil {
		// A service that is running must not die because its status could not be
		// reported, so this is logged and otherwise ignored.
		s.log.Log(LevelWarn, "reporting status %s: %v", next, err)
	}
}

// runOneShot runs a command to completion, with no shell and no supervision.
//
// The command string is split with the platform's own rules, because that is what
// the string means: a Windows command line on Windows, a shell command line
// elsewhere. Using shell rules on Windows would consume the backslashes in a
// path and the command would simply not be found.
func runOneShot(ctx context.Context, command, dir string, env []string) error {
	args, err := splitForExecution(command)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Output is deliberately not captured. A readiness check that prints something
	// useful when it fails should have that output visible, not swallowed.
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// remainingUntil returns the time left before a deadline, floored at a small
// positive value so that context.WithTimeout does not immediately expire.
func remainingUntil(deadline time.Time) time.Duration {
	d := time.Until(deadline)
	if d < time.Second {
		return time.Second
	}
	return d
}

func commandName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}
