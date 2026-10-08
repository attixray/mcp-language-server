//go:build !windows

package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Inherited Unix stdout is a blocking descriptor: closing it does not interrupt
// a syscall already writing to a full pipe. Give the adapter a nonblocking,
// pollable duplicate so Go's runtime can interrupt that write on Close.
func newAdapterOutput() (*os.File, error) {
	fd, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate MCP stdout: %w", err)
	}
	unix.CloseOnExec(fd)
	if err = unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("make MCP stdout interruptible: %w", err)
	}
	output := os.NewFile(uintptr(fd), "MCP stdout")
	// NewFile discovers O_NONBLOCK and registers the pipe with the runtime
	// poller. Fail explicitly if stdout cannot provide cancellable pipe I/O.
	if err = output.SetWriteDeadline(time.Time{}); err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("MCP stdout must support interruptible pipe writes: %w", err)
	}
	return output, nil
}
