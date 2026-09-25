//go:build windows

package watcher

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// readSharingDelete reads a file without blocking its deletion. os.ReadFile
// opens without FILE_SHARE_DELETE, and a build tool deleting the file at that
// moment would fail with a sharing violation.
func readSharingDelete(file string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: file, Err: err}
	}
	f := os.NewFile(uintptr(handle), file)
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
