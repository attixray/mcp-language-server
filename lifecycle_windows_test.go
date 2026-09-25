//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"golang.org/x/sys/windows"
)

// Language servers and their descendants must not outlive the bridge, even
// when it is killed and gets no chance to shut them down.
func TestKilledBridgeTerminatesLanguageServerTree(t *testing.T) {
	workspace := t.TempDir()
	bridge := lifecycleHelper(t, "bridge", workspace)
	stdin, err := bridge.StdinPipe() // held open: EOF must not be what stops the child
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	child, grandchild := waitPID(t, workspace, "lsp.pid"), waitPID(t, workspace, "grandchild.pid")
	if err := bridge.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = bridge.Wait()
	waitExit(t, "language server", child)
	waitExit(t, "language server's child", grandchild)
}

// When the client exits but a copy of its end of stdin stays open elsewhere,
// the bridge still shuts down with its parent.
func TestBridgeExitsWithParentWhileStdinStaysOpen(t *testing.T) {
	workspace := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	parent := lifecycleHelper(t, "parent", workspace)
	parent.Stdin = reader
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	bridge := waitPID(t, workspace, "bridge.pid")
	child, grandchild := waitPID(t, workspace, "lsp.pid"), waitPID(t, workspace, "grandchild.pid")
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	waitExit(t, "bridge", bridge)
	waitExit(t, "language server", child)
	waitExit(t, "language server's child", grandchild)
}

func lifecycleHelper(t *testing.T, role, workspace string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleHelper$")
	cmd.Env = append(os.Environ(), "LIFECYCLE_HELPER="+role, "LIFECYCLE_WORKSPACE="+workspace)
	return cmd
}

func TestLifecycleHelper(t *testing.T) {
	workspace := os.Getenv("LIFECYCLE_WORKSPACE")
	switch os.Getenv("LIFECYCLE_HELPER") {
	case "parent":
		// Starts the bridge on the parent's own stdin, then waits to be killed.
		bridge := lifecycleHelper(t, "bridge", workspace)
		bridge.Stdin = os.Stdin
		if err := bridge.Start(); err != nil {
			os.Exit(2)
		}
		writePID(workspace, "bridge.pid", bridge.Process.Pid)
		sleepForever()
	case "bridge":
		if err := containChildren(); err != nil {
			os.Exit(3)
		}
		s, err := newServer(&config{workspaceDir: workspace, lspCommand: os.Args[0], lspArgs: []string{"-test.run=^TestLifecycleHelper$"}})
		if err != nil {
			os.Exit(4)
		}
		_ = os.Setenv("LIFECYCLE_HELPER", "lsp")
		_ = runServer(s, make(chan os.Signal))
		os.Exit(0)
	case "lsp":
		grandchild := lifecycleHelper(t, "sleep", workspace)
		if err := grandchild.Start(); err != nil {
			os.Exit(5)
		}
		writePID(workspace, "grandchild.pid", grandchild.Process.Pid)
		writePID(workspace, "lsp.pid", os.Getpid())
		serveLifecycleLSP()
	case "sleep":
		sleepForever()
	}
}

// serveLifecycleLSP answers requests but never exits on shutdown, like a
// language server that is slow to stop.
func serveLifecycleLSP() {
	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := lsp.ReadMessage(r)
		if err != nil {
			sleepForever()
		}
		if msg.ID == nil || msg.Method == "shutdown" {
			continue
		}
		result := json.RawMessage(`null`)
		if msg.Method == "initialize" {
			result = json.RawMessage(`{"capabilities":{}}`)
		}
		if err := lsp.WriteMessage(os.Stdout, &lsp.Message{JSONRPC: "2.0", ID: msg.ID, Result: result}); err != nil {
			sleepForever()
		}
	}
}

func sleepForever() {
	for {
		time.Sleep(time.Second)
	}
}

func writePID(dir, name string, pid int) {
	temp := filepath.Join(dir, name+".tmp")
	if os.WriteFile(temp, []byte(strconv.Itoa(pid)), 0o600) != nil || os.Rename(temp, filepath.Join(dir, name)) != nil {
		os.Exit(6)
	}
}

func waitPID(t *testing.T, dir, name string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %v", name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitExit(t *testing.T, what string, pid int) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	event, err := windows.WaitForSingleObject(handle, 15000)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(handle, 1)
		t.Fatalf("%s (pid %d) outlived the bridge", what, pid)
	}
}
