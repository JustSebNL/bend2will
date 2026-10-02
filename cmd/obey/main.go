// Command obey runs and manages services with bend2will.
//
// It supervises a program as a foreground process, and installs, starts, stops
// and removes it as a real service using whatever the host operating system
// provides: the Windows Service Control Manager, systemd on Linux, launchd on
// macOS.
//
// Usage:
//
//	obey <command> [options]
//
// Run "obey help <command>" for the options of a single command.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/JustSebNL/bend2will"
)

// exitCode is the process exit status.
type exitCode int

const (
	exitOK    exitCode = 0
	exitError exitCode = 1
	// exitUsage is returned for a bad command line, matching the convention that
	// 2 means "you held it wrong".
	exitUsage exitCode = 2
)

func main() {
	os.Exit(int(run(os.Args[1:], os.Stdout, os.Stderr)))
}

// run is main's testable body: it takes the arguments and writers and returns an
// exit status instead of touching the process, so the whole CLI can be tested
// in-process.
func run(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	if len(args) == 0 {
		usage(stdout)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "help", "-h", "--help":
		return help(rest, stdout)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "%s %s\n", bend2willCommandName, bend2willVersion)
		return exitOK
	case "shoo", "run", "service":
		return runSupervise(rest, stdout, stderr, plat...)
	case "show", "print":
		return runPrint(rest, stdout, stderr)
	case "bind", "install":
		return runInstall(rest, stdout, stderr)
	case "release", "uninstall":
		return runUninstall(rest, stdout, stderr)
	case "serve", "start":
		return runStartStop("start", rest, stdout, stderr)
	case "terminate", "stop":
		return runStartStop("stop", rest, stdout, stderr)
	case "restart":
		return runRestart(rest, stdout, stderr)
	case "report", "status":
		return runStatus(rest, stdout, stderr)
	case "portable":
		if len(rest) == 0 {
			fmt.Fprintf(stderr, "%s portable: which command? try 'obey portable bind myapp'\n\n", bend2willCommandName)
			return exitUsage
		}
		portable, err := portablePlatform()
		if err != nil {
			return fail(stderr, err)
		}
		return dispatch(rest[0], rest[1:], stdout, stderr, portable)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", bend2willCommandName, cmd)
		usage(stderr)
		return exitUsage
	}
}

// dispatch routes a subcommand, optionally against a specific platform.
func dispatch(cmd string, args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	if plat == nil {
		plat = []bend2will.Platform{nil}
	}
	switch cmd {
	case "shoo", "run", "service":
		return runSupervise(args, stdout, stderr, plat...)
	case "show", "print":
		return runPrint(args, stdout, stderr, plat...)
	case "bind", "install":
		return runInstall(args, stdout, stderr, plat...)
	case "release", "uninstall":
		return runUninstall(args, stdout, stderr, plat...)
	case "serve", "start":
		return runStartStop("start", args, stdout, stderr, plat...)
	case "terminate", "stop":
		return runStartStop("stop", args, stdout, stderr, plat...)
	case "restart":
		return runRestart(args, stdout, stderr, plat...)
	case "report", "status":
		return runStatus(args, stdout, stderr, plat...)
	case "help", "-h", "--help":
		return help(nil, stdout)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n", bend2willCommandName, cmd)
		return exitUsage
	}
}

// errUsage marks an error that should be reported as a command-line problem
// rather than a runtime failure.
var errUsage = errors.New("usage")

// fail prints err and returns the right exit status.
func fail(stderr *os.File, err error) exitCode {
	if errors.Is(err, errUsage) {
		return exitUsage
	}
	fmt.Fprintf(stderr, "%s: %v\n", bend2willCommandName, err)
	return exitError
}
