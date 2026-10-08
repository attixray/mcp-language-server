//go:build windows

package main

import "os"

// Windows pipe handles support cancellation by Close (covered by the native
// unread-stdout subprocess test).
func newAdapterOutput() (*os.File, error) { return os.Stdout, nil }
