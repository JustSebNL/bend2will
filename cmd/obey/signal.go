package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals the CLI treats as "stop supervising".
//
// SIGINT is what a terminal sends on Ctrl-C, SIGTERM what `systemctl stop` and
// `launchctl kill` send, and SIGHUP what some supervisors send for a reload.
// They are safe to register on every platform: on Windows only os.Interrupt is
// ever delivered, because a Windows service has no console to raise the others
// on, and registering an undeliverable signal is harmless.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
