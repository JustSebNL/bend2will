package bend2will

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is a log severity. Higher values are more severe.
type Level int

// Log levels, in increasing order of severity.
const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	// LevelOff discards everything. It is only valid as a threshold.
	LevelOff
)

func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "trace"
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	case LevelOff:
		return "off"
	default:
		return fmt.Sprintf("Level(%d)", int(l))
	}
}

// ParseLogLevel maps a configuration string onto a Level.
func ParseLogLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "", "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error", "err":
		return LevelError, nil
	case "off", "none", "silent":
		return LevelOff, nil
	default:
		return LevelInfo, fmt.Errorf("bend2will: unknown log level %q (want trace, debug, info, warn, error or off)", s)
	}
}

// Logger receives diagnostic messages. Implement it to route bend2will's
// diagnostics into an existing logging system.
type Logger interface {
	Log(level Level, format string, args ...any)
}

// LoggerFunc adapts a function to the Logger interface.
type LoggerFunc func(level Level, format string, args ...any)

func (f LoggerFunc) Log(level Level, format string, args ...any) { f(level, format, args...) }

// DiscardLogger throws everything away.
var DiscardLogger Logger = LoggerFunc(func(Level, string, ...any) {})

// stderrLogger is the default: timestamped lines on the supervisor's own
// stderr, which is where every service manager expects a daemon to complain.
type stderrLogger struct {
	mu    sync.Mutex
	w     io.Writer
	level Level
	// utc stamps messages in UTC so that logs from several hosts line up.
	utc bool
	now func() time.Time
}

func newStderrLogger(w io.Writer, level Level) *stderrLogger {
	if w == nil {
		w = os.Stderr
	}
	return &stderrLogger{w: w, level: level, now: time.Now}
}

func (l *stderrLogger) Log(level Level, format string, args ...any) {
	if level < l.level || l.level == LevelOff {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	stamp := l.now().Format("2006-01-02T15:04:05.000Z07:00")
	if l.utc {
		stamp = l.now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	line := fmt.Sprintf("%s %-5s %s: %s\n", stamp, level, serviceNameForLog, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, line)
}

// serviceNameForLog is the name stamped on every line. It is set once per
// process by newLogger, and is a plain string rather than a field so that the
// hot path stays cheap.
var serviceNameForLog = "bend2will"

// NewLogger builds the default logger for a service.
//
// logPath selects the sink:
//
//	""    or "-"   the supervisor's stderr (the usual choice under a service manager)
//	"@"            the platform system log: syslog, Event Log or ASL
//	anything else  that file, created if needed and appended to
//
// A ">" prefix on a file path truncates the file instead of appending. A positive
// maxBytes rotates a file log at the limit and retains one .1 backup.
func NewLogger(name, logPath, logLevel string, maxBytes ...int64) (Logger, io.Closer, error) {
	level, err := ParseLogLevel(logLevel)
	if err != nil {
		return nil, nil, err
	}
	maxLogBytes := int64(0)
	if len(maxBytes) > 0 {
		maxLogBytes = maxBytes[0]
	}
	if maxLogBytes < 0 {
		return nil, nil, fmt.Errorf("bend2will: log max bytes must not be negative")
	}

	switch {
	case logPath == "" || logPath == "-":
		return newStderrLogger(os.Stderr, level), io.NopCloser(nil), nil

	case logPath == "@":
		l, closer, err := newSystemLogger(name, level)
		if err != nil {
			// Never fail startup because the system log is unavailable: a service
			// that refuses to run because syslog is misconfigured is worse than
			// one that complains on stderr.
			return newStderrLogger(os.Stderr, level), io.NopCloser(nil), nil
		}
		return l, closer, nil

	default:
		f, err := openLogFile(logPath, level, maxLogBytes)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
}

// logFileLogger writes to a file, serialising access so that a supervisor and a
// child process writing to the same file cannot interleave mid-line.
type logFileLogger struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	level    Level
	maxBytes int64
	size     int64
	mode     os.FileMode
}

func openLogFile(path string, level Level, maxBytes int64) (*logFileLogger, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("bend2will: log max bytes must not be negative")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("bend2will: open log file %q: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("bend2will: stat log file %q: %w", path, err)
	}
	return &logFileLogger{path: path, f: f, level: level, maxBytes: maxBytes, size: info.Size(), mode: 0o644}, nil
}

func (l *logFileLogger) Log(level Level, format string, args ...any) {
	if level < l.level || l.level == LevelOff {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	stamp := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	line := fmt.Sprintf("%s %-5s %s: %s\n", stamp, level, serviceNameForLog, msg)
	_, _ = l.Write([]byte(line))
}

func (l *logFileLogger) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	if l.maxBytes > 0 && l.size > 0 && int64(len(data)) > l.maxBytes-l.size {
		if err := l.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(data)
	l.size += int64(n)
	return n, err
}

func (l *logFileLogger) rotateLocked() error {
	if l.f == nil {
		return os.ErrClosed
	}
	if err := l.f.Close(); err != nil {
		l.f = nil
		if reopenErr := l.reopenAppendLocked(); reopenErr != nil {
			return fmt.Errorf("%w; reopen log file: %v", err, reopenErr)
		}
		return fmt.Errorf("bend2will: close log before rotation: %w", err)
	}
	l.f = nil

	backup := l.path + ".1"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		if reopenErr := l.reopenAppendLocked(); reopenErr != nil {
			return fmt.Errorf("%w; reopen log file: %v", err, reopenErr)
		}
		return fmt.Errorf("bend2will: remove previous log backup: %w", err)
	}
	if err := os.Rename(l.path, backup); err != nil {
		if reopenErr := l.reopenAppendLocked(); reopenErr != nil {
			return fmt.Errorf("%w; reopen log file: %v", err, reopenErr)
		}
		return fmt.Errorf("bend2will: rotate log file: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, l.mode)
	if err != nil {
		if restoreErr := os.Rename(backup, l.path); restoreErr == nil {
			if reopenErr := l.reopenAppendLocked(); reopenErr != nil {
				return fmt.Errorf("bend2will: create rotated log file: %w; reopen log file: %v", err, reopenErr)
			}
		}
		return fmt.Errorf("bend2will: create rotated log file %q: %w", l.path, err)
	}
	l.f = f
	l.size = 0
	return nil
}

func (l *logFileLogger) reopenAppendLocked() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, l.mode)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f = f
	l.size = info.Size()
	return nil
}

func (l *logFileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
