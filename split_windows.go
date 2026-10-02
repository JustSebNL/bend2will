//go:build windows

package bend2will

import "strings"

// splitForExecution splits a command string into an argv for running as a
// program.
//
// On Windows the command string is a Windows command line, so it is parsed with
// the CommandLineToArgvW rules. Using shell rules here would be a real bug rather
// than a stylistic one: a path such as C:\Program Files\thing\thing.exe has its
// backslashes consumed as escapes and no longer names a file that exists.
func splitForExecution(s string) ([]string, error) {
	argv := commandLineToArgvW(s)
	if len(argv) == 0 {
		return nil, &execSplitError{command: s, reason: "the command is empty"}
	}
	// A path that exists as written is taken verbatim, because that is almost
	// always what the operator meant and it removes any doubt about quoting.
	if len(argv) == 1 && isExecutable(argv[0]) {
		return argv, nil
	}
	return argv, nil
}

// commandLineToArgvW implements the CommandLineToArgvW parsing rules.
//
// The rule for a run of backslashes before a quote: 2n backslashes then a quote
// yields n backslashes and toggles quoting; 2n+1 backslashes then a quote yields
// n backslashes and a literal quote; a run not followed by a quote is literal and
// the character after it is then processed normally.
func commandLineToArgvW(cmdline string) []string {
	const (
		quote     = '"'
		backslash = '\\'
	)

	var (
		args    []string
		cur     strings.Builder
		inQuote bool
		started bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}

	for i := 0; i < len(cmdline); {
		c := cmdline[i]

		if c == backslash {
			n := 0
			for i < len(cmdline) && cmdline[i] == backslash {
				n++
				i++
			}
			if i == len(cmdline) {
				// A trailing run with nothing after it is literal.
				cur.WriteString(strings.Repeat(string(backslash), n))
				started = true
				break
			}
			if cmdline[i] == quote {
				cur.WriteString(strings.Repeat(string(backslash), n/2))
				if n%2 == 1 {
					cur.WriteByte(quote) // an escaped quote
				} else {
					inQuote = !inQuote // a real quote
				}
				started = true
				i++
				continue
			}
			// Not before a quote, so the run is literal. Leave i where it is so the
			// next character is processed by the next iteration.
			cur.WriteString(strings.Repeat(string(backslash), n))
			started = true
			continue
		}

		if c == quote {
			inQuote = !inQuote
			started = true
			i++
			continue
		}

		if (c == ' ' || c == '\t') && !inQuote {
			flush()
			i++
			continue
		}

		cur.WriteByte(c)
		started = true
		i++
	}
	flush()
	return args
}
