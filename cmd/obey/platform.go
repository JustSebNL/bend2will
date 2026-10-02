package main

import (
	"os"

	"github.com/JustSebNL/bend2will"
)

// platformOrDetect returns the platform a subcommand should act on.
//
// Every subcommand takes an optional platform so that the same implementation
// serves both editions: a plain verb detects the native backend, and
// "obey portable <verb>" passes a portable wrapper instead. That is what keeps
// the two editions from drifting apart.
func platformOrDetect(plat []bend2will.Platform) bend2will.Platform {
	if len(plat) > 0 && plat[0] != nil {
		return plat[0]
	}
	return bend2will.Detect()
}

// portablePlatform builds the per-user platform for "obey portable".
//
// The root can be overridden with OB portable_DIR, which is what makes the
// portable edition testable and what lets an operator keep several portable
// services side by side in one chosen directory.
func portablePlatform() (bend2will.Platform, error) {
	root, scope, err := bend2will.PortableRoot()
	if err != nil {
		return nil, err
	}
	if override := os.Getenv("OBEY_PORTABLE_DIR"); override != "" {
		root = override
	}
	return bend2will.NewPortablePlatform(bend2will.Detect(), root, scope), nil
}

// portableRootForHelp reports where a portable install would go, so that the
// help text can tell the operator before they try anything.
func portableRootForHelp() string {
	root, _, err := bend2will.PortableRoot()
	if err != nil {
		return "(unavailable: " + err.Error() + ")"
	}
	return root
}
