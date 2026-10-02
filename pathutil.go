package bend2will

import (
	"os"
	"os/exec"
	"path/filepath"
)

// filepathAbsEvalSymlinks resolves a path to its real, absolute form.
//
// A Windows service definition must not contain a path that depends on a
// working directory or on a drive mapping, and a symlinked binary is better
// recorded under its target so that the definition keeps working if the link is
// replaced. Failures are not fatal: the caller falls back to the unresolved
// path, which is still correct on every platform bend2will targets.
func filepathAbsEvalSymlinks(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// isExecutable reports whether path names a file that can be run.
//
// It is used to turn a confusing "fork/exec: no such file or directory" into a
// message that names the actual problem, which is the single most common
// misconfiguration when installing a service by hand.
func isExecutable(path string) bool {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().IsRegular()
	}
	// Not a path, so it must be a bare name that exec will look up on PATH.
	_, err := exec.LookPath(path)
	return err == nil
}
