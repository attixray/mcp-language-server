//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// containChildren places the bridge in a job object that terminates every
// process still in it when the bridge exits, however it exits. The language
// server and everything it starts (Roslyn build hosts, MSBuild nodes) inherit
// the job, so none of them can outlive the bridge, even when the MCP client
// kills the bridge without letting it shut the language server down.
func containChildren() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	// The handle stays open on purpose: the system closes it when the bridge
	// exits, and that is what terminates the rest of the job.
	return nil
}

// watchParent reports when the process that started the bridge exits. Windows
// does not reparent orphans, and the client's end of stdin can stay open in
// other processes that inherited it, so wait on the parent's process handle.
func watchParent() <-chan struct{} {
	exited := make(chan struct{})
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getppid()))
	if err != nil {
		// Already gone or not accessible: stdin EOF remains the signal.
		return exited
	}
	// A reused PID belongs to a process started after the bridge.
	if !startedBefore(parent, windows.CurrentProcess()) {
		_ = windows.CloseHandle(parent)
		return exited
	}
	go func() {
		defer func() { _ = windows.CloseHandle(parent) }()
		if event, err := windows.WaitForSingleObject(parent, windows.INFINITE); err == nil && event == windows.WAIT_OBJECT_0 {
			close(exited)
		}
	}()
	return exited
}

func startedBefore(a, b windows.Handle) bool {
	var aCreated, bCreated, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(a, &aCreated, &exit, &kernel, &user) != nil ||
		windows.GetProcessTimes(b, &bCreated, &exit, &kernel, &user) != nil {
		return false
	}
	return aCreated.Nanoseconds() <= bCreated.Nanoseconds()
}
