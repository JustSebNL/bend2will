//go:build !windows

package bend2will

import (
	"fmt"
	"io"
	"log/syslog"
	"strings"
)

// syslogLogger routes diagnostics to syslog(3), which is what a service manager
// on Linux and the BSDs expects to find.
//
// log/syslog models syslog as a set of severity-specific methods rather than as
// a priority plus a message, so each bend2will level maps onto the method that
// encodes the matching priority. That matters because the priority is carried in
// the packet header: journalctl and most collectors filter on it, not on the
// message text.
type syslogLogger struct {
	w     *syslog.Writer
	level Level
}

func newSyslogLogger(name string, level Level) (Logger, io.Closer, error) {
	w, err := syslog.New(syslog.LOG_DAEMON, sanitiseTag(name))
	if err != nil {
		return nil, nil, fmt.Errorf("bend2will: open syslog: %w", err)
	}
	return &syslogLogger{w: w, level: level}, w, nil
}

// newSystemLogger opens the platform system log.
func newSystemLogger(name string, level Level) (Logger, io.Closer, error) {
	return newSyslogLogger(name, level)
}

func (l *syslogLogger) Log(level Level, format string, args ...any) {
	if level < l.level || l.level == LevelOff {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	// A closed or unreachable syslog socket must never take the service down, so
	// every write error is deliberately discarded. The caller has a log level for
	// exactly this trade-off.
	switch level {
	case LevelError:
		_ = l.w.Err(msg)
	case LevelWarn:
		_ = l.w.Warning(msg)
	case LevelInfo:
		_ = l.w.Info(msg)
	default:
		_ = l.w.Debug(msg)
	}
}

func (l *syslogLogger) Close() error { return l.w.Close() }

// sanitiseTag reduces a service name to something syslog will accept: no spaces,
// no slashes, and short enough not to be truncated by the daemon.
func sanitiseTag(name string) string {
	if name == "" {
		return CommandName
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() == 32 {
			break
		}
	}
	if b.Len() == 0 {
		return CommandName
	}
	return b.String()
}
