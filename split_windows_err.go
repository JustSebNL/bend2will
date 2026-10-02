//go:build windows

package bend2will

// execSplitError reports a command string that cannot be turned into an argv.
type execSplitError struct {
	command string
	reason  string
}

func (e *execSplitError) Error() string {
	return "bend2will: cannot split " + quoteForMessage(e.command) + ": " + e.reason
}
