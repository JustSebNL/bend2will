//go:build windows

package bend2will

// newPlatform returns the Windows Service Control Manager backend.
func newPlatform() Platform { return newWindowsPlatform() }
