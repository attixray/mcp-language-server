package bari

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	rstrtmgr            = windows.NewLazySystemDLL("rstrtmgr.dll")
	rmStartSession      = rstrtmgr.NewProc("RmStartSession")
	rmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	rmGetList           = rstrtmgr.NewProc("RmGetList")
	rmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

// rmProcessInfo mirrors RM_PROCESS_INFO.
type rmProcessInfo struct {
	pid              uint32
	startTime        windows.Filetime
	appName          [256]uint16
	serviceShortName [64]uint16
	appType          uint32
	appStatus        uint32
	tsSessionID      uint32
	restartable      int32
}

// lockHolders asks the Restart Manager which processes have file open and
// describes each by its image and command line. Locks during design-time
// builds last milliseconds, so this is called in-process right after a
// sharing violation instead of through pwsh.
func lockHolders(file string) []string {
	var session uint32
	var key [33]uint16
	if r, _, _ := rmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return []string{fmt.Sprintf("RmStartSession: %d", r)}
	}
	defer func() { _, _, _ = rmEndSession.Call(uintptr(session)) }()
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return nil
	}
	if r, _, _ := rmRegisterResources.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&name)), 0, 0, 0, 0); r != 0 {
		return []string{fmt.Sprintf("RmRegisterResources: %d", r)}
	}
	var infos [16]rmProcessInfo
	var needed, count, reasons uint32
	count = uint32(len(infos))
	if r, _, _ := rmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons))); r != 0 {
		return []string{fmt.Sprintf("RmGetList: %d", r)}
	}
	var holders []string
	for _, info := range infos[:count] {
		holders = append(holders, describeProcess(info.pid, windows.UTF16ToString(info.appName[:])))
	}
	return holders
}

func describeProcess(pid uint32, appName string) string {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Sprintf("%s (pid %d, exited)", appName, pid)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	image := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(image))
	if windows.QueryFullProcessImageName(process, 0, &image[0], &size) == nil {
		appName = filepath.Base(windows.UTF16ToString(image[:size]))
	}
	// ProcessCommandLineInformation returns a UNICODE_STRING followed by its buffer.
	buffer := make([]uint64, 4096)
	var length uint32
	if windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buffer[0]), uint32(len(buffer)*8), &length) != nil {
		return appName
	}
	return appName + ": " + (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0])).String()
}
