# NSSM-Compatible Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (or superpowers:subagent-driven-development) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Bend2Will install, configure, query, dump, start, stop, restart, and remove Windows services through the common NSSM-compatible workflow while retaining the existing portable supervisor and CLI forms.

**Architecture:** Keep `bend2will.Platform` unchanged for portable backends. Add optional Windows-only service capabilities for loading and editing the persisted canonical `Config`, store that configuration as versioned JSON under the service registry key, and make the SCM `ImagePath` a stable `obey service --name <name>` launcher contract. The CLI will use the optional capability for `set`, `get`, and `dump`, while `service` mode loads the persisted configuration and invokes the existing supervisor in strict service mode; older flag-embedded ImagePaths remain accepted as a fallback.

**Tech Stack:** Go standard library, Windows SCM and registry APIs through the existing dependency-free lazy syscall bindings, JSON, `flag.FlagSet`, and the repository’s existing cross-platform test/build targets.

**Spec:** `docs/superpowers/specs/2026-09-28-nssm-compatible-management-design.md`

## Global Constraints

- Bend2Will will provide a practical NSSM replacement on Windows while keeping the existing `obey` CLI, portable supervisor, and cross-platform library.
- The SCM `ImagePath` will contain only the stable launcher contract: `<obey.exe> service --name <service-name>`.
- Existing long-form `--command`, `--arguments`, configuration-file, and `install`/`uninstall` forms remain valid.
- Windows SCM query/start/stop operations request connection and the minimum service rights they need; install/remove retain configuration rights.
- A live SCM smoke test is only reported when run against a disposable service name with administrator rights; source and cross-build success are not live-service proof.
- Child stdout and stderr are independently configurable; when only one NSSM output path is configured, only that stream is redirected.

## Review Focus

- A positional install with an application argument containing spaces must preserve the exact argv vector; Task 1 pins this with a parser round-trip test.
- A service launched by SCM with only `--name` must load persisted configuration, while an older ImagePath with `--command` must still run; Task 2 tests both dispatch paths.
- Unknown, malformed, and unsupported NSSM parameters must fail with the parameter name and supported alternatives; Task 3 tests each error class.
- Start/stop/uninstall must not report success while SCM remains pending and must honor context cancellation; Task 4 exercises pending-state and cancellation paths.
- Configuring only stdout or only stderr must not redirect the other stream; Task 5 tests the four routing combinations.

### Task 1: NSSM-Compatible CLI Surface

**Files:**
- Modify: `cmd/obey/main.go` to route `remove` and the management commands while preserving existing aliases.
- Modify: `cmd/obey/commands.go` to parse positional `install <name> <application> [arguments...]` and dispatch `set`, `get`, and `dump`.
- Modify: `cmd/obey/flags.go` to preserve the existing option form and expose the parameter values needed by management commands.
- Create: `cmd/obey/nssm_cli_test.go` for parser, alias, and usage behavior.

**Interfaces:**
- Consumes: `options.config`, `serviceNameFrom`, `platformOrDetect`, and the optional editor interface defined in Task 2.
- Produces: `parsePositionalInstall(args []string) (name, application string, applicationArgs []string, err error)`, `runSet`, `runGet`, `runDump`, and `remove` as an alias of uninstall.

- [ ] **Step 1: Write the failing tests**

```go
func TestParsePositionalInstallPreservesApplicationArguments(t *testing.T) {
	name, app, args, err := parsePositionalInstall([]string{"web", `C:\Program Files\Web\web.exe`, "--title", "two words"})
	if err != nil { t.Fatal(err) }
	if name != "web" || app != `C:\Program Files\Web\web.exe` || !reflect.DeepEqual(args, []string{"--title", "two words"}) {
		t.Fatalf("got %q %q %#v", name, app, args)
	}
}

func TestRunRecognizesNSSMAliases(t *testing.T) {
	for _, command := range []string{"remove", "set", "get", "dump"} {
		if !commandIsKnown(command) { t.Fatalf("%q is not recognized", command) }
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./cmd/obey -run 'Test(ParsePositionalInstall|RunRecognizesNSSMAliases)' -count=1`

Expected: FAIL because the positional parser and management command routing do not exist yet.

- [ ] **Step 3: Implement the minimal CLI behavior**

Add `parsePositionalInstall` with exactly three validation rules: require a non-empty service name, require a non-empty application, and return all remaining tokens unchanged. In `runInstall`, use this parser only when the caller supplied at least two positional tokens and did not provide `--command`, `-c`, or `--config`; otherwise retain `options.config` unchanged. Add `remove` to the uninstall switch and route `set`, `get`, and `dump` to capability-aware command functions. Reject these three commands before platform access when no service name is supplied.

- [ ] **Step 4: Run focused and regression tests**

Run: `go test ./cmd/obey -run 'Test(ParsePositionalInstall|RunRecognizesNSSMAliases|TestRun|TestServiceName)' -count=1`

Expected: PASS, with the existing CLI tests still passing.

- [ ] **Step 5: Commit the CLI slice**

```powershell
git add cmd/obey/main.go cmd/obey/commands.go cmd/obey/flags.go cmd/obey/nssm_cli_test.go
git commit -m "feat: add NSSM-compatible obey command surface"
```

### Task 2: Persisted Windows Service Configuration and Service Mode

**Files:**
- Modify: `platform.go` to define optional `ServiceConfigLoader` and `ServiceEditor` capability interfaces without changing `Platform`.
- Create: `service_config_windows.go` for versioned JSON persistence under `Parameters\\Bend2WillConfig` and Windows service configuration loading.
- Create: `service_config_other.go` for a deterministic unsupported-operation error on non-Windows builds.
- Modify: `platform_windows.go` so install writes the canonical config before SCM registration, uses `obey service --name <name>` as ImagePath, applies SCM metadata/dependencies/start settings, and rolls back partial state.
- Modify: `winapi_windows.go` to bind the registry and `ChangeServiceConfigW` entry points needed by the isolated implementation.
- Modify: `cmd/obey/commands.go` so `service` mode loads persisted configuration when `--command` is absent and forces `ModeService`; retain the current embedded-flag fallback.
- Create: `service_config_windows_test.go` for serialization, validation, ImagePath, and fake API rollback tests.

**Interfaces:**
- Consumes: `Config`, `InstallOptions`, `windowsPlatform`, and the stable service launcher contract from the spec.
- Produces: `type ServiceConfigLoader interface { LoadServiceConfig(context.Context, string) (Config, error) }`, `type ServiceEditor interface { SetServiceParameter(context.Context, string, string, []string) error; GetServiceParameter(context.Context, string, string) (string, error); DumpServiceParameters(context.Context, string) ([]string, error) }`, and versioned `persistedServiceConfig` JSON containing `Version`, `Config`, and `InstallOptions`.

- [ ] **Step 1: Write failing persistence and dispatch tests**

```go
func TestPersistedServiceConfigRoundTrip(t *testing.T) {
	original := DefaultConfig("web", `C:\Program Files\Web\web.exe`)
	original.Args = []string{"--title", "two words"}
	original.Env = []string{"WEB_MODE=prod"}
	got, err := decodePersistedServiceConfig(encodePersistedServiceConfig(original, InstallOptions{}))
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(got.Config, original) { t.Fatalf("round trip changed config: %#v", got.Config) }
}

func TestImagePathUsesStableServiceLauncher(t *testing.T) {
	cfg := DefaultConfig("web", `C:\web.exe`)
	path, err := newWindowsPlatform().serviceImagePath(&cfg)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(path, ` service --name web`) || strings.Contains(path, "--command") { t.Fatalf("unstable ImagePath: %s", path) }
}
```

- [ ] **Step 2: Run the focused Windows tests and verify failure**

Run: `go test . -run 'Test(PersistedServiceConfigRoundTrip|ImagePathUsesStableServiceLauncher)' -count=1`

Expected: FAIL because the persisted representation and stable ImagePath helper are absent.

- [ ] **Step 3: Implement versioned persistence and loader**

Use `encoding/json` with `const persistedServiceConfigVersion = 1`. Validate `Config` before encoding and after decoding. Store UTF-16 JSON with `RegCreateKeyExW`/`RegSetValueExW` and read it with `RegQueryValueExW`; never include the JSON value in errors or logs. Return an error that names the service when the value is missing or has an unsupported version. Implement `LoadServiceConfig` on `windowsPlatform` and return `ErrServiceManagementUnsupported` on other platforms.

- [ ] **Step 4: Implement stable service dispatch and rollback**

Change `imagePathArgs` to return only the executable, `service`, `--name`, and service name. Add `serviceImagePath` as a testable wrapper. In `Install`, validate and persist before `CreateServiceW`, configure the service with `ChangeServiceConfigW`/`ChangeServiceConfig2W`, and delete both the registry value and service handle if a later step fails. In `runSupervise`, if service mode has a name but no command, type-assert `ServiceConfigLoader`, load the config, set `cfg.Mode = "service"`, and continue through `RunWithPlatformContext`; if command flags are present, keep the existing fallback path.

- [ ] **Step 5: Run focused, Windows, and cross-platform tests**

Run: `go test . ./cmd/obey -run 'Test(PersistedServiceConfig|ImagePath|ServiceMode)' -count=1`; then `go test ./...`; then `GOOS=linux go test ./...` and `GOOS=darwin go test ./...`.

Expected: PASS on Windows and both cross-platform test builds; non-Windows code returns an explicit unsupported-operation error for service editing.

- [ ] **Step 6: Commit the persistence and dispatch slice**

```powershell
git add platform.go platform_windows.go winapi_windows.go service_config_windows.go service_config_other.go service_config_windows_test.go cmd/obey/commands.go
git commit -m "feat: persist Windows service configuration"
```

### Task 3: NSSM Parameter Mapping, Get, Set, and Dump

**Files:**
- Create: `nssm_parameters.go` for canonical parameter names, validation, conversion, and deterministic dump generation.
- Create: `nssm_parameters_test.go` for all supported mappings and failure messages.
- Modify: `service_config_windows.go` to load, update, and save `Config` plus apply SCM metadata/start/dependency changes.
- Modify: `cmd/obey/commands.go` to format one-value `get`, all-value `get`, and reproducible `dump` output.
- Modify: `README.md` and `HELP.md` to document the exact supported command examples and unsupported-parameter behavior.

**Interfaces:**
- Consumes: `ServiceEditor` and persisted config from Task 2.
- Produces: `nssmParameterNames []string`, `setNSSMParameter(*Config, *InstallOptions, name string, values []string) error`, `getNSSMParameter(Config, InstallOptions, name string) (string, error)`, and `dumpNSSMParameters(Config, InstallOptions) []string`.

- [ ] **Step 1: Write failing mapping tests**

```go
func TestNSSMParametersMapCanonicalSettings(t *testing.T) {
	cfg := DefaultConfig("web", `C:\web.exe`)
	if err := setNSSMParameter(&cfg, nil, "AppParameters", []string{"--port", "8080"}); err != nil { t.Fatal(err) }
	if got, err := getNSSMParameter(cfg, InstallOptions{}, "AppParameters"); err != nil || got != `--port 8080` { t.Fatalf("got %q, %v", got, err) }
}

func TestNSSMUnknownParameterNamesSupportedAlternatives(t *testing.T) {
	cfg := DefaultConfig("web", `C:\web.exe`)
	err := setNSSMParameter(&cfg, nil, "NotAParameter", []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "NotAParameter") || !strings.Contains(err.Error(), "Application") { t.Fatalf("bad error: %v", err) }
}

func TestNSSMDumpIsDeterministicAndReproducible(t *testing.T) {
	cfg := DefaultConfig("web", `C:\web.exe`)
	cfg.Args = []string{"--title", "two words"}
	first := dumpNSSMParameters(cfg, InstallOptions{})
	second := dumpNSSMParameters(cfg, InstallOptions{})
	if !reflect.DeepEqual(first, second) || !strings.Contains(strings.Join(first, "\n"), "obey set web AppPath") { t.Fatalf("unstable dump: %#v", first) }
}
```

- [ ] **Step 2: Run mapping tests and verify failure**

Run: `go test . -run 'TestNSSM' -count=1`

Expected: FAIL because parameter conversion and deterministic dump functions do not exist.

- [ ] **Step 3: Implement the complete supported mapping**

Support `Application`/`AppPath`, `AppParameters`, `AppDirectory`, `AppStdout`, `AppStderr`, `AppEnvironmentExtra`, `DisplayName`, `Description`, `AppRestartDelay`, `AppExit`, `AppStopMethodConsole`, `AppStopMethodSkip`, `DependOnService`, `Start`, and `ObjectName`. Require exactly the value shapes each setting needs: one scalar for path/metadata/delay/start/account, a JSON-or-token vector for parameters/environment/dependencies, and the documented `AppExit` policy spellings. Reject empty paths, malformed `KEY=VALUE`, negative delays, unsupported accounts, and unsupported parameter names with a stable list of alternatives.

- [ ] **Step 4: Implement get/set persistence and SCM updates**

Load the canonical config, apply one validated parameter change, persist it atomically, then update SCM metadata/start/dependencies when the parameter affects SCM. On failure, leave the previous JSON intact. `get` without a parameter returns the supported names and values in the same order as `nssmParameterNames`; `dump` emits `obey set <name> <parameter> <quoted values>` lines in that order using the existing Windows argument quoting helper.

- [ ] **Step 5: Run mapping, CLI, and documentation verification**

Run: `go test . ./cmd/obey -run 'TestNSSM|TestRun' -count=1`; then `go test ./...`.

Expected: PASS, and `obey help`/README examples show `install`, `set`, `get`, `dump`, and `remove` without implying unsupported NSSM GUI behavior.

- [ ] **Step 6: Commit the parameter-management slice**

```powershell
git add nssm_parameters.go nssm_parameters_test.go service_config_windows.go cmd/obey/commands.go README.md HELP.md
git commit -m "feat: add NSSM parameter management"
```

### Task 4: Least-Privilege and Pending-State SCM Operations

**Files:**
- Modify: `platform_windows.go` to use exact manager/service access masks, wait for start and stop state transitions, and preserve context cancellation.
- Modify: `config_windows_test.go` and create `platform_windows_management_test.go` for access-mask selection and pending-state behavior.
- Modify: `cmd/obey/commands.go` only if start/restart output needs to distinguish a completed transition from a request accepted by SCM.

**Interfaces:**
- Consumes: stable service installation and parameter storage from Tasks 2–3.
- Produces: `windowsManagerAccess(operation string) uint32`, `windowsServiceAccess(operation string) uint32`, and context-aware start/stop waits that return only after the target state is observed.

- [ ] **Step 1: Write failing access and lifecycle tests**

```go
func TestWindowsOperationAccessIsLeastPrivilege(t *testing.T) {
	if got := windowsServiceAccess("status"); got != svcQueryStatus { t.Fatalf("status access %#x", got) }
	if got := windowsServiceAccess("start"); got != svcStart|svcQueryStatus { t.Fatalf("start access %#x", got) }
	if got := windowsServiceAccess("stop"); got != svcStop|svcQueryStatus { t.Fatalf("stop access %#x", got) }
	if got := windowsServiceAccess("remove"); got != svcStop|svcQueryStatus|svcDelete { t.Fatalf("remove access %#x", got) }
}

func TestStartWaitHonorsCancellationWhilePending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForServiceState(ctx, func() (uint32, error) { return serviceStartPending, nil }, serviceRunning, time.Second)
	if !errors.Is(err, context.Canceled) { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test . -run 'TestWindows(OperationAccess|StartWait)' -count=1`

Expected: FAIL because operation-specific access helpers and start-state waiting are not present.

- [ ] **Step 3: Implement exact access masks and waits**

Use `scManagerConnect` for status/start/stop and add `scManagerCreateService` only for install. Open status with `svcQueryStatus`, start with `svcStart|svcQueryStatus`, stop with `svcStop|svcQueryStatus`, and remove with `svcStop|svcQueryStatus|svcDelete`. After `StartServiceW`, poll for `serviceRunning`; after stop, keep the existing pending-state wait and return timeout/cancellation without deleting a running service. Reuse the caller context and a bounded operation deadline.

- [ ] **Step 4: Run lifecycle and full verification**

Run: `go test . -run 'Test(Windows|Start|Stop|Wait|RequestStop)' -count=1`; then `go test ./...`; then `go vet ./...`.

Expected: PASS with no regression in the existing SCM callback tests.

- [ ] **Step 5: Commit the lifecycle slice**

```powershell
git add platform_windows.go config_windows_test.go platform_windows_management_test.go cmd/obey/commands.go
git commit -m "fix: wait for Windows service lifecycle transitions"
```

### Task 5: Independent Child Stdout and Stderr Routing

**Files:**
- Modify: `config.go` to add validated `StdoutPath` and `StderrPath` fields while retaining `LogPath`.
- Modify: `cmd/obey/flags.go` and `config_file.go` to parse `--stdout`, `--stderr`, `stdout`, and `stderr`.
- Modify: `child_windows.go` and `child_unix.go` to route each stream independently and close all opened handles.
- Create: `child_io_test.go` or extend the platform-specific child tests with the four routing combinations.
- Modify: `nssm_parameters.go` to map `AppStdout` and `AppStderr` directly to the two fields.

**Interfaces:**
- Consumes: canonical config and NSSM mapping from Tasks 2–3.
- Produces: independent `Config.StdoutPath` and `Config.StderrPath` semantics; `LogPath` remains the supervisor’s own diagnostic sink.

- [ ] **Step 1: Write failing routing tests**

```go
func TestPrepareChildIOOnlyRedirectsConfiguredStream(t *testing.T) {
	cmd := exec.Command("test-child")
	cfg := DefaultConfig("svc", "test-child")
	cfg.StdoutPath = filepath.Join(t.TempDir(), "out.log")
	closer, err := prepareChildIOWithConsole(cmd, &cfg, discardLogger{}, true)
	if err != nil { t.Fatal(err) }
	defer closer.Close()
	if cmd.Stdout == nil || cmd.Stderr != os.Stderr { t.Fatalf("stdout/stderr routing was not independent") }
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run: `go test . -run TestPrepareChildIOOnlyRedirectsConfiguredStream -count=1`

Expected: FAIL because `Config.StdoutPath` and independent stream routing are absent.

- [ ] **Step 3: Implement independent paths**

Open each non-empty, non-`-`, non-`@` stream path independently with the existing append/truncate rules. If only one path is set, leave the other stream attached to the current console/log behavior. Preserve `@` and `-` semantics for both streams, close every file/pipe in the returned closer, and validate that a configured path is not a directory.

- [ ] **Step 4: Run focused and full tests**

Run: `go test . -run 'TestPrepareChildIO|TestConfig|TestNSSM' -count=1`; then `go test ./...`; then `GOOS=linux go test ./...` and `GOOS=darwin go test ./...`.

Expected: PASS with existing log routing unchanged when neither dedicated stream path is configured.

- [ ] **Step 5: Commit the I/O slice**

```powershell
git add config.go config_file.go cmd/obey/flags.go child_windows.go child_unix.go child_io_test.go nssm_parameters.go
git commit -m "feat: support independent service stdout and stderr"
```

### Task 6: Final Verification and Handoff

**Files:**
- Modify: `README.md`, `HELP.md`, and the active TimeKeeper evidence only as needed to record verified behavior.

- [ ] **Step 1: Run the complete verification matrix**

Run: `go test ./...`; `go vet ./...`; `GOOS=windows go test ./...`; `GOOS=linux go test ./...`; `GOOS=darwin go test ./...`; and the project’s documented cross-build command from `Makefile`.

- [ ] **Step 2: Inspect the final diff and executable help**

Run: `git diff --check`; `go run ./cmd/obey help`; `go run ./cmd/obey install --help`; and inspect `git status --short` to ensure no unrelated files were changed.

- [ ] **Step 3: Run a live SCM smoke test only when permitted by the host**

Use a disposable service name, a harmless executable, and administrator rights. Verify install, `get`, `set`, `dump`, status, start, stop, and remove. If SCM access is denied, record that exact limitation and do not present source tests as a live-service result.

- [ ] **Step 4: Update TimeKeeper with evidence and close only verified work**

Record the test commands, cross-build results, live SCM result or access-denied limitation, and remaining follow-up in `T-10572`/`SP-10573`. Complete the task and sprint only when all acceptance items are verified; otherwise leave the task/sprint open with a recoverable checkpoint.

