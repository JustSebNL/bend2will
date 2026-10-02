package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/JustSebNL/bend2will"
)

const (
	bend2willCommandName = bend2will.CommandName
	bend2willVersion     = bend2will.Version
)

// usage prints the top-level help.
func usage(w *os.File) {
	fmt.Fprintf(w, `%[1]s %[2]s - run and manage services on Windows, Linux and macOS

usage: %[1]s <command> [options]

commands:
  shoo        supervise a program in the foreground
  service     internal service-manager entry point
  bind        install a service with the platform's service manager
  release     stop and remove a service
  serve       start an installed service
  terminate   stop an installed service
  restart     restart an installed service
  report      report an installed service's state
  show        show the service definition without applying it
  portable    the portable edition: the same commands, installed per-user
  version     print the version
  help        show help for a command

The quickest way to see what a service would look like:

  %[1]s show myapp --command /usr/bin/myapp --arguments --serve --port 5432

The quickest way to make an existing program a service, with no flags at all:

  %[1]s shoo myapp /usr/bin/myapp --serve

Run "%[1]s help <command>" for the options of one command.
`, bend2willCommandName, bend2willVersion)
}

// help prints help for one command.
func help(args []string, w *os.File) exitCode {
	if len(args) == 0 {
		usage(w)
		return exitOK
	}

	switch args[0] {
	case "shoo", "run", "service", "bind", "install", "show", "print":
		// These share the full option set, so their help is the flag list.
		fs := newFlagSet(args[0], w)
		var o options
		o.register(fs)
		fmt.Fprintf(w, "%s %s - see the options below\n\nusage: %s %s [options] [command [arguments...]]\n\noptions:\n",
			bend2willCommandName, bend2willVersion, bend2willCommandName, args[0])
		fs.PrintDefaults()
		return exitOK

	case "release", "uninstall", "serve", "start", "terminate", "stop", "restart", "report", "status":
		fmt.Fprintf(w, `usage: %[1]s %[2]s [options] <service>

options:
  -name string   the service to operate on, instead of a positional argument
  -dry-run       report what would happen without changing anything
  --purge        (release only) also remove the service's saved data
`, bend2willCommandName, args[0])
		return exitOK

	case "version":
		fmt.Fprintf(w, "%s %s\n", bend2willCommandName, bend2willVersion)
		return exitOK

	default:
		fmt.Fprintf(w, "%s: no help for %q\n\n", bend2willCommandName, args[0])
		usage(w)
		return exitUsage
	}
}

// signalContext returns a context cancelled by an interrupt or a termination
// request.
//
// The CLI's own context cancellation is what lets Ctrl-C reach the supervisor,
// which then performs the same ordered shutdown a service manager would ask for.
// Without this, the first Ctrl-C would kill the supervisor outright and orphan
// the child.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, shutdownSignals()...)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}

// versionLine renders the version banner used by diagnostics.
func versionLine() string {
	return strings.TrimSpace(fmt.Sprintf("%s %s", bend2willCommandName, bend2willVersion))
}
