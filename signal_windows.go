//go:build windows

package bend2will

import "os"

// shutdownSignals lists the console control events that mean "stop".
//
// CTRL_C_EVENT and CTRL_BREAK_EVENT are the two a user or a parent process can
// send to a console process. Neither arrives when bend2will is running as a
// Windows service, because a service has no console: in that case the Service
// Control Manager sends a control message instead, which the Windows platform
// backend handles.
//
// Go's os/signal translates a console control event into an os.Signal, so the
// same channel plumbing works on both.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
