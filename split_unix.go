//go:build !windows

package bend2will

// splitForExecution splits a command string into an argv for running as a
// program.
//
// On Unix a command string is a shell command line, so shell rules apply: single
// and double quotes group, and a backslash escapes the next character. That is
// what a configuration file, a readiness check and a shutdown command are written
// in.
func splitForExecution(s string) ([]string, error) {
	argv, err := splitCommandLine(s)
	if err != nil {
		return nil, &execSplitError{command: s, reason: err.Error()}
	}
	if len(argv) == 0 {
		return nil, &execSplitError{command: s, reason: "the command is empty"}
	}
	return argv, nil
}

// execSplitError reports a command string that cannot be turned into an argv.
type execSplitError struct {
	command string
	reason  string
}

func (e *execSplitError) Error() string {
	return "bend2will: cannot split " + quoteForMessage(e.command) + ": " + e.reason
}
