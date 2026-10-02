// Command helper is the test child process for bend2will's supervisor tests.
//
// It is a separate program, built on demand by the test, so that the tests
// exercise a real child process without depending on a shell. Shelling out to
// cmd.exe, /bin/sh or zsh would make the tests depend on three different quoting
// grammars for no benefit.
//
// Every mode is selected by the first argument, and every mode is deterministic
// so that a test can assert on exactly what happened.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// arg returns argument i, or the empty string.
func arg(i int) string {
	if i < len(os.Args) {
		return os.Args[i]
	}
	return ""
}

// millis parses a millisecond count, treating anything unparsable as zero.
func millis(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

// exitCode parses an exit code, defaulting to zero.
func exitCode(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// stopSignals is the set this helper treats as a polite stop request. On Windows
// only os.Interrupt is ever delivered, because a process with no console cannot
// receive the others; registering them anyway is harmless.
func stopSignals() []os.Signal {
	sigs := []os.Signal{os.Interrupt}
	if term, ok := any(syscall.SIGTERM).(os.Signal); ok {
		sigs = append(sigs, term)
	}
	return sigs
}

// appendLine adds one line to a file, which is how the helper reports what
// happened to it.
func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: cannot open", path, err)
		return
	}
	fmt.Fprintln(f, line)
	_ = f.Close()
}

func main() {
	mode := "sleep"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	pid := strconv.Itoa(os.Getpid())

	switch mode {

	case "sleep":
		// Run for a while, then exit cleanly.
		time.Sleep(millis(arg(2)))
		os.Exit(0)

	case "exit":
		// Exit immediately with a given code.
		os.Exit(exitCode(arg(2)))

	case "crash":
		// Run for a while, then exit with a non-zero code.
		time.Sleep(millis(arg(2)))
		os.Exit(exitCode(arg(3)))

	case "ignore-term":
		// Keep running and discard every stop request. This is the child that a
		// supervisor can only stop by killing, which is what the escalation test
		// needs.
		sig := make(chan os.Signal, 4)
		signal.Notify(sig, stopSignals()...)
		deadline := time.After(millis(arg(2)))
		for {
			select {
			case <-sig:
				// Deliberately ignored.
			case <-deadline:
				os.Exit(0)
			}
		}

	case "record":
		// Announce a start, then exit either on a stop request or on a deadline.
		// The signal name is recorded so a test can prove the stop was polite.
		file := arg(2)
		appendLine(file, "start "+pid+" "+strings.Join(os.Args[2:], " "))

		sig := make(chan os.Signal, 4)
		signal.Notify(sig, stopSignals()...)
		select {
		case s := <-sig:
			appendLine(file, "signal "+s.String()+" "+pid)
			os.Exit(0)
		case <-time.After(millis(arg(3))):
			appendLine(file, "timeout "+pid)
			os.Exit(0)
		}

	case "ready":
		// Create a marker file, then keep running for a while. Used as a readiness
		// check whose success a test controls.
		if err := os.WriteFile(arg(2), []byte(pid), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "helper: cannot create marker:", err)
			os.Exit(2)
		}
		time.Sleep(millis(arg(3)))
		os.Exit(0)

	case "ready-if-exists":
		// Exit zero only if a given file exists. This is a readiness check a test
		// can hold in the failing state and then release.
		if _, err := os.Stat(arg(2)); err != nil {
			os.Exit(1)
		}
		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		os.Exit(64)
	}
}
