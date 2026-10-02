//go:build windows

package bend2will

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestCommandLineRoundTripsThroughWindowsParser(t *testing.T) {
	original := []string{
		"simple",
		"with space",
		`with"quote`,
		`with\backslash`,
		`trailing\`,
		`\\server\share`,
		"",
		"tab\there",
		`"`,
		`""`,
		`a\`,
		"many     spaces",
	}
	c := DefaultConfig("svc", `C:\Program Files\obey\obey.exe`)
	c.Args = original

	got := commandLineToArgvW(c.CommandLine())
	if len(got) != len(original)+1 {
		t.Fatalf("got %d elements, want %d: %q", len(got), len(original)+1, got)
	}
	if got[0] != c.Command {
		t.Errorf("command = %q, want %q", got[0], c.Command)
	}
	for i := range original {
		if got[i+1] != original[i] {
			t.Errorf("arg %d = %q, want %q (the round trip lost information)", i, got[i+1], original[i])
		}
	}
}

func TestWindowsDefinitionUsesServiceAccountAndRecovery(t *testing.T) {
	cfg := DefaultConfig("TimeKeeper", `C:\TimeKeeper\obey.exe`)
	cfg.Command = `C:\TimeKeeper\timekeeper.exe`
	opts := InstallOptions{
		Enable:                         true,
		WindowsServiceAccount:          "LocalService",
		WindowsDelayedAutoStart:        true,
		WindowsServiceRestartOnFailure: true,
		WindowsServiceRestartDelay:     5 * time.Second,
	}
	def, err := DefinitionForInstall(newWindowsPlatform(), &cfg, opts)
	if err != nil {
		t.Fatalf("definitionWithOptions: %v", err)
	}
	for _, want := range []string{
		"start= delayed-auto",
		`obj= "NT AUTHORITY\LocalService" password= ""`,
		"sc.exe failure TimeKeeper reset= 86400 actions= restart/5000/restart/5000/restart/5000",
	} {
		if !strings.Contains(def.Contents, want) {
			t.Errorf("definition missing %q:\n%s", want, def.Contents)
		}
	}
}

func TestWindowsDefinitionDefaultsToDemandAndLocalSystem(t *testing.T) {
	cfg := DefaultConfig("svc", `C:\svc.exe`)
	def, err := newWindowsPlatform().Definition(&cfg)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if !strings.Contains(def.Contents, "start= demand") {
		t.Errorf("default start type is not demand:\n%s", def.Contents)
	}
	if strings.Contains(def.Contents, " obj=") || strings.Contains(def.Contents, "sc.exe failure") {
		t.Errorf("default install unexpectedly configures an account or recovery:\n%s", def.Contents)
	}
}

func TestWindowsServiceAccountNormalization(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"LocalSystem", ""},
		{`NT AUTHORITY\LocalSystem`, ""},
		{"LocalService", `NT AUTHORITY\LocalService`},
		{`NT AUTHORITY\LocalService`, `NT AUTHORITY\LocalService`},
		{"NetworkService", `NT AUTHORITY\NetworkService`},
	}
	for _, tc := range cases {
		got, err := normalizeWindowsServiceAccount(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("normalizeWindowsServiceAccount(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	if _, err := normalizeWindowsServiceAccount("Administrator"); err == nil {
		t.Error("expected arbitrary accounts to be rejected")
	}
}

func TestWindowsRestartDelayMilliseconds(t *testing.T) {
	cases := []struct {
		delay time.Duration
		want  uint32
	}{
		{time.Nanosecond, 1},
		{5 * time.Second, 5000},
	}
	for _, tc := range cases {
		got, err := windowsRestartDelayMilliseconds(tc.delay)
		if err != nil || got != tc.want {
			t.Errorf("windowsRestartDelayMilliseconds(%s) = %d, %v; want %d", tc.delay, got, err, tc.want)
		}
	}
	tooLong := time.Duration(uint64(^uint32(0))+1) * time.Millisecond
	if _, err := windowsRestartDelayMilliseconds(tooLong); err == nil {
		t.Error("expected delay above the SCM millisecond limit to be rejected")
	}
}

func TestWindowsInstallSettingsValidateRecoveryOptions(t *testing.T) {
	if _, err := newWindowsInstallSettings(InstallOptions{WindowsServiceRestartDelay: time.Second}); err == nil {
		t.Error("expected restart delay without recovery to be rejected")
	}
	settings, err := newWindowsInstallSettings(InstallOptions{WindowsServiceRestartOnFailure: true})
	if err != nil {
		t.Fatalf("windowsInstallSettings: %v", err)
	}
	if settings.restartDelayMilliseconds != 5000 {
		t.Errorf("default restart delay = %dms, want 5000ms", settings.restartDelayMilliseconds)
	}
}

func TestWindowsConnectRegistersServiceMainCallback(t *testing.T) {
	original := startServiceCtrlDispatcherCall
	t.Cleanup(func() { startServiceCtrlDispatcherCall = original })
	serviceName := "B2W-Test"
	registeredName := ""
	var serviceProc uintptr
	startServiceCtrlDispatcherCall = func(entry *serviceTableEntry) (uintptr, error) {
		entries := unsafe.Slice(entry, 2)
		registeredName = syscall.UTF16ToString(unsafe.Slice(entries[0].ServiceName, len(serviceName)+1))
		serviceProc = entries[0].ServiceProc
		if entries[1] != (serviceTableEntry{}) {
			t.Errorf("service table terminator = %#v, want zero value", entries[1])
		}
		return 1, nil
	}
	cfg := DefaultConfig(serviceName, `C:\B2W\obey.exe`)
	platform := newWindowsPlatform()
	handled, err := platform.Connect(context.Background(), &cfg, ModeService, func(context.Context) error { return nil }, NewStopper())
	if err != nil || !handled {
		t.Fatalf("Connect = (%t, %v), want handled service", handled, err)
	}
	if registeredName != serviceName || serviceProc == 0 {
		t.Fatalf("registered name=%q service proc=%#x", registeredName, serviceProc)
	}
}

func TestWindowsServiceMainUsesServiceNameAndReportsControlsByState(t *testing.T) {
	originalRegister := registerServiceCtrlHandlerCall
	originalReport := setServiceStatusCall
	t.Cleanup(func() {
		registerServiceCtrlHandlerCall = originalRegister
		setServiceStatusCall = originalReport
	})
	serviceName := "B2W-Test"
	registeredName := ""
	statuses := []serviceStatus{}
	registerServiceCtrlHandlerCall = func(name *uint16, handler uintptr) (uintptr, error) {
		if handler == 0 {
			t.Error("service control callback is nil")
		}
		registeredName = syscall.UTF16ToString(unsafe.Slice(name, len(serviceName)+1))
		return 1, nil
	}
	setServiceStatusCall = func(handle syscall.Handle, status *serviceStatus) error {
		if handle != 1 {
			t.Errorf("status handle = %d, want 1", handle)
		}
		statuses = append(statuses, *status)
		return nil
	}
	var platform *windowsPlatform
	platform = &windowsPlatform{
		serviceName:          serviceName,
		stopper:              NewStopper(),
		finished:             make(chan struct{}),
		startWaitHintSeconds: 30,
	}
	platform.supervise = func(ctx context.Context) error { return platform.ReportStatus(StatusRunning) }
	t.Cleanup(func() { platform.status = 0 })

	if syscall.NewCallback(platform.serviceMain) == 0 || syscall.NewCallback(platform.serviceControlHandler) == 0 {
		t.Fatal("SCM callback registration returned a nil pointer")
	}
	if result := platform.serviceMain(0, 0); result != 0 {
		t.Fatalf("ServiceMain result = %d, want 0", result)
	}
	if registeredName != serviceName {
		t.Fatalf("registered service name = %q, want %q", registeredName, serviceName)
	}
	wantStates := []uint32{serviceStartPending, serviceRunning, serviceStopped}
	if len(statuses) != len(wantStates) {
		t.Fatalf("reported %d statuses, want %d: %#v", len(statuses), len(wantStates), statuses)
	}
	for i, status := range statuses {
		if status.CurrentState != wantStates[i] {
			t.Errorf("status %d state = %d, want %d", i, status.CurrentState, wantStates[i])
		}
		wantControls := uint32(0)
		if status.CurrentState == serviceRunning {
			wantControls = serviceAcceptStop | serviceAcceptShutdown
		}
		if status.ControlsAccepted != wantControls {
			t.Errorf("status %d accepted controls = %#x, want %#x", i, status.ControlsAccepted, wantControls)
		}
	}
	if statuses[0].WaitHint != 30000 {
		t.Errorf("startup wait hint = %d, want 30000ms", statuses[0].WaitHint)
	}
}

func TestWindowsServiceControlHandlerStopsSupervisor(t *testing.T) {
	for _, control := range []uintptr{serviceControlStop, serviceControlShutdown} {
		stopper := NewStopper()
		platform := &windowsPlatform{stopper: stopper}
		if got := platform.serviceControlHandler(control); got != noError {
			t.Errorf("control %d result = %d, want noError", control, got)
		}
		if !stopper.Stopped() {
			t.Errorf("control %d did not stop supervisor", control)
		}
	}
}

func TestRequestStopAndWaitWaitsForStoppedState(t *testing.T) {
	controlCalls, waitCalls := 0, 0
	err := requestStopAndWait(serviceRunning, func() error {
		controlCalls++
		return nil
	}, func() error {
		waitCalls++
		return nil
	})
	if err != nil || controlCalls != 1 || waitCalls != 1 {
		t.Fatalf("stop calls=%d wait calls=%d err=%v", controlCalls, waitCalls, err)
	}
}

func TestRequestStopAndWaitHandlesAlreadyStoppedOrPendingService(t *testing.T) {
	for _, tc := range []struct {
		state        uint32
		wantControls int
		wantWaits    int
	}{
		{state: serviceStopped},
		{state: serviceStopPending, wantWaits: 1},
	} {
		controlCalls, waitCalls := 0, 0
		err := requestStopAndWait(tc.state, func() error {
			controlCalls++
			return nil
		}, func() error {
			waitCalls++
			return nil
		})
		if err != nil || controlCalls != tc.wantControls || waitCalls != tc.wantWaits {
			t.Fatalf("state=%d controls=%d waits=%d err=%v", tc.state, controlCalls, waitCalls, err)
		}
	}
}

func TestWaitForServiceStateHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := waitForServiceState(ctx, func() (uint32, error) {
		calls++
		return serviceRunning, nil
	}, serviceStopped, time.Second)
	if err != context.Canceled || calls != 0 {
		t.Fatalf("wait error=%v query calls=%d", err, calls)
	}
}
