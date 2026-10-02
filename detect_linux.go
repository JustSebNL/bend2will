//go:build linux

package bend2will

// newPlatform returns the systemd backend.
func newPlatform() Platform { return newLinuxPlatform() }
