package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestSharedBrokerActiveCancellationKeepsLSP(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	document := filepath.Join(root, "main.go")
	if err := os.WriteFile(document, []byte("shared-content"), 0600); err != nil {
		t.Fatal(err)
	}
	survivor, err := startTestAdapter(binary, root, state, "--request-timeout", "10s")
	if err != nil {
		t.Fatal(err)
	}
	defer survivor.close(t)
	if err = survivor.initialize(); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"name": "hover", "arguments": map[string]any{"filePath": document, "line": 1, "column": 1}}
	// More cancellations than the restart budget, including both explicit MCP
	// cancellation and EOF during an active request. A surviving session checks
	// the same LSP after each cancellation.
	for _, disconnect := range []bool{false, true, false, true, false, true} {
		victim, err := startTestAdapter(binary, root, state, "--request-timeout", "10s")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if victim.cmd.ProcessState == nil {
				victim.close(t)
			}
		})
		if err = victim.initialize(); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(root, "request-seen"))
		_ = os.Remove(filepath.Join(root, "cancel-seen"))
		if err = os.WriteFile(filepath.Join(root, "stall"), []byte("yes"), 0600); err != nil {
			t.Fatal(err)
		}
		pending := make(chan error, 1)
		go func() {
			response, err := victim.exchange(2, "tools/call", params)
			if !disconnect && err == nil && !strings.Contains(responseText(response), `isError":true`) {
				err = fmt.Errorf("canceled request succeeded: %v", response)
			}
			pending <- err
		}()
		await(t, time.Second, func() bool { _, err := os.Stat(filepath.Join(root, "request-seen")); return err == nil }, "hover did not reach LSP")
		if disconnect {
			victim.close(t)
		} else {
			_, err = victim.input.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":2}}\n"))
			if err != nil {
				t.Fatal(err)
			}
		}
		select {
		case err = <-pending:
			if !disconnect && err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled tool did not stop")
		}
		await(t, time.Second, func() bool { _, err := os.Stat(filepath.Join(root, "cancel-seen")); return err == nil }, "LSP cancellation notification missing")
		if !disconnect {
			victim.close(t)
		}
		_ = os.Remove(filepath.Join(root, "stall"))
		response, err := survivor.exchange(2, "tools/call", params)
		if err != nil || !strings.Contains(responseText(response), "shared-content") {
			t.Fatalf("surviving session lost service: %v %v", response, err)
		}
		if pids := startedPIDs(root); len(pids) != 1 {
			t.Fatalf("cancellation restarted shared LSP: %v", pids)
		}
	}
}

func TestSupervisorJoinsServerApplyEditBeforeRestart(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprint("stuck=", stuck), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("BROKER_FAKE_ROOT", root)
			s, err := newServer(&config{workspaceDir: root, lspCommand: os.Args[0], lspArgs: []string{"-test.run=^TestBrokerFakeLSP$"}, restartLimit: 3, brokerDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer s.cleanup()
			if err = s.initializeLSP(); err != nil {
				t.Fatal(err)
			}
			old := s.currentClient()
			begun, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			old.RegisterServerRequestHandler("workspace/applyEdit", func(params json.RawMessage) (any, error) {
				close(begun)
				<-release
				defer close(finished)
				return lsp.HandleApplyEdit(params)
			})
			target := filepath.Join(root, "late.go")
			if err = os.WriteFile(target, []byte("old-content"), 0600); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"edit": map[string]any{"changes": map[string]any{string(protocol.URIFromPath(target)): []any{map[string]any{"range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 11}}, "newText": "late-content"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "trigger-apply-edit"), payload, 0600); err != nil {
				t.Fatal(err)
			}
			pending := make(chan error, 1)
			go func() { pending <- old.Call(context.Background(), "test/triggerApplyEdit", nil, nil) }()
			select {
			case <-begun:
			case <-time.After(time.Second):
				t.Fatal("applyEdit handler never started")
			}
			old.Abort()
			select {
			case <-pending:
			case <-time.After(time.Second):
				t.Fatal("abort did not release pending request")
			}
			if !stuck {
				time.AfterFunc(100*time.Millisecond, unblock)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			err = s.ensureLSP(ctx)
			if stuck {
				if err == nil || !s.supervisor.poisoned.Load() {
					t.Fatalf("stuck handler was not quarantined: %v", err)
				}
				if s.currentClient() != old || len(startedPIDs(root)) != 1 {
					t.Fatal("new generation started while old applyEdit could still mutate")
				}
				unblock()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("old handler did not finish")
				}
				// Even after the callback returns, quarantine remains explicit. Neither
				// watchdog recovery nor a new tool may bypass it.
				if err = s.ensureLSP(ctx); err == nil {
					t.Fatal("quarantined broker restarted")
				}
				called := false
				handler := s.supervisedTool(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					called = true
					return nil, nil
				})
				result, err := handler(ctx, mcp.CallToolRequest{})
				if err != nil || result == nil || !result.IsError || called {
					t.Fatalf("tool bypassed quarantine: %v %v", result, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if s.currentClient() == old {
					t.Fatal("completed handler prevented recovery")
				}
				select {
				case <-finished:
				default:
					t.Fatal("replacement published before handler finished")
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "late-content" {
					t.Fatalf("applyEdit did not finish before replacement: %q %v", data, err)
				}
			}
		})
	}
}

func TestBrokerFollowerRequiresAuthenticatedLiveOwner(t *testing.T) {
	for _, ack := range []string{"unreachable", "NO\n", "OK\n"} {
		t.Run(strings.TrimSpace(ack), func(t *testing.T) {
			dir, key := t.TempDir(), "owner"
			lock, err := tryOwnerLock(filepath.Join(dir, key+".lock"))
			if err != nil || lock == nil {
				t.Fatalf("lock: %v", err)
			}
			defer func() { _ = lock.Close() }()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			token := strings.Repeat("a", 64)
			if ack == "unreachable" {
				_ = listener.Close()
			} else {
				defer func() { _ = listener.Close() }()
				go func() {
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						_ = conn.SetDeadline(time.Now().Add(time.Second))
						line, err := bufio.NewReader(conn).ReadString('\n')
						if err == nil && line == token+"\n" {
							_, _ = fmt.Fprint(conn, ack)
						}
						_ = conn.Close()
					}
				}()
			}
			stale := brokerEndpoint{Protocol: brokerProtocol, Key: key, PID: 12345, Address: address, Token: token}
			data, err := json.Marshal(stale)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, key+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			err = runBroker(&config{lockTimeout: 100 * time.Millisecond}, dir, key)
			if ack == "OK\n" {
				if err != nil {
					t.Fatalf("live owner was not recognized: %v", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stale/unauthenticated owner ended lock wait: %v", err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("owner probing ignored lock budget")
			}
		})
	}
}

func TestBrokerRetriesExitedStartupChild(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	if err := os.WriteFile(filepath.Join(root, "fail-init-once"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := startTestAdapter(binary, root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close(t)
	if err = client.initialize(); err != nil {
		t.Fatalf("adapter did not retry exited broker child: %v", err)
	}
	if pids := startedPIDs(root); len(pids) != 2 {
		t.Fatalf("expected one failed and one successful startup: %v", pids)
	}
}
