package bend2will

import "os"

// selfExePath returns the absolute path of the running binary.
//
// Generated service definitions embed this path, so it is resolved once at
// install time rather than at run time. os.Executable is already absolute on
// Linux and on Windows 10 and later; EvalSymlinks is applied so that a
// symlinked binary is written into the definition under its real name, which
// keeps a `readlink` of the unit file meaningful.
func selfExePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepathAbsEvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}
