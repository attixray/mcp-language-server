package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
)

func TestStdioEOFShutsDownChild(t *testing.T) {
	for _, mode := range []string{"normal", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			workspace := t.TempDir()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBridgeEOFHelper$")
			cmd.Env = append(os.Environ(), "BRIDGE_EOF_HELPER="+mode, "BRIDGE_EOF_WORKSPACE="+workspace)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bridge did not exit cleanly on EOF: %v\n%s", err, output)
			}
			if mode == "normal" {
				if _, err := os.Stat(filepath.Join(workspace, "child-exited")); err != nil {
					t.Fatalf("child did not shut down: %v", err)
				}
			}
		})
	}
}

func TestBridgeEOFHelper(t *testing.T) {
	if os.Getenv("BRIDGE_EOF_HELPER") == "" {
		return
	}
	s, err := newServer(&config{workspaceDir: os.Getenv("BRIDGE_EOF_WORKSPACE"), lspCommand: os.Args[0], lspArgs: []string{"-test.run=^TestBridgeLSPHelper$"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runServer(s, make(chan os.Signal)); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeLSPHelper(t *testing.T) {
	mode := os.Getenv("BRIDGE_EOF_HELPER")
	if mode == "" {
		return
	}
	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := lsp.ReadMessage(r)
		if err != nil || msg.Method == "exit" {
			if err := os.WriteFile(filepath.Join(os.Getenv("BRIDGE_EOF_WORKSPACE"), "child-exited"), []byte("ok"), 0600); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
		if msg.Method == "shutdown" && mode == "stalled" {
			for {
				time.Sleep(time.Second)
			}
		}
		if msg.ID != nil {
			result := json.RawMessage(`null`)
			if msg.Method == "initialize" {
				result = json.RawMessage(`{"capabilities":{}}`)
			}
			if err := lsp.WriteMessage(os.Stdout, &lsp.Message{JSONRPC: "2.0", ID: msg.ID, Result: result}); err != nil {
				os.Exit(3)
			}
		}
	}
}
