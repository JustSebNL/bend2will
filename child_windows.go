//go:build windows

package bend2will

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// basicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION.
type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// ioRateControl mirrors JOBOBJECT_IO_RATE_CONTROL_INFORMATION.
type ioRateControl struct {
	ControlFlags uint32
	Reserved     [6]uintptr
	Control      uintptr
}

// extendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioRateControl
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// platformHandle carries a Windows job object, which is how a process tree is
// tracked and killed as a unit. The kernel has no equivalent of a Unix process
// group that reliably covers every descendant, and a job object is the
// supported substitute.
type platformHandle struct {
	job syscall.Handle
}

// release kills the remaining members of the job and closes the handle.
//
// It is called once the child has been reaped. Kill-on-job-close already makes
// the close sufficient, but terminating first means descendants get no chance to
// outlive the service.
func (h platformHandle) release() {
	if h.job != 0 {
		_, _, _ = procTerminateJobObject.Call(uintptr(h.job), jobObjectTerminate)
		_ = syscall.CloseHandle(h.job)
	}
}

// sysProcAttr builds the child creation attributes.
//
// CREATE_NEW_PROCESS_GROUP is what makes GenerateConsoleCtrlEvent(CTRL_BREAK)
// addressable to this child specifically rather than to every process sharing the
// console. The priority class is applied here because Windows offers no post-start
// API for it.
func sysProcAttr(cfg *Config, p Priority) (*syscall.SysProcAttr, error) {
	flags := uint32(createNewProcessGroup)

	switch p {
	case PriorityHigh:
		flags |= highPriorityClass
	case PriorityIdle:
		flags |= idlePriorityClass
	case PriorityRealtime:
		flags |= realtimePriorityClass
	default:
		flags |= normalPriorityClass
	}

	if cfg.User != "" {
		return nil, fmt.Errorf("bend2will: user is not supported on Windows (got %q); "+
			"use the service account in the service definition instead", cfg.User)
	}

	// Prevent a console window from being created when we are not attached to one,
	// otherwise a console-mode child flashes a window on every restart.
	if !hasConsole() {
		flags |= createNoWindow
	}

	return &syscall.SysProcAttr{CreationFlags: flags, HideWindow: !hasConsole()}, nil
}

// acquireTree creates a job object with kill-on-close and puts the freshly
// started child into it.
//
// The child is assigned to the job after exec, so there is a brief window in
// which it could create a descendant first. That is harmless: job assignment
// applies to the whole subtree retroactively, so a descendant cannot escape by
// having been created earlier.
func acquireTree(pid int) platformHandle {
	job, _, _ := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		// Losing the job object degrades tree-kill to single-process kill, which
		// is not worth failing a start over.
		return platformHandle{}
	}

	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = limitKillOnJobClose
	if ret, _, _ := procSetInformationJobObject.Call(job, jobObjectExtendedLimit,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ret == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return platformHandle{}
	}

	// OpenProcess fails if the child has already exited, which is harmless.
	if h, err := syscall.OpenProcess(processAllAccess, false, uint32(pid)); h != 0 {
		_, _, _ = procAssignProcessToJobObject.Call(job, uintptr(h))
		_ = syscall.CloseHandle(h)
	} else {
		_ = err
	}

	return platformHandle{job: syscall.Handle(job)}
}

// signalTree asks the child to shut down.
//
// The graceful path is a console ctrl-break, which a cooperative program handles
// by running its normal shutdown. It is only meaningful when a console is
// actually attached: with no console there is no ctrl event to raise, so this
// reports that and the caller escalates to Kill once its timeout expires.
func (c *Child) signalTree(pid int, sig Signal) error {
	if sig == SignalKill {
		return c.killTree(pid)
	}
	if c.job == 0 {
		return fmt.Errorf("bend2will: no job object for pid %d, cannot stop it gracefully", pid)
	}
	if !hasConsole() {
		return fmt.Errorf("bend2will: no console attached, cannot deliver ctrl-break to pid %d", pid)
	}

	ret, _, err := procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if ret == 0 {
		return fmt.Errorf("bend2will: ctrl-break to pid %d: %w", pid, err)
	}
	return nil
}

// killTree terminates the whole job, so every descendant of the child dies with
// it. That is the behaviour a service manager expects from a stop.
func (c *Child) killTree(pid int) error {
	if c.job == 0 {
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		if err := p.Kill(); err != nil {
			return fmt.Errorf("bend2will: kill pid %d: %w", pid, err)
		}
		return nil
	}
	if ret, _, err := procTerminateJobObject.Call(uintptr(c.job), jobObjectTerminate); ret == 0 {
		return fmt.Errorf("bend2will: TerminateJobObject: %w", err)
	}
	return nil
}

// hasConsole reports whether this process is attached to a console.
//
// A Windows service has none, which is both how we decide to pass
// CREATE_NO_WINDOW and how we know a ctrl-break cannot be delivered.
func hasConsole() bool {
	var ids [8]uint32
	ret, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return ret > 0
}

// exitCodeForWaitStatus is a fallback only. On Windows os.ProcessState already
// carries the real exit code, so exitCodeOf never reaches this.
func exitCodeForWaitStatus(ee *exec.ExitError) int { return 1 }

// prepareChildIO wires the child's standard streams.
//
// With a console attached the child simply inherits ours. Without one -- the
// normal case under the Service Control Manager -- the inherited handles are
// invalid, so output goes to the configured log file if there is one, and to the
// null device otherwise.
//
// The returned closer, if any, must be called once the child has been reaped.
func prepareChildIO(cmd *exec.Cmd, cfg *Config, log Logger) (io.Closer, error) {
	return prepareChildIOWithConsole(cmd, cfg, log, hasConsole())
}

func prepareChildIOWithConsole(cmd *exec.Cmd, cfg *Config, log Logger, console bool) (io.Closer, error) {
	cmd.Stdin = nil

	if console {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return nil, nil
	}

	if path := strings.TrimSpace(cfg.LogPath); path != "" && path != "-" && path != "@" {
		if writer, ok := log.(*logFileLogger); ok {
			cmd.Stdout = writer
			cmd.Stderr = writer
			return nil, nil
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, cfg.LogFileMode())
		if err != nil {
			log.Log(LevelWarn, "cannot open log file %q for child output (%v); discarding child output", path, err)
		} else {
			cmd.Stdout = f
			cmd.Stderr = f
			return f, nil
		}
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("bend2will: open %s: %w", os.DevNull, err)
	}
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	return devNull, nil
}

// parseSignalName resolves a signal name. Windows offers no real choice, so only
// names that map onto a documented behaviour are accepted.
func parseSignalName(name string) (Signal, error) {
	n := name
	if len(n) > 3 && strings.EqualFold(n[:3], "sig") {
		n = n[3:]
	}
	n = strings.ToUpper(n)
	switch n {
	case "", "TERM", "BREAK", "CTRL_BREAK", "HUP":
		return SignalTerm, nil
	case "INT":
		return SignalInterrupt, nil
	case "KILL", "QUIT", "9":
		return SignalKill, nil
	default:
		return SignalNone, fmt.Errorf(
			"bend2will: unsupported signal %q on Windows (want TERM, INT or KILL)", name)
	}
}

// processAlive reports whether a pid is still running. Go cannot deliver a
// signal-zero probe on Windows, so the exit code is consulted instead.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == exitDone || code == stillActive
}

// hasProcessGroupSupport is false: Windows has no process groups in the POSIX
// sense, and a graceful stop depends on a console being present.
const hasProcessGroupSupport = false
