package main

import (
	"flag"
	"strings"
)

// reorderArgs moves flags ahead of positional arguments so that Go's flag
// package can parse them.
//
// The flag package stops at the first non-flag token, which would make
//
//	obey print myapp --command /usr/bin/myapp --arguments --serve
//
// silently ignore everything after "myapp". Silently ignoring flags is the worst
// possible failure for a tool that installs services, so the two are separated
// before parsing.
//
// The split is driven by the FlagSet's own metadata rather than by a hand-kept
// list, so a flag that changes from taking a value to not taking one cannot make
// this go out of step with the real parser. A "--" terminator ends flag parsing
// and forces everything after it to be positional.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	rest := make([]string, 0, 4)

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--":
			// Everything after the terminator is positional, by definition.
			rest = append(rest, args[i+1:]...)
			return append(flags, rest...)

		case !looksLikeFlag(arg):
			rest = append(rest, arg)

		case takesNoValue(fs, arg):
			flags = append(flags, arg)

		default:
			flags = append(flags, arg)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, rest...)
}

// looksLikeFlag reports whether a token is a flag rather than a value.
//
// A bare "-" is a conventional stand-in for stdin, and a negative number is a
// value, so neither is treated as a flag.
func looksLikeFlag(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' {
		return false
	}
	if arg == "-" || arg == "--" {
		return false
	}
	// "-5" and "-1.5" are numbers, not flags.
	if rest := arg[1:]; rest[0] >= '0' && rest[0] <= '9' {
		return false
	}
	return true
}

// takesNoValue reports whether a flag is a boolean, which therefore does not
// consume the following token.
func takesNoValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")

	// The flag package handles -h and -help itself and treats them as boolean.
	if name == "h" || name == "help" {
		return true
	}
	f := fs.Lookup(name)
	if f == nil {
		// An unknown flag: assume it takes a value, which is the common case and
		// the safer default, because the flag package will report it either way.
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return bf.IsBoolFlag()
	}
	return false
}

// parse parses args, tolerating flags and positional arguments in any order.
func parse(fs *flag.FlagSet, args []string) error {
	return fs.Parse(reorderArgs(fs, args))
}
