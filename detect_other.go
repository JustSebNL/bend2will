//go:build !windows && !linux && !darwin

package bend2will

import "runtime"

// newPlatform returns the fallback backend, which supervises correctly and
// writes a POSIX sh wrapper for whatever init system the host has.
func newPlatform() Platform { return newFallbackPlatform(runtime.GOOS) }
