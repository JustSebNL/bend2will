//go:build !windows

package bend2will

// portableIsUserService reports whether this platform has a per-user service
// manager. Linux and macOS both do.
func portableIsUserService() bool { return true }

// launcherName and startupFolder are only reachable when the answer is false,
// which cannot happen on Unix. They are defined so that the shared file compiles
// on every platform.
func launcherName(*Config) string           { return "" }
func startupFolder() string                 { return "" }
func launcherScript(*Config, string) string { return "" }
