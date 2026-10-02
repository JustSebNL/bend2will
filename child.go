package bend2will

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lookPath is a thin indirection so that config.go does not have to import
// os/exec directly.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

// Priority selects the scheduling priority class of the child process.
type Priority int

// Scheduling priorities.
const (
	PriorityNormal Priority = iota
	PriorityHigh
	PriorityIdle
	PriorityRealtime
)

func (p Priority) String() string {
	switch p {
	case PriorityHigh:
		return "high"
	case PriorityIdle:
		return "idle"
	case PriorityRealtime:
		return "realtime"
	case PriorityNormal:
		return "normal"
	default:
		return fmt.Sprintf("Priority(%d)", int(p))
	}
}

// ParsePriority maps a configuration string onto a Priority.
func ParsePriority(s string) (Priority, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "normal":
		return PriorityNormal, nil
	case "high":
		return PriorityHigh, nil
	case "idle", "low":
		return PriorityIdle, nil
	case "realtime", "real":
		return PriorityRealtime, nil
	default:
		return PriorityNormal, fmt.Errorf("bend2will: unknown priority %q (want normal, high, idle or realtime)", s)
	}
}

// Signal is a portable termination request. On Unix it maps onto a POSIX
// signal; on Windows only SignalTerm (a console ctrl-break) and SignalKill (an
// unconditional job-object termination) are meaningful.
type Signal int

// Portable signals.
const (
	SignalNone Signal = iota
	// SignalTerm asks the child to shut down cleanly.
	SignalTerm
	// SignalInterrupt asks the child to interrupt whatever it is doing.
	SignalInterrupt
	// SignalKill terminates the child and every descendant without warning.
	SignalKill
)

func (s Signal) String() string {
	switch s {
	case SignalTerm:
		return "term"
	case SignalInterrupt:
		return "interrupt"
	case SignalKill:
		return "kill"
	default:
		return "none"
	}
}

// ErrNoChild is returned when an operation needs a live child and there is none.
var ErrNoChild = errors.New("bend2will: no child process")

// Child is a supervised child process.
//
// A Child is safe for concurrent use. The supervisor runs Wait in a goroutine
// and selects on Done while it waits for a stop request, so Signal, Kill and
// Alive may all be called while Wait is outstanding.
type Child struct {
	cmd      *exec.Cmd
	priority Priority

	mu       sync.Mutex
	pid      int
	exited   bool
	exitCode int
	waitErr  error

	// done is closed once the child has been reaped.
	done chan struct{}

	// platformHandle carries whatever the OS needs in order to signal or kill
	// the whole process tree (a Unix process-group id, a Windows job object).
	platformHandle

	// closer releases any file the child inherited from us, once it is reaped.
	closer io.Closer

	// tree is released exactly once, when the child is reaped, so platform
	// resources do not outlive it.
	closeTreeOnce sync.Once
}

// pid returns the child's process id, or 0 before Start and after reap.
func (c *Child) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pid
}

// Done returns a channel closed when the child has exited and been reaped.
func (c *Child) Done() <-chan struct{} { return c.done }

// ExitCode returns the child's exit status. It is only meaningful once Done is
// closed. A child killed by a signal reports 128+signum, matching shell
// convention.
func (c *Child) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitCode
}

// Wait returns the error from reaping the child, if any. It is only meaningful
// once Done is closed.
func (c *Child) Wait() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr
}

// Alive reports whether the child is still running. It is advisory: a child that
// exits immediately after this returns true will still be reported as exited by
// the next Done.
func (c *Child) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Signal delivers a termination signal to the child and its descendants.
//
// On Unix this goes to the child's process group, so a shell that spawned
// grandchildren takes them down too. On Windows the ctrl-break path is only
// available when a console is attached; otherwise this falls back to Kill, and
// reports which it did.
func (c *Child) Signal(sig Signal) error {
	c.mu.Lock()
	exited, pid := c.exited, c.pid
	c.mu.Unlock()

	if exited || pid == 0 {
		return ErrNoChild
	}
	return c.signalTree(pid, sig)
}

// Kill terminates the child and every descendant immediately.
func (c *Child) Kill() error {
	c.mu.Lock()
	exited, pid := c.exited, c.pid
	c.mu.Unlock()

	if exited || pid == 0 {
		return ErrNoChild
	}
	return c.killTree(pid)
}

// WaitFor blocks until the child exits or timeout elapses. It reports whether
// the child actually exited. A zero timeout waits indefinitely.
func (c *Child) WaitFor(timeout time.Duration) (exited bool, err error) {
	if timeout <= 0 {
		<-c.done
		return true, c.Wait()
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-c.done:
		return true, c.Wait()
	case <-t.C:
		return false, nil
	}
}

// startChild launches cfg as a child process.
//
// The caller must not have called resolveCommand if it wants PATH lookup to be
// skipped; startChild performs its own lookup through exec.Command, which
// handles a bare command name correctly on all three platforms.
func startChild(ctx context.Context, cfg *Config, log Logger) (*Child, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	priority, err := ParsePriority(cfg.Priority)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = childEnv(cfg.Env)

	attr, err := sysProcAttr(cfg, priority)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = attr

	closer, err := prepareChildIO(cmd, cfg, log)
	if err != nil {
		return nil, err
	}

	// The command is deliberately built with exec.Command rather than
	// exec.CommandContext. The supervisor owns the child's lifetime, and stopping
	// is an explicit, ordered operation: a signal, then a grace period, then a
	// kill. Tying the child to a context would have os/exec SIGKILL it the moment
	// a stop was requested, which is exactly the abrupt shutdown the supervisor
	// exists to avoid.

	if err := cmd.Start(); err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, fmt.Errorf("bend2will: start %q: %w", cfg.Command, err)
	}

	c := &Child{
		cmd:      cmd,
		priority: priority,
		pid:      cmd.Process.Pid,
		done:     make(chan struct{}),
		closer:   closer,
	}
	c.platformHandle = acquireTree(c.pid)

	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.exited = true
		c.waitErr = err
		c.exitCode = exitCodeOf(cmd, err)
		c.mu.Unlock()
		c.closeTreeOnce.Do(c.platformHandle.release)
		if c.closer != nil {
			_ = c.closer.Close()
		}
		close(c.done)
	}()

	log.Log(LevelDebug, "started pid %d: %s", c.pid, cfg.CommandLine())
	return c, nil
}

// exitCodeOf derives a shell-style exit code from the result of reaping.
func exitCodeOf(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			return code
		}
	}
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitCodeForWaitStatus(ee)
	}
	return 1
}

// childEnv merges the configured variables over the inherited environment.
// Entries are validated to contain '=' by Config.Validate.
func childEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	env = append(env, extra...)
	return env
}

// stopSignal resolves the configured stop signal name, falling back to the
// platform default.
func (c *Config) stopSignal() (Signal, error) {
	name := strings.TrimSpace(c.StopSignalName)
	if name == "" {
		return SignalTerm, nil
	}
	return parseSignalName(name)
}

// percentPID replaces the literal %PID% token in a stop command.
func percentPID(command string, pid int) string {
	if pid <= 0 {
		return command
	}
	return strings.ReplaceAll(command, "%PID%", strconv.Itoa(pid))
}

// SplitCommandLine splits a command string into an argv using shell-like rules:
// single and double quotes group, and a backslash escapes the next character
// outside single quotes.
//
// It exists for configuration files, for readiness and shutdown commands, and for
// any platform that is handed a single command line rather than an argv vector.
func SplitCommandLine(s string) ([]string, error) { return splitCommandLine(s) }

// splitCommandLine is the implementation behind SplitCommandLine.
func splitCommandLine(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		started bool
		quote   byte
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			if c == '\\' && quote == '"' && i+1 < len(s) {
				next := s[i+1]
				if next == '"' || next == '\\' {
					cur.WriteByte(next)
					i++
					continue
				}
			}
			cur.WriteByte(c)

		case c == '\'' || c == '"':
			quote = c
			started = true

		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			started = true
			i++

		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()

		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("bend2will: unterminated %c quote in %q", quote, s)
	}
	flush()
	return args, nil
}
