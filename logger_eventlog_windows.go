//go:build windows

package bend2will

import (
	"fmt"
	"io"
	"syscall"
	"unsafe"
)

// eventLogLogger writes to the Windows Event Log, which is where an
// administrator will look for a service's complaints.
type eventLogLogger struct {
	handle syscall.Handle
	level  Level
}

func newEventLogLogger(name string, level Level) (Logger, io.Closer, error) {
	if name == "" {
		name = "bend2will"
	}
	src, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, nil, fmt.Errorf("bend2will: event source name %q: %w", name, err)
	}
	h, _, callErr := procRegisterEventSourceW.Call(0, uintptr(unsafe.Pointer(src)))
	if h == 0 {
		return nil, nil, fmt.Errorf("bend2will: RegisterEventSource(%q): %w", name, callErr)
	}
	l := &eventLogLogger{handle: syscall.Handle(h), level: level}
	return l, &eventLogCloser{handle: syscall.Handle(h)}, nil
}

// newSystemLogger opens the Windows Event Log.
func newSystemLogger(name string, level Level) (Logger, io.Closer, error) {
	return newEventLogLogger(name, level)
}

func (l *eventLogLogger) Log(level Level, format string, args ...any) {
	if level < l.level || l.level == LevelOff {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	msgPtr, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		// A string that cannot be represented as UTF-16 is not worth crashing
		// over: the process is on its way out either way.
		return
	}
	severity, eventType := eventClass(level)
	_, _, _ = procReportEventW.Call(
		uintptr(l.handle),
		uintptr(eventType),
		uintptr(severity),
		uintptr(eventLogCustom),
		0, 0, 0, 1, // 1 insertion
		uintptr(unsafe.Pointer(msgPtr)), 1)
}

// eventClass maps a bend2will level onto the Event Log's type/severity pair.
func eventClass(level Level) (severity, eventType uint16) {
	switch level {
	case LevelError:
		return sevError, eventLogErrorType
	case LevelWarn:
		return sevWarning, eventLogWarningType
	default:
		return sevInfo, eventLogInformationType
	}
}

// eventLogCloser deregisters the event source exactly once.
type eventLogCloser struct {
	handle syscall.Handle
	closed bool
}

func (c *eventLogCloser) Close() error {
	if c.handle == 0 || c.closed {
		return nil
	}
	c.closed = true
	_, _, _ = procDeregisterEventSource.Call(uintptr(c.handle))
	_, _, _ = procCloseEventLog.Call(uintptr(c.handle))
	c.handle = 0
	return nil
}
