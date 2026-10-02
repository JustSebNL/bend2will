//go:build windows

package bend2will

import (
	"syscall"
	"unicode/utf16"
)

// Windows constants from winsvc.h, winerror.h and winnt.h. They are declared
// here rather than pulled from golang.org/x/sys/windows so that bend2will stays
// dependency-free and can be vendored as a single file.
const (
	// Service and manager access rights.
	scManagerConnect       = 0x0001
	scManagerCreateService = 0x0002
	svcAllAccess           = 0x000F01FF
	svcStart               = 0x0010
	svcStop                = 0x0020
	svcQueryStatus         = 0x0004
	svcDelete              = 0x00010000

	// Service configuration.
	serviceWin32OwnProcess            = 0x00000010
	serviceAutoStart                  = 0x00000002
	serviceDemandStart                = 0x00000003
	serviceErrorNormal                = 0x00000001
	serviceConfigFailureActions       = 0x00000002
	serviceConfigDelayedAutoStartInfo = 0x00000003
	serviceActionRestart              = 0x00000001

	// Service states.
	serviceStopped         = 0x00000001
	serviceStartPending    = 0x00000002
	serviceStopPending     = 0x00000003
	serviceRunning         = 0x00000004
	serviceContinuePending = 0x00000005
	servicePausePending    = 0x00000006
	servicePaused          = 0x00000007

	// Accepted controls and control codes.
	serviceAcceptStop         = 0x00000001
	serviceAcceptShutdown     = 0x00000004
	serviceControlStop        = 0x00000001
	serviceControlInterrogate = 0x00000004
	serviceControlShutdown    = 0x00000005

	// Error codes.
	noError                      = 0
	errorServiceDoesNotExist     = 1060
	errorFailedServiceController = 1063
	errorServiceAlreadyRunning   = 1056
	errorServiceNotActive        = 1057
	errorServiceSpecificError    = 1066
	errorServiceMarkedForDelete  = 1072
	errorInsufficientBuffer      = 122

	// Process and console.
	ctrlBreakEvent          = 1
	processAllAccess        = 0x1F0FFF
	processTerminate        = 0x0001
	queryLimitedInformation = 0x1000
	duplicateSameAccess     = 0x00000002
	stillActive             = 259
	exitDone                = 0x103 // GetExitCodeProcess result for a live process

	// Process creation flags. The priority classes and CREATE_NO_WINDOW are not
	// exported by the syscall package, so they are declared here.
	createNewProcessGroup    = 0x00000200
	createNoWindow           = 0x08000000
	realtimePriorityClass    = 0x00000100
	highPriorityClass        = 0x00000080
	aboveNormalPriorityClass = 0x00008000
	normalPriorityClass      = 0x00000020
	belowNormalPriorityClass = 0x00004000
	idlePriorityClass        = 0x00000040

	// Job objects.
	jobObjectAllAccess     = 0x1F001FF
	jobObjectExtendedLimit = 9
	jobObjectTerminate     = 1
	limitKillOnJobClose    = 0x00002000

	// Event Log.
	eventLogApplication     = 0x00000005
	eventLogCustom          = 0x00000001
	eventLogErrorType       = 0x0001
	eventLogWarningType     = 0x0002
	eventLogInformationType = 0x0004
	sevSuccess              = 0x0
	sevError                = 0x1
	sevWarning              = 0x2
	sevInfo                 = 0x4
	sevMessageString        = 0x1
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
)

// kernel32 entry points.
var (
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procGetConsoleProcessList    = kernel32.NewProc("GetConsoleProcessList")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procTerminateProcess         = kernel32.NewProc("TerminateProcess")
	procDuplicateHandle          = kernel32.NewProc("DuplicateHandle")
	procGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")
	procGetExitCodeProcess       = kernel32.NewProc("GetExitCodeProcess")
)

// advapi32 entry points: Service Control Manager.
var (
	procOpenSCManagerW              = advapi32.NewProc("OpenSCManagerW")
	procCreateServiceW              = advapi32.NewProc("CreateServiceW")
	procChangeServiceConfig2W       = advapi32.NewProc("ChangeServiceConfig2W")
	procOpenServiceW                = advapi32.NewProc("OpenServiceW")
	procDeleteService               = advapi32.NewProc("DeleteService")
	procCloseServiceHandle          = advapi32.NewProc("CloseServiceHandle")
	procStartServiceW               = advapi32.NewProc("StartServiceW")
	procControlService              = advapi32.NewProc("ControlService")
	procQueryServiceStatus          = advapi32.NewProc("QueryServiceStatus")
	procStartServiceCtrlDispatcherW = advapi32.NewProc("StartServiceCtrlDispatcherW")
	procRegisterServiceCtrlHandlerW = advapi32.NewProc("RegisterServiceCtrlHandlerW")
	procSetServiceStatus            = advapi32.NewProc("SetServiceStatus")
)

// advapi32 entry points: Event Log.
var (
	procRegisterEventSourceW  = advapi32.NewProc("RegisterEventSourceW")
	procReportEventW          = advapi32.NewProc("ReportEventW")
	procDeregisterEventSource = advapi32.NewProc("DeregisterEventSource")
	procCloseEventLog         = advapi32.NewProc("CloseEventLog")
)

// utf16PtrFromStrings renders a slice as a NUL-terminated UTF-16 array, which
// is what every string-accepting Windows API wants.
func utf16PtrFromString(s string) (*uint16, error) {
	return syscall.UTF16PtrFromString(s)
}

// utf16PtrFromStrings renders a slice as a NUL-NUL-terminated MULTI_SZ.
//
// syscall.UTF16PtrFromString cannot be used here: it rejects embedded NULs, and
// a MULTI_SZ is made of exactly those.
func utf16PtrFromMultiSz(items []string) (*uint16, error) {
	if len(items) == 0 {
		return nil, nil
	}
	units := make([]uint16, 0, len(items)*8+1)
	for _, s := range items {
		units = append(units, utf16.Encode([]rune(s))...)
		units = append(units, 0)
	}
	units = append(units, 0)
	return &units[0], nil
}

// isErrno reports whether err is a syscall.Errno with the given code.
func isErrno(err error, code uintptr) bool {
	e, ok := err.(syscall.Errno)
	return ok && uintptr(e) == code
}
