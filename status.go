package bend2will

import "fmt"

// Status is the lifecycle state of a supervised service. It maps onto the
// native vocabulary of each platform: SERVICE_START_PENDING and friends on
// Windows, Type=notify transitions on systemd, and nothing at all on launchd
// (which infers liveness from the process).
type Status int

// Service lifecycle states.
const (
	// StatusUnknown means the supervisor has not decided yet.
	StatusUnknown Status = iota
	// StatusStarting means the child has been launched but is not yet ready.
	StatusStarting
	// StatusRunning means the child is up and has passed its readiness check.
	StatusRunning
	// StatusStopping means a stop has been requested and is in progress.
	StatusStopping
	// StatusStopped means the child is gone and no restart is scheduled.
	StatusStopped
	// StatusFailed means the supervisor gave up.
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusStarting:
		return "starting"
	case StatusRunning:
		return "running"
	case StatusStopping:
		return "stopping"
	case StatusStopped:
		return "stopped"
	case StatusFailed:
		return "failed"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// IsTerminal reports whether no further transition is expected.
func (s Status) IsTerminal() bool { return s == StatusStopped || s == StatusFailed }

// CanTransitionTo reports whether from -> to is a legal move. The supervisor
// uses it to catch ordering bugs, and the platform backends use it to reject
// nonsensical status reports before they reach the OS.
func (s Status) CanTransitionTo(to Status) bool {
	if to == StatusUnknown {
		return false
	}
	if s == to {
		return true
	}
	switch s {
	case StatusUnknown:
		return to == StatusStarting || to == StatusRunning || to == StatusStopped || to == StatusFailed
	case StatusStarting:
		// Starting -> Failed is legal: a start that never completes, because the
		// readiness check timed out or the child died immediately, is a failure
		// and not merely another attempt.
		return to == StatusRunning || to == StatusStopping || to == StatusStopped || to == StatusFailed
	case StatusRunning:
		return to == StatusStopping || to == StatusStopped || to == StatusFailed
	case StatusStopping:
		return to == StatusStopped || to == StatusFailed
	default:
		return false
	}
}

// statusTracker guards a Status against out-of-order transitions and notifies a
// listener. It is safe for concurrent use.
type statusTracker struct {
	current Status
	emit    func(Status)
}

func newStatusTracker(emit func(Status)) *statusTracker {
	if emit == nil {
		emit = func(Status) {}
	}
	return &statusTracker{emit: emit}
}

func (t *statusTracker) current0() Status { return t.current }

// set moves to next if the transition is legal, returning true if it happened.
// An illegal transition is reported through onIllegal and otherwise ignored, so
// that a misbehaving platform backend can never wedge the supervisor.
func (t *statusTracker) set(next Status, onIllegal func(from, to Status)) bool {
	if next == t.current {
		return true
	}
	if !t.current.CanTransitionTo(next) {
		if onIllegal != nil {
			onIllegal(t.current, next)
		}
		return false
	}
	t.current = next
	t.emit(next)
	return true
}
