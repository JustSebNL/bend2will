package bend2will

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// The supervisor tests need a real, controllable child process. Shelling out to
// the platform's shell would make them depend on shell syntax and on quoting
// rules that differ between the three targets, so they run a purpose-built helper
// instead.
//
// The helper is ordinary Go source under testdata/helper, copied into a temporary
// directory and compiled once per test run. Building it through the same go tool
// that is running the tests means no separate toolchain assumptions are made.
var (
	buildHelperOnce sync.Once
	buildHelperPath string
	buildHelperErr  error
)

func helper(t *testing.T) string {
	t.Helper()
	buildHelperOnce.Do(buildHelper)
	if buildHelperErr != nil {
		t.Fatalf("building the test helper: %v", buildHelperErr)
	}
	return buildHelperPath
}

func buildHelper() {
	dir := filepath.Join(".tmp", "helper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		buildHelperErr = err
		return
	}

	name := "helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	absolutePath, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		buildHelperErr = err
		return
	}
	buildHelperPath = absolutePath

	// Copy the helper source in, along with a go.mod so the build does not depend
	// on where the repository happens to live.
	src, err := os.ReadFile(filepath.Join("testdata", "helper", "main.go"))
	if err != nil {
		buildHelperErr = err
		return
	}
	pkg := filepath.Join(dir, "helper")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		buildHelperErr = err
		return
	}
	if err := os.WriteFile(filepath.Join(pkg, "main.go"), src, 0o644); err != nil {
		buildHelperErr = err
		return
	}
	goMod := "module bend2willhelper\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(pkg, "go.mod"), []byte(goMod), 0o644); err != nil {
		buildHelperErr = err
		return
	}

	cmd := exec.Command(goToolPath(), "build", "-o", buildHelperPath, ".")
	cmd.Dir = pkg
	// CGO off keeps the helper static and free of any toolchain surprise, and
	// GOFLAGS off stops a caller's -race or -mod settings leaking in.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		buildHelperErr = &buildError{output: string(out), err: err}
	}
}

// goToolPath locates the go command.
func goToolPath() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	// `go test` is running, so a go executable exists even when it is not on PATH.
	if goroot := runtime.GOROOT(); goroot != "" {
		candidate := filepath.Join(goroot, "bin", "go")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		return candidate
	}
	return "go"
}

// buildError carries the helper compiler's own diagnostics, which are the only
// useful thing to report if the build fails.
type buildError struct {
	output string
	err    error
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.output }
func (e *buildError) Unwrap() error { return e.err }
