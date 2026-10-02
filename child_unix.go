//go:build !windows

package bend2will

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// platformHandle carries the OS state needed to signal or kill a whole process
// tree. On Unix that is the child's process-group id, which equals its pid
// because we always create a new group.
type platformHandle struct {
	pgid int
}

func (h platformHandle) release() {}

// sysProcAttr builds the child creation attributes.
//
// The child is put in its own process group. Two consequences, both wanted:
//
//   - Signalling the negated group id reaches every descendant, so a shell that
//     forked a daemon does not leave the daemon orphaned.
//   - The child is not in the terminal's foreground process group, so Ctrl-C at
//     the terminal is delivered to bend2will alone. The supervisor then decides
//     what happens to the child, instead of both processes racing.
func sysProcAttr(cfg *Config, p Priority) (*syscall.SysProcAttr, error) {
	attr := &syscall.SysProcAttr{Setpgid: true}

	if cfg.User != "" || cfg.Group != "" {
		cred, err := credential(cfg.User, cfg.Group)
		if err != nil {
			return nil, err
		}
		attr.Credential = cred
	}
	return attr, nil
}

// credential resolves a user and/or group name to the numeric ids that
// SysProcAttr.Credential needs. When no group is named, the user's primary group
// is used, which is almost always what is wanted.
func credential(userName, groupName string) (*syscall.Credential, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("bend2will: user/group is set (%q/%q) but bend2will is not running as root",
			userName, groupName)
	}

	cred := &syscall.Credential{}

	if userName != "" {
		u, err := user.Lookup(userName)
		if err != nil {
			return nil, fmt.Errorf("bend2will: look up user %q: %w", userName, err)
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bend2will: user %q has non-numeric uid %q", userName, u.Uid)
		}
		cred.Uid = uint32(uid)
		cred.Gid = uint32(mustAtoi(u.Gid))

		if groupName == "" {
			groupName = primaryGroupName(u)
		}
	}

	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return nil, fmt.Errorf("bend2will: look up group %q: %w", groupName, err)
		}
		gid, err := strconv.ParseUint(g.Gid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bend2will: group %q has non-numeric gid %q", groupName, g.Gid)
		}
		cred.Gid = uint32(gid)
		// Supplementary groups must be set explicitly, and the child needs
		// CAP_SETGID to do it; SysProcAttr handles the ordering.
		cred.Groups = []uint32{uint32(gid)}
	}

	return cred, nil
}

// primaryGroupName finds the group name for a user's primary gid, falling back
// to the bare gid if /etc/group has no matching entry.
func primaryGroupName(u *user.User) string {
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		return g.Name
	}
	return u.Gid
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// acquireTree records the child's process group.
func acquireTree(pid int) platformHandle { return platformHandle{pgid: pid} }

// signalTree delivers sig to the child's process group.
func (c *Child) signalTree(pid int, sig Signal) error {
	pgid := c.pgid
	if pgid == 0 {
		pgid = pid
	}
	s, err := toSyscallSignal(sig)
	if err != nil {
		return err
	}
	// A negative pid addresses the process group. ESRCH just means the child
	// exited between our liveness check and the signal, which is not an error.
	if err := syscall.Kill(-pgid, s); err == syscall.ESRCH {
		return nil
	} else if err != nil {
		return fmt.Errorf("bend2will: signal %s to process group %d: %w", sig, pgid, err)
	}
	return nil
}

// killTree sends SIGKILL to the child's process group.
func (c *Child) killTree(pid int) error {
	pgid := c.pgid
	if pgid == 0 {
		pgid = pid
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err == syscall.ESRCH {
		return nil
	} else if err != nil {
		return fmt.Errorf("bend2will: kill process group %d: %w", pgid, err)
	}
	return nil
}

func toSyscallSignal(sig Signal) (syscall.Signal, error) {
	switch sig {
	case SignalTerm, SignalNone:
		return syscall.SIGTERM, nil
	case SignalInterrupt:
		return syscall.SIGINT, nil
	case SignalKill:
		return syscall.SIGKILL, nil
	default:
		return 0, fmt.Errorf("bend2will: unknown signal %d", int(sig))
	}
}

// parseSignalName resolves a signal name, accepting common aliases in either
// case ("SIGTERM", "term", "TERM").
func parseSignalName(name string) (Signal, error) {
	n := name
	if len(n) > 3 && strings.EqualFold(n[:3], "sig") {
		n = n[3:]
	}
	n = strings.ToUpper(n)
	switch n {
	case "TERM", "HUP":
		return SignalTerm, nil
	case "INT":
		return SignalInterrupt, nil
	case "KILL", "QUIT":
		return SignalKill, nil
	default:
		return SignalNone, fmt.Errorf("bend2will: unsupported signal %q (want TERM, INT, HUP, QUIT or KILL)", name)
	}
}

// exitCodeForWaitStatus converts a wait status into a shell-style exit code.
func exitCodeForWaitStatus(ee *exec.ExitError) int {
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return 1
	}
	switch {
	case ws.Signaled():
		return 128 + int(ws.Signal())
	case ws.Exited():
		return ws.ExitStatus()
	default:
		return 1
	}
}

// prepareChildIO wires the child's standard streams. On Unix the supervisor
// simply inherits its own: under systemd and launchd those are already routed to
// the journal and to the configured log files respectively.
func prepareChildIO(cmd *exec.Cmd, cfg *Config, log Logger) (io.Closer, error) {
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return nil, nil
}

// processAlive reports whether a pid currently exists and is signalable. It is
// used by "is this service already running" checks, where stale pid files are
// common.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// hasProcessGroupSupport is true everywhere this file builds.
const hasProcessGroupSupport = true
