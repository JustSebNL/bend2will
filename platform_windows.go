//go:build windows

package bend2will

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// The Windows constants and lazily-bound entry points this file uses are
// declared once, in winapi_windows.go, so that the child, logging and service
// code all share one set of bindings.

// serviceStatus mirrors SERVICE_STATUS.
type serviceStatus struct {
	ServiceType             uint32
	CurrentState            uint32
	ControlsAccepted        uint32
	Win32ExitCode           uint32
	ServiceSpecificExitCode uint32
	CheckPoint              uint32
	WaitHint                uint32
}

// serviceTableEntry mirrors SERVICE_TABLE_ENTRYW.
type serviceTableEntry struct {
	ServiceName *uint16
	ServiceProc uintptr
}

// windowsPlatform integrates with the Service Control Manager.
//
// The SCM requires the process to call StartServiceCtrlDispatcherW, which blocks
// until the service stops. That single fact shapes the whole design: Connect
// installs a serviceMain callback, returns handled=true, and the supervision loop
// runs inside serviceMain on a thread the SCM created. Everything else about the
// supervisor is identical on all three platforms.
type windowsPlatform struct {
	mu          sync.Mutex
	serviceName string
	status      syscall.Handle
	stopper     *Stopper
	supervise   SuperviseFunc

	// finished is closed once the supervision loop has returned, so serviceMain
	// knows when it is safe to report SERVICE_STOPPED and return.
	finished chan struct{}

	// startWaitHintSeconds is how long the SCM will tolerate SERVICE_START_PENDING
	// before deciding the service is hung.
	startWaitHintSeconds uint32
}

var registerServiceCtrlHandlerCall = func(name *uint16, handler uintptr) (uintptr, error) {
	h, _, err := procRegisterServiceCtrlHandlerW.Call(uintptr(unsafe.Pointer(name)), handler)
	return h, err
}

var setServiceStatusCall = func(handle syscall.Handle, status *serviceStatus) error {
	ret, _, err := procSetServiceStatus.Call(uintptr(handle), uintptr(unsafe.Pointer(status)))
	if ret == 0 {
		return err
	}
	return nil
}

var startServiceCtrlDispatcherCall = func(entry *serviceTableEntry) (uintptr, error) {
	ret, _, err := procStartServiceCtrlDispatcherW.Call(uintptr(unsafe.Pointer(entry)))
	return ret, err
}

func newWindowsPlatform() Platform { return &windowsPlatform{} }

func (p *windowsPlatform) Name() string { return "windows service control manager" }

// Connect decides whether this process is a service and, if so, becomes one.
//
// A process started by the SCM has no console, which is the cheapest reliable
// signal available. When the operator explicitly asked for service mode we ask
// the SCM directly regardless: if the SCM says no, that is worth reporting,
// because it means the service definition is wrong.
func (p *windowsPlatform) Connect(ctx context.Context, cfg *Config, mode Mode, sup SuperviseFunc, stopper *Stopper) (bool, error) {
	if mode == ModeConsole {
		return false, nil
	}
	if mode == ModeAuto && hasConsole() {
		// A console means an operator is watching: supervise in the foreground.
		return false, nil
	}

	hint := uint32(30)
	if cfg.StartTimeout > 0 {
		hint = uint32(cfg.StartTimeout/time.Second) + 10
	}

	p.mu.Lock()
	p.serviceName = cfg.Name
	p.stopper = stopper
	p.supervise = sup
	p.finished = make(chan struct{})
	p.startWaitHintSeconds = hint
	p.mu.Unlock()

	namePtr, err := syscall.UTF16PtrFromString(cfg.Name)
	if err != nil {
		return false, fmt.Errorf("bend2will: service name %q: %w", cfg.Name, err)
	}

	table := [2]serviceTableEntry{
		{ServiceName: namePtr, ServiceProc: syscall.NewCallback(p.serviceMain)},
		{},
	}

	// Blocks until the service stops. Returns 0 when we are not a service.
	ret, callErr := startServiceCtrlDispatcherCall(&table[0])
	if ret != 0 {
		// serviceMain ran and has now returned: the service has stopped.
		return true, nil
	}

	if code, ok := callErr.(syscall.Errno); ok && code == errorFailedServiceController {
		if mode == ModeService {
			return false, fmt.Errorf("bend2will: the Service Control Manager did not start us as service %q; "+
				"check the service's ImagePath, and that it was started with `sc start`", cfg.Name)
		}
		return false, nil
	}
	return false, fmt.Errorf("bend2will: StartServiceCtrlDispatcher: %w", callErr)
}

// serviceMain is the SCM entry point. The SCM starts it on a thread of its own,
// so this is where supervision happens.
func (p *windowsPlatform) serviceMain(_, _ uintptr) uintptr {
	p.mu.Lock()
	hint := p.startWaitHintSeconds
	finished := p.finished
	serviceName := p.serviceName
	sup := p.supervise
	p.mu.Unlock()

	// RegisterServiceCtrlHandlerEx would also give us power events; the plain
	// handler is enough for stop and shutdown, which is all we need.
	namePtr, err := syscall.UTF16PtrFromString(serviceName)
	if err != nil {
		p.mu.Lock()
		p.finished = closedChan()
		p.mu.Unlock()
		return 0
	}
	h, err := registerServiceCtrlHandlerCall(namePtr, syscall.NewCallback(p.serviceControlHandler))
	runtime.KeepAlive(namePtr)
	if h == 0 {
		// Without a status handle we cannot report anything, so the service is
		// already doomed. signalStopped releases serviceMain's caller so that the
		// process can exit.
		p.mu.Lock()
		p.finished = closedChan()
		p.mu.Unlock()
		_ = err
		return 0
	}
	p.mu.Lock()
	p.status = syscall.Handle(h)
	p.mu.Unlock()

	_ = p.setStatus(serviceStartPending, 1, hint)

	// The control handler runs on a thread the SCM owns and must return promptly,
	// so the supervision loop gets a goroutine of its own.
	supervisionResult := make(chan error, 1)
	go func() {
		var err error
		if sup != nil {
			err = sup(context.Background())
		}
		supervisionResult <- err
		close(finished)
	}()

	<-finished
	if err := <-supervisionResult; err != nil {
		_ = p.setFailedStatus()
		return 0
	}
	_ = p.setStatus(serviceStopped, 0, 0)
	return 0
}

// serviceControlHandler answers the SCM. It runs on a thread the SCM owns, so it
// returns immediately: the actual shutdown work belongs to the supervisor.
func (p *windowsPlatform) serviceControlHandler(control uintptr) uintptr {
	switch control {
	case serviceControlStop, serviceControlShutdown:
		_ = p.setStatus(serviceStopPending, 0, 0)
		p.requestStop()
	case serviceControlInterrogate:
		// The SCM is asking for the status we have already reported.
	}
	return noError
}

func (p *windowsPlatform) requestStop() {
	p.mu.Lock()
	stopper := p.stopper
	p.mu.Unlock()
	if stopper != nil {
		stopper.Stop()
	}
}

// closedChan returns an already-closed channel.
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// ReportStatus publishes a lifecycle transition to the SCM.
func (p *windowsPlatform) ReportStatus(status Status) error {
	switch status {
	case StatusStarting:
		return p.setStatus(serviceStartPending, 0, 0)
	case StatusRunning:
		return p.setStatus(serviceRunning, 0, 0)
	case StatusStopping:
		return p.setStatus(serviceStopPending, 0, 0)
	case StatusStopped:
		return p.setStatus(serviceStopped, 0, 0)
	case StatusFailed:
		return p.setFailedStatus()
	default:
		return nil
	}
}

// setStatus calls SetServiceStatus, translating a failure into an error.
func (p *windowsPlatform) setStatus(state, checkpoint, waitHintSeconds uint32) error {
	return p.setStatusWithExitCode(state, checkpoint, waitHintSeconds, noError, 0)
}

func (p *windowsPlatform) setFailedStatus() error {
	return p.setStatusWithExitCode(serviceStopped, 0, 0, errorServiceSpecificError, 1)
}

func (p *windowsPlatform) setStatusWithExitCode(state, checkpoint, waitHintSeconds, win32ExitCode, serviceSpecificExitCode uint32) error {
	p.mu.Lock()
	h := p.status
	p.mu.Unlock()

	if h == 0 {
		return nil
	}

	// Accepting shutdown as well as stop matters: without it the SCM does not
	// send SERVICE_CONTROL_SHUTDOWN during a reboot and Windows times the
	// service out instead of stopping it cleanly.
	controlsAccepted := uint32(0)
	if state == serviceRunning {
		controlsAccepted = serviceAcceptStop | serviceAcceptShutdown
	}
	ss := serviceStatus{
		ServiceType:             serviceWin32OwnProcess,
		CurrentState:            state,
		ControlsAccepted:        controlsAccepted,
		Win32ExitCode:           win32ExitCode,
		ServiceSpecificExitCode: serviceSpecificExitCode,
		CheckPoint:              checkpoint,
		WaitHint:                waitHintSeconds * 1000,
	}
	if err := setServiceStatusCall(h, &ss); err != nil {
		return fmt.Errorf("bend2will: SetServiceStatus(%d): %w", state, err)
	}
	return nil
}

type windowsInstallSettings struct {
	serviceAccount           string
	startType                string
	startTypeValue           uintptr
	restartDelayMilliseconds uint32
}

type serviceDelayedAutoStartInfo struct {
	DelayedAutoStart uint32
}

type serviceFailureAction struct {
	Type  uint32
	Delay uint32
}

type serviceFailureActions struct {
	ResetPeriod  uint32
	RebootMsg    *uint16
	Command      *uint16
	ActionsCount uint32
	Actions      *serviceFailureAction
}

func newWindowsInstallSettings(opts InstallOptions) (windowsInstallSettings, error) {
	account, err := normalizeWindowsServiceAccount(opts.WindowsServiceAccount)
	if err != nil {
		return windowsInstallSettings{}, err
	}
	if opts.WindowsServiceRestartDelay < 0 {
		return windowsInstallSettings{}, fmt.Errorf("bend2will: Windows service restart delay must not be negative")
	}
	if !opts.WindowsServiceRestartOnFailure && opts.WindowsServiceRestartDelay != 0 {
		return windowsInstallSettings{}, fmt.Errorf("bend2will: Windows service restart delay requires restart-on-failure")
	}

	settings := windowsInstallSettings{
		serviceAccount: account,
		startType:      "demand",
		startTypeValue: serviceDemandStart,
	}
	if opts.Enable || opts.WindowsDelayedAutoStart {
		settings.startType = "auto"
		settings.startTypeValue = serviceAutoStart
	}
	if opts.WindowsDelayedAutoStart {
		settings.startType = "delayed-auto"
	}
	if opts.WindowsServiceRestartOnFailure {
		delay := opts.WindowsServiceRestartDelay
		if delay == 0 {
			delay = 5 * time.Second
		}
		settings.restartDelayMilliseconds, err = windowsRestartDelayMilliseconds(delay)
		if err != nil {
			return windowsInstallSettings{}, err
		}
	}
	return settings, nil
}

func normalizeWindowsServiceAccount(account string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(account)) {
	case "", "localsystem", `nt authority\localsystem`:
		return "", nil
	case "localservice", `nt authority\localservice`:
		return `NT AUTHORITY\LocalService`, nil
	case "networkservice", `nt authority\networkservice`:
		return `NT AUTHORITY\NetworkService`, nil
	default:
		return "", fmt.Errorf("bend2will: unsupported Windows service account %q (want LocalSystem, LocalService or NetworkService)", account)
	}
}

func windowsRestartDelayMilliseconds(delay time.Duration) (uint32, error) {
	if delay <= 0 {
		return 0, fmt.Errorf("bend2will: Windows service restart delay must be greater than zero")
	}
	milliseconds := (uint64(delay) + uint64(time.Millisecond) - 1) / uint64(time.Millisecond)
	if milliseconds > uint64(^uint32(0)) {
		return 0, fmt.Errorf("bend2will: Windows service restart delay exceeds the SCM limit")
	}
	return uint32(milliseconds), nil
}

func setWindowsServiceDelayedAutoStart(handle uintptr) error {
	settings := serviceDelayedAutoStartInfo{DelayedAutoStart: 1}
	ret, _, err := procChangeServiceConfig2W.Call(
		handle,
		serviceConfigDelayedAutoStartInfo,
		uintptr(unsafe.Pointer(&settings)),
	)
	runtime.KeepAlive(&settings)
	if ret == 0 {
		return fmt.Errorf("bend2will: configure delayed auto-start: %w", err)
	}
	return nil
}

func setWindowsServiceRecovery(handle uintptr, restartDelayMilliseconds uint32) error {
	actions := [3]serviceFailureAction{}
	for i := range actions {
		actions[i] = serviceFailureAction{Type: serviceActionRestart, Delay: restartDelayMilliseconds}
	}
	settings := serviceFailureActions{
		ResetPeriod:  uint32((24 * time.Hour) / time.Second),
		ActionsCount: uint32(len(actions)),
		Actions:      &actions[0],
	}
	ret, _, err := procChangeServiceConfig2W.Call(
		handle,
		serviceConfigFailureActions,
		uintptr(unsafe.Pointer(&settings)),
	)
	runtime.KeepAlive(&actions)
	runtime.KeepAlive(&settings)
	if ret == 0 {
		return fmt.Errorf("bend2will: configure Windows service recovery: %w", err)
	}
	return nil
}

// Definition describes how the service should be registered.
//
// The SCM has no definition file: the equivalent lives in the registry. What is
// rendered here is the sc.exe invocation that Install performs, so that
// `obey print` shows the operator exactly what will be written.
func (p *windowsPlatform) Definition(cfg *Config) (Definition, error) {
	return p.definitionWithOptions(cfg, InstallOptions{})
}

func (p *windowsPlatform) definitionWithOptions(cfg *Config, opts InstallOptions) (Definition, error) {
	if err := cfg.Validate(); err != nil {
		return Definition{}, err
	}
	settings, err := newWindowsInstallSettings(opts)
	if err != nil {
		return Definition{}, err
	}
	argv, err := p.imagePathArgs(cfg)
	if err != nil {
		return Definition{}, err
	}
	imagePath := windowsCommandLine(argv)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", firstLine(cfg.Description, "managed by bend2will"))
	fmt.Fprintf(&b, "#\n")
	// binPath is emitted raw rather than quoted. sc.exe takes everything after
	// "binPath= " to end of line as the value and strips at most one surrounding
	// pair of quotes, so wrapping a value that already contains quoted arguments
	// in another pair produces backslash-escaped quotes that sc.exe does not
	// unescape. The value is a command line and is already correctly quoted.
	fmt.Fprintf(&b, "sc.exe create %s binPath= %s start= %s DisplayName= %s",
		scQuote(cfg.Name), imagePath, settings.startType, scQuote(cfg.DisplayName))
	if settings.serviceAccount != "" {
		fmt.Fprintf(&b, " obj= %s password= \"\"", scQuote(settings.serviceAccount))
	}
	b.WriteByte('\n')
	if deps := requireDependency(cfg.Dependencies, "/"); deps != "" {
		fmt.Fprintf(&b, "sc.exe config %s depend= %s\n", scQuote(cfg.Name), scQuote(deps))
	}
	if len(cfg.Args) > 0 {
		// The program's own arguments are carried as one 0x1f-separated value,
		// because the SCM's ImagePath is a single command line. Say so, or the
		// separators look like corruption to whoever reads this.
		fmt.Fprintf(&b, "#\n# the program's arguments travel as one 0x1f-separated value:\n"+
			"#   %s\n", visibleSeparators(strings.Join(cfg.Args, "\x1f")))
	}
	if cfg.Dir != "" {
		fmt.Fprintf(&b, "#\n# the working directory is applied by the service itself: %s\n", cfg.Dir)
	}
	if opts.WindowsServiceRestartOnFailure {
		delay := settings.restartDelayMilliseconds
		fmt.Fprintf(&b, "sc.exe failure %s reset= 86400 actions= restart/%d/restart/%d/restart/%d\n",
			scQuote(cfg.Name), delay, delay, delay)
	}

	return Definition{
		Kind:     "service",
		Path:     fmt.Sprintf(`HKLM\SYSTEM\CurrentControlSet\Services\%s`, cfg.Name),
		Contents: b.String(),
		Instructions: []string{
			fmt.Sprintf("%s install %s", CommandName, cfg.Name),
		},
	}, nil
}

// Install registers the service with the SCM.
func (p *windowsPlatform) Install(ctx context.Context, cfg *Config, opts InstallOptions) error {
	if err := opts.Validate(p.Name()); err != nil {
		return err
	}
	if _, err := p.definitionWithOptions(cfg, opts); err != nil {
		return err
	}
	settings, err := newWindowsInstallSettings(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return nil
	}

	argv, err := p.imagePathArgs(cfg)
	if err != nil {
		return err
	}
	imagePath := windowsCommandLine(argv)

	namePtr, err := syscall.UTF16PtrFromString(cfg.Name)
	if err != nil {
		return fmt.Errorf("bend2will: service name %q: %w", cfg.Name, err)
	}
	displayPtr, err := syscall.UTF16PtrFromString(cfg.DisplayName)
	if err != nil {
		return fmt.Errorf("bend2will: display name %q: %w", cfg.DisplayName, err)
	}
	binPtr, err := syscall.UTF16PtrFromString(imagePath)
	if err != nil {
		return fmt.Errorf("bend2will: image path: %w", err)
	}
	depsPtr, err := utf16PtrFromMultiSz(cfg.Dependencies)
	if err != nil {
		return err
	}
	var serviceAccountPtr uintptr
	if settings.serviceAccount != "" {
		accountPtr, aerr := syscall.UTF16PtrFromString(settings.serviceAccount)
		if aerr != nil {
			return fmt.Errorf("bend2will: service account: %w", aerr)
		}
		serviceAccountPtr = uintptr(unsafe.Pointer(accountPtr))
	}

	mgr, err := p.openManager(scManagerConnect | scManagerCreateService)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(mgr)

	h, _, err := procCreateServiceW.Call(
		mgr,
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(displayPtr)),
		svcAllAccess,
		serviceWin32OwnProcess,
		settings.startTypeValue,
		serviceErrorNormal,
		uintptr(unsafe.Pointer(binPtr)),
		0,
		0,
		uintptr(unsafe.Pointer(depsPtr)),
		serviceAccountPtr,
		0,
	)
	if h == 0 {
		if code, ok := err.(syscall.Errno); ok && code == errorServiceMarkedForDelete {
			return fmt.Errorf("bend2will: service %q is still marked for deletion; "+
				"wait a moment and try again, or reboot", cfg.Name)
		}
		return fmt.Errorf("bend2will: CreateService(%q): %w (administrator rights are required)", cfg.Name, err)
	}
	defer procCloseServiceHandle.Call(h)

	removePartialService := func(cause error) error {
		if ret, _, deleteErr := procDeleteService.Call(h); ret == 0 {
			return fmt.Errorf("%w; remove partially configured service: %v", cause, deleteErr)
		}
		return cause
	}
	if opts.WindowsDelayedAutoStart {
		if err := setWindowsServiceDelayedAutoStart(h); err != nil {
			return removePartialService(err)
		}
	}
	if opts.WindowsServiceRestartOnFailure {
		if err := setWindowsServiceRecovery(h, settings.restartDelayMilliseconds); err != nil {
			return removePartialService(err)
		}
	}

	if opts.Now {
		return p.Start(ctx, cfg.Name)
	}
	return nil
}

// Uninstall stops and deletes the service.
func (p *windowsPlatform) Uninstall(ctx context.Context, name string, purge bool) error {
	mgr, err := p.openManager(scManagerConnect)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(mgr)

	h, err := p.openService(mgr, name, svcStop|svcQueryStatus|svcDelete)
	if err != nil {
		if isErrno(err, errorServiceDoesNotExist) {
			return nil // already gone, which is what was asked for
		}
		return err
	}
	defer procCloseServiceHandle.Call(h)

	state, err := p.queryState(h)
	if err != nil {
		return fmt.Errorf("bend2will: query service %q before uninstall: %w", name, err)
	}
	if err := requestStopAndWait(state, func() error {
		return p.control(h, serviceControlStop)
	}, func() error {
		return p.waitForState(ctx, h, serviceStopped, 30*time.Second)
	}); err != nil {
		return fmt.Errorf("bend2will: stop service %q before uninstall: %w", name, err)
	}

	if ret, _, err := procDeleteService.Call(h); ret == 0 {
		return fmt.Errorf("bend2will: DeleteService(%q): %w", name, err)
	}
	return nil
}

func (p *windowsPlatform) Start(ctx context.Context, name string) error {
	mgr, err := p.openManager(scManagerConnect)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(mgr)

	h, err := p.openService(mgr, name, svcStart|svcQueryStatus)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(h)

	if ret, _, callErr := procStartServiceW.Call(h, 0, 0); ret == 0 {
		if isErrno(callErr, errorServiceAlreadyRunning) {
			return nil
		}
		return fmt.Errorf("bend2will: start %q: %w", name, callErr)
	}
	return nil
}

func (p *windowsPlatform) Stop(ctx context.Context, name string) error {
	mgr, err := p.openManager(scManagerConnect)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(mgr)

	h, err := p.openService(mgr, name, svcStop|svcQueryStatus)
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(h)

	state, err := p.queryState(h)
	if err != nil {
		return fmt.Errorf("bend2will: query service %q before stop: %w", name, err)
	}
	if err := requestStopAndWait(state, func() error {
		return p.control(h, serviceControlStop)
	}, func() error {
		return p.waitForState(ctx, h, serviceStopped, 30*time.Second)
	}); err != nil {
		return fmt.Errorf("bend2will: stop %q: %w", name, err)
	}
	return nil
}

// Status reports a service's current state.
func (p *windowsPlatform) Status(ctx context.Context, name string) (ServiceStatus, error) {
	mgr, err := p.openManager(scManagerConnect)
	if err != nil {
		return ServiceStatus{}, err
	}
	defer procCloseServiceHandle.Call(mgr)

	h, err := p.openService(mgr, name, svcQueryStatus)
	if err != nil {
		if isErrno(err, errorServiceDoesNotExist) {
			return ServiceStatus{Name: name, State: StatusStopped, Detail: "not installed"}, nil
		}
		return ServiceStatus{}, err
	}
	defer procCloseServiceHandle.Call(h)

	state, err := p.queryState(h)
	if err != nil {
		return ServiceStatus{}, err
	}
	return ServiceStatus{Name: name, State: windowsState(state)}, nil
}

func (p *windowsPlatform) Close() error {
	p.mu.Lock()
	h := p.status
	p.status = 0
	p.mu.Unlock()
	if h != 0 {
		return syscall.CloseHandle(h)
	}
	return nil
}

// openManager opens a handle to the SCM.
func (p *windowsPlatform) openManager(access uint32) (uintptr, error) {
	mgr, _, err := procOpenSCManagerW.Call(0, 0, uintptr(access))
	if mgr == 0 {
		return 0, fmt.Errorf("bend2will: OpenSCManager(access %#x): %w", access, err)
	}
	return mgr, nil
}

// openService opens a handle to one service.
func (p *windowsPlatform) openService(mgr uintptr, name string, access uint32) (uintptr, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("bend2will: service name %q: %w", name, err)
	}
	h, _, callErr := procOpenServiceW.Call(mgr, uintptr(unsafe.Pointer(namePtr)), uintptr(access))
	if h == 0 {
		return 0, fmt.Errorf("bend2will: OpenService(%q): %w", name, callErr)
	}
	return h, nil
}

// queryState reads SERVICE_STATUS.CurrentState.
func (p *windowsPlatform) queryState(h uintptr) (uint32, error) {
	var ss serviceStatus
	if ret, _, err := procQueryServiceStatus.Call(h, uintptr(unsafe.Pointer(&ss))); ret == 0 {
		return 0, fmt.Errorf("bend2will: QueryServiceStatus: %w", err)
	}
	return ss.CurrentState, nil
}

// control sends a control code to a service.
func (p *windowsPlatform) control(h uintptr, code uint32) error {
	ss := serviceStatus{CurrentState: code}
	if ret, _, err := procControlService.Call(h, uintptr(unsafe.Pointer(&ss))); ret == 0 {
		return err
	}
	return nil
}

// waitForState polls until the service reaches want, or the deadline passes.
func (p *windowsPlatform) waitForState(ctx context.Context, h uintptr, want uint32, timeout time.Duration) error {
	return waitForServiceState(ctx, func() (uint32, error) { return p.queryState(h) }, want, timeout)
}

func waitForServiceState(ctx context.Context, query func() (uint32, error), want uint32, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := query()
		if err != nil {
			return err
		}
		if state == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("bend2will: timed out after %s waiting for service state %d", timeout, want)
		case <-ticker.C:
		}
	}
}

func requestStopAndWait(state uint32, control, wait func() error) error {
	if state == serviceStopped {
		return nil
	}
	if state != serviceStopPending {
		if err := control(); err != nil {
			if isErrno(err, errorServiceNotActive) {
				return nil
			}
			return err
		}
	}
	return wait()
}

// imagePathArgs builds the argv that becomes the service's ImagePath.
//
// The first element is the bend2will binary itself, because the SCM executes
// exactly what ImagePath holds. The rest are long-option flags re-parsed by
// obey when the service starts. \x1f separates the supervised program's own
// arguments, because that byte cannot occur in a Windows path or command-line
// argument, which makes the join and the split exactly reversible.
func (p *windowsPlatform) imagePathArgs(cfg *Config) ([]string, error) {
	self, err := selfExePath()
	if err != nil {
		return nil, fmt.Errorf("bend2will: locate own executable: %w", err)
	}
	argv := []string{
		self,
		"service",
		"--name", cfg.Name,
		"--command", cfg.Command,
	}
	if len(cfg.Args) > 0 {
		argv = append(argv, "--arguments", strings.Join(cfg.Args, "\x1f"))
	}
	return append(argv, flattenConfigFlags(cfg, ModeService)...), nil
}

// windowsState maps an SCM state onto a lifecycle Status.
func windowsState(state uint32) Status {
	switch state {
	case serviceRunning:
		return StatusRunning
	case serviceStartPending, serviceContinuePending, servicePausePending:
		return StatusStarting
	case serviceStopPending:
		return StatusStopping
	case serviceStopped, servicePaused:
		return StatusStopped
	default:
		return StatusUnknown
	}
}

// isErrno reports whether err is a syscall.Errno with the given code.

// windowsCommandLine renders argv as a single command line, quoting each element
// so that the SCM's own parsing hands back exactly what we put in.
func windowsCommandLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// scQuote renders a value for an sc.exe command line.
//
// sc.exe strips at most one surrounding pair of quotes, so inner quotes must be
// left alone. This is only correct for values that are not themselves command
// lines; binPath is emitted raw for that reason.
func scQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return `"` + s + `"`
}

// visibleSeparators renders 0x1f as a readable token, for comments and help text
// where the raw control byte would be invisible.
func visibleSeparators(s string) string {
	return strings.ReplaceAll(s, "\x1f", "<US>")
}

// flattenConfigFlags renders the parts of cfg that the service's ImagePath needs
// in order to reproduce a foreground run exactly.
func flattenConfigFlags(cfg *Config, mode Mode) []string {
	var out []string
	add := func(flag, value string) {
		if value != "" {
			out = append(out, "--"+flag, value)
		}
	}
	add("workdir", cfg.Dir)
	add("log", cfg.LogPath)
	add("level", cfg.LogLevel)
	if cfg.LogMaxBytes > 0 {
		add("log-max-bytes", fmt.Sprintf("%d", cfg.LogMaxBytes))
	}
	add("mode", mode.String())
	add("restart", boolFlag(cfg.Restart))
	add("priority", cfg.Priority)
	add("stop", cfg.StopMethod.String())
	add("stop-command", cfg.StopCommand)
	add("stop-timeout", cfg.StopTimeout.String())
	add("start-timeout", cfg.StartTimeout.String())
	add("ready-command", cfg.ReadyCommand)
	for _, kv := range cfg.Env {
		add("env", kv)
	}
	for _, d := range cfg.Dependencies {
		add("after", d)
	}
	return out
}

func boolFlag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func firstLine(s, fallback string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
