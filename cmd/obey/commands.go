package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JustSebNL/bend2will"
)

// runSupervise implements "obey run": supervise a program in the foreground.
func runSupervise(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("run", stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	cfg, err := o.config(fs, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	var runErr error
	if len(plat) > 0 && plat[0] != nil {
		runErr = bend2will.RunWithPlatformContext(ctx, cfg, plat[0])
	} else {
		runErr = bend2will.RunContext(ctx, cfg)
	}
	if runErr != nil {
		return fail(stderr, runErr)
	}
	return exitOK
}

// runPrint implements "obey print": show the service definition without applying
// it. It is the command to reach for when a service will not start and the
// generated definition is the thing in question.
func runPrint(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("print", stderr)
	var o options
	o.register(fs)
	fs.Bool("with-instructions", true, "include the manual steps after the definition")
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	cfg, err := o.config(fs, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}
	installOptions, err := o.installOptions()
	if err != nil {
		return fail(stderr, err)
	}

	selected := platformOrDetect(plat)
	def, err := bend2will.DefinitionForInstall(selected, &cfg, installOptions)
	if err != nil {
		return fail(stderr, err)
	}

	fmt.Fprintf(stdout, "# %s definition for %q (%s)\n", def.Kind, cfg.Name, selected.Name())
	fmt.Fprintf(stdout, "# destination: %s\n", def.Path)
	fmt.Fprint(stdout, def.Contents)

	if with, ok := fs.Lookup("with-instructions").Value.(interface{ Get() any }); ok {
		if b, _ := with.Get().(bool); !b {
			return exitOK
		}
	}
	if len(def.Instructions) > 0 {
		fmt.Fprintf(stdout, "\n# remaining manual steps:\n")
		for _, step := range def.Instructions {
			fmt.Fprintf(stdout, "#   %s\n", step)
		}
	}
	return exitOK
}

// runInstall implements "obey install".
func runInstall(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("install", stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	cfg, err := o.config(fs, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}
	installOptions, err := o.installOptions()
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	selected := platformOrDetect(plat)
	if err := selected.Install(ctx, &cfg, installOptions); err != nil {
		return fail(stderr, err)
	}

	if o.dryRun {
		def, derr := bend2will.DefinitionForInstall(selected, &cfg, installOptions)
		if derr != nil {
			return fail(stderr, derr)
		}
		fmt.Fprintf(stdout, "dry run: would install %q as a %s at %s\n", cfg.Name, def.Kind, def.Path)
		for _, step := range def.Instructions {
			fmt.Fprintf(stdout, "dry run:   %s\n", step)
		}
		return exitOK
	}

	fmt.Fprintf(stdout, "installed %q\n", cfg.Name)
	return exitOK
}

// runUninstall implements "obey uninstall".
func runUninstall(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("uninstall", stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	name, err := serviceNameFrom(&o, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	selected := platformOrDetect(plat)
	if o.dryRun {
		fmt.Fprintf(stdout, "dry run: would uninstall %q\n", name)
		return exitOK
	}
	if err := selected.Uninstall(ctx, name, o.purge); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "uninstalled %q\n", name)
	return exitOK
}

// runStartStop implements "obey start" and "obey stop".
func runStartStop(action string, args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet(action, stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	name, err := serviceNameFrom(&o, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	selected := platformOrDetect(plat)
	if o.dryRun {
		fmt.Fprintf(stdout, "dry run: would %s %q\n", action, name)
		return exitOK
	}

	var aerr error
	if action == "start" {
		aerr = selected.Start(ctx, name)
	} else {
		aerr = selected.Stop(ctx, name)
	}
	if aerr != nil {
		return fail(stderr, aerr)
	}
	fmt.Fprintf(stdout, "%sed %q\n", strings.TrimSuffix(action, "e"), name)
	return exitOK
}

func runRestart(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("restart", stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	name, err := serviceNameFrom(&o, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}

	selected := platformOrDetect(plat)
	if o.dryRun {
		fmt.Fprintf(stdout, "dry run: would restart %q\n", name)
		return exitOK
	}

	ctx, stop := signalContext()
	defer stop()
	if err := restartService(ctx, selected, name); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "restarted %q\n", name)
	return exitOK
}

func restartService(ctx context.Context, platform bend2will.Platform, name string) error {
	if err := platform.Stop(ctx, name); err != nil {
		return err
	}
	if err := waitForServiceState(ctx, platform, name, bend2will.StatusStopped); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := platform.Start(ctx, name); err != nil {
		return err
	}
	return waitForServiceState(ctx, platform, name, bend2will.StatusRunning)
}

func waitForServiceState(ctx context.Context, platform bend2will.Platform, name string, want bend2will.Status) error {
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		status, err := platform.Status(ctx, name)
		if err != nil {
			return err
		}
		if status.State == want || want == bend2will.StatusStopped && status.State == bend2will.StatusFailed {
			return nil
		}
		if status.State == bend2will.StatusFailed {
			return fmt.Errorf("service %q failed while waiting for %s", name, want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("timed out waiting for service %q to reach %s", name, want)
		case <-ticker.C:
		}
	}
}

// runStatus implements "obey status".
func runStatus(args []string, stdout, stderr *os.File, plat ...bend2will.Platform) exitCode {
	fs := newFlagSet("status", stderr)
	var o options
	o.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage
	}

	name, err := serviceNameFrom(&o, fs.Args())
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	st, err := platformOrDetect(plat).Status(ctx, name)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, st.String())
	return exitOK
}

// serviceNameFrom works out which service an operation refers to: the first
// positional argument, or -name.
func serviceNameFrom(o *options, args []string) (string, error) {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return args[0], nil
	}
	if o.name != "" {
		return o.name, nil
	}
	return "", fmt.Errorf("no service named: pass a name, for example 'obey report myapp'")
}
