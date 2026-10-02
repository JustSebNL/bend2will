package bend2will

import (
	"os"
	"os/signal"
	"syscall"
)

// installSignalHandler arranges for the signals a service manager sends during
// a shutdown to be delivered on the returned channel.
//
// It returns nil when there is nothing to watch, which is the normal case under
// the Windows SCM: the SCM sends a control message, not a signal, and the
// platform backend handles that.
func installSignalHandler() chan os.Signal {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, shutdownSignals()...)
	return ch
}

// stopSignalHandler undoes installSignalHandler.
func stopSignalHandler(ch chan os.Signal) {
	if ch == nil {
		return
	}
	signal.Stop(ch)
	close(ch)
}

// waitForSignal blocks until a shutdown signal arrives, and is used by callers
// that only need "tell me when to stop".
func waitForSignal(ch <-chan os.Signal) os.Signal {
	if ch == nil {
		return nil
	}
	return <-ch
}

// syscallSignal converts a received os.Signal into a raw syscall signal, for the
// benefit of code that needs the numeric value.
func syscallSignal(s os.Signal) (syscall.Signal, bool) {
	sig, ok := s.(syscall.Signal)
	return sig, ok
}
