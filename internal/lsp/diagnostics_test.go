package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func diagnosticTestClient(t *testing.T, handler func(*Message) []*Message) *Client {
	t.Helper()
	clientPipe, serverPipe := net.Pipe()
	c := &Client{stdin: clientPipe, stdout: bufio.NewReader(clientPipe), done: make(chan struct{}),
		handlers: make(map[string]chan *Message), openFiles: make(map[string]*OpenFileInfo),
		notificationHandlers: make(map[string]NotificationHandler), serverRequestHandlers: make(map[string]ServerRequestHandler)}
	c.RegisterNotificationHandler("textDocument/publishDiagnostics", func(raw json.RawMessage) { HandleDiagnostics(c, raw) })
	go c.handleMessages()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		reader := bufio.NewReader(serverPipe)
		for {
			msg, err := ReadMessage(reader)
			if err != nil {
				return
			}
			for _, reply := range handler(msg) {
				if err := WriteMessage(serverPipe, reply); err != nil {
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		if err := clientPipe.Close(); err != nil {
			t.Error(err)
		}
		if err := serverPipe.Close(); err != nil {
			t.Error(err)
		}
		<-serverDone
		<-c.done
	})
	return c
}

func TestDiagnosticsFreshPushAndSilentServer(t *testing.T) {
	for _, versioned := range []bool{true, false} {
		t.Run(map[bool]string{true: "versioned", false: "unversioned"}[versioned], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "file # %.cs")
			if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			uri := protocol.URIFromPath(file)
			publish := func(version int, message string) *Message {
				params := map[string]any{"uri": uri, "diagnostics": []protocol.Diagnostic{{Message: message}}}
				if versioned {
					params["version"] = version
				}
				msg, err := NewNotification("textDocument/publishDiagnostics", params)
				if err != nil {
					t.Fatal(err)
				}
				return msg
			}
			c := diagnosticTestClient(t, func(msg *Message) []*Message {
				switch msg.Method {
				case "textDocument/didOpen":
					return []*Message{publish(1, "old")}
				case "textDocument/didChange":
					if versioned {
						return []*Message{publish(1, "stale"), publish(2, "new")}
					}
					return []*Message{publish(2, "new")}
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := c.DiagnosticsForFile(ctx, file)
			if err != nil || len(got) != 1 || got[0].Message != "old" {
				t.Fatalf("first report: %v, %v", got, err)
			}
			if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err = c.DiagnosticsForFile(ctx, file)
			if err != nil || len(got) != 1 || got[0].Message != "new" {
				t.Fatalf("updated report: %v, %v", got, err)
			}
		})
	}
	t.Run("silent", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "silent.cs")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		c := diagnosticTestClient(t, func(*Message) []*Message { return nil })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, err := c.DiagnosticsForFile(ctx, file); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("missing diagnostics must not mean clean: %v", err)
		}
	})
}

func TestDiagnosticsPullReports(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		count     int
		wantError bool
	}{
		{"items", `{"kind":"full","items":[{"message":"pull result"}]}`, 1, false},
		{"clean", `{"kind":"full","items":[]}`, 0, false},
		{"unchanged without previous", `{"kind":"unchanged","resultId":"unrequested"}`, 0, true},
		{"missing items", `{"kind":"full"}`, 0, true},
		{"null", `null`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "pull.cs")
			if err := os.WriteFile(file, nil, 0600); err != nil {
				t.Fatal(err)
			}
			c := diagnosticTestClient(t, func(msg *Message) []*Message {
				if msg.Method == "textDocument/diagnostic" {
					return []*Message{{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(tc.raw)}}
				}
				return nil
			})
			c.pullDiagnostics.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := c.DiagnosticsForFile(ctx, file)
			if (err != nil) != tc.wantError || (!tc.wantError && len(got) != tc.count) {
				t.Fatalf("report: %v, error: %v", got, err)
			}
		})
	}
}

func TestDiagnosticsPullErrorIsNotClean(t *testing.T) {
	file := filepath.Join(t.TempDir(), "error.cs")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c := diagnosticTestClient(t, func(msg *Message) []*Message {
		if msg.Method == "textDocument/diagnostic" {
			return []*Message{{JSONRPC: "2.0", ID: msg.ID, Error: &ResponseError{Code: -32603, Message: "analysis failed"}}}
		}
		return nil
	})
	c.pullDiagnostics.Store(true)
	if _, err := c.DiagnosticsForFile(context.Background(), file); err == nil || !strings.Contains(err.Error(), "analysis failed") {
		t.Fatalf("expected server error: %v", err)
	}
}

func TestDiagnosticServerCancellationRetryFlag(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "no retry", true: "retry"}[retry], func(t *testing.T) {
			c := diagnosticTestClient(t, func(msg *Message) []*Message {
				if msg.ID.String() == "1" {
					data, _ := json.Marshal(map[string]bool{"retriggerRequest": retry})
					return []*Message{{JSONRPC: "2.0", ID: msg.ID, Error: &ResponseError{Code: -32802, Message: "server cancelled", Data: data}}}
				}
				return []*Message{{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{"kind":"full","items":[]}`)}}
			})
			err := c.Call(context.Background(), "textDocument/diagnostic", nil, nil)
			if retry && (err != nil || c.nextID.Load() != 2) {
				t.Fatalf("requested retry did not succeed: %v", err)
			}
			if !retry && (err == nil || c.nextID.Load() != 1) {
				t.Fatalf("retried without permission: %v", err)
			}
		})
	}
}
