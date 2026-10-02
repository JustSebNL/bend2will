//go:build darwin

package bend2will

// newPlatform returns the launchd backend.
func newPlatform() Platform { return newDarwinPlatform() }
