//go:build !windows

package main

import (
	"os"
	"time"
)

// containChildren is a no-op outside Windows; stdin EOF, signals and parent
// exit run the bounded shutdown of the language server.
func containChildren() error { return nil }

// watchParent reports when the process that started the bridge exits. Orphans
// are reparented, so a changed parent PID means the original parent is gone.
func watchParent() <-chan struct{} {
	exited := make(chan struct{})
	parent := os.Getppid()
	if parent == 1 {
		return exited
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if os.Getppid() != parent {
				close(exited)
				return
			}
		}
	}()
	return exited
}
