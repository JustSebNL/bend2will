//go:build !windows && !linux && !darwin

package bend2will

import "runtime"

// hostOS reports the operating system this binary targets. It exists so that
// portable mode can be tested on a host that is not the one being simulated.
func hostOS() string { return runtime.GOOS }
