//go:build !windows

package watcher

import "os"

// readSharingDelete reads a file; POSIX opens never block deletion.
func readSharingDelete(file string) ([]byte, error) {
	return os.ReadFile(file)
}
