//go:build !windows

package bend2will

import (
	"os"
	"syscall"
)

// shutdownSignals lists the signals a graceful shutdown can arrive on.
//
//   - SIGINT and SIGTERM are what an operator and `systemctl stop` send.
//   - SIGHUP is included because some supervisors use it for a reload; treat it
//     as a stop rather than ignoring it.
//   - SIGQUIT is the traditional "please dump core and die" request, and is
//     treated as a stop so that a wedged child is not immortal.
//
// SIGKILL and SIGSTOP cannot be caught and so are absent by necessity.
func shutdownSignals() []os.Signal {
	return []os.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	}
}
