package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertAdapterError(t *testing.T, response map[string]any, id any, unknown bool, reason string) {
	t.Helper()
	if response["jsonrpc"] != "2.0" || response["id"] != id {
		t.Fatalf("wrong error identity: %v", response)
	}
	failure, ok := response["error"].(map[string]any)
	if !ok || failure["code"] != float64(brokerUnavailableCode) {
		t.Fatalf("missing broker error: %v", response)
	}
	data, ok := failure["data"].(map[string]any)
	if !ok || data["outcome_unknown"] != unknown || data["request_replayed"] != false || data["reason"] != reason {
		t.Fatalf("wrong failure semantics: %v", response)
	}
	if !strings.Contains(failure["message"].(string), "Restart the MCP adapter") {
		t.Fatalf("missing recovery instructions: %v", response)
	}
	if unknown && !strings.Contains(failure["message"].(string), "verify file state") {
		t.Fatalf("missing mutation warning: %v", response)
	}
}
func waitFailedAdapter(t *testing.T, client *adapterTestClient) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- client.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("broker loss produced successful adapter exit")
		}
	case <-time.After(5 * time.Second):
		_ = client.cmd.Process.Kill()
		<-done
		t.Fatal("adapter did not exit after broker loss")
	}
	if !strings.Contains(client.errors.String(), "shared LSP broker unavailable") || !strings.Contains(client.errors.String(), "verify file state") || !strings.Contains(client.errors.String(), "Broker log:") {
		t.Fatalf("missing unconditional stderr diagnostics: %s", client.errors.String())
	}
	_ = client.input.Close()
	if raw, err := client.reader.ReadBytes('\n'); len(raw) != 0 || err == nil {
		t.Fatalf("duplicate or unsolicited MCP response after failure: %s %v", raw, err)
	}
}
func TestBrokerKillPropagatesErrorsToActiveAndIdleAdapters(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Setenv("LOG_LEVEL", "FATAL") // stderr diagnosis must survive log filtering.
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	clients := make([]*adapterTestClient, 3)
	for i := range clients {
		client, err := startTestAdapter(binary, root, state, "--request-timeout", "10s")
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = client
		t.Cleanup(func() {
			if client.cmd.ProcessState == nil {
				_ = client.cmd.Process.Kill()
				_ = client.cmd.Wait()
			}
			_ = client.input.Close()
		})
		if err = client.initialize(); err != nil {
			t.Fatal(err)
		}
	}
	document := filepath.Join(root, "main.go")
	if err := os.WriteFile(document, []byte("initial-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stall"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		response map[string]any
		err      error
	}
	hover := make(chan outcome, 1)
	go func() {
		response, err := clients[0].exchange(2, "tools/call", map[string]any{"name": "hover", "arguments": map[string]any{"filePath": document, "line": 1, "column": 1}})
		hover <- outcome{response, err}
	}()
	await(t, time.Second, func() bool { _, err := os.Stat(filepath.Join(root, "request-seen")); return err == nil }, "hover did not reach LSP")
	const mutationID = "rename-opaque-2"
	wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": mutationID, "method": "tools/call", "params": map[string]any{"name": "rename_symbol", "arguments": map[string]any{"filePath": document, "line": 1, "column": 1, "newName": "other"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clients[1].input.Write(append(wire, '\n')); err != nil {
		t.Fatal(err)
	}
	await(t, time.Second, func() bool {
		response, err := clients[2].exchange(3, "tools/call", map[string]any{"name": "lsp_status", "arguments": map[string]any{}})
		if err != nil {
			return false
		}
		result := response["result"].(map[string]any)
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var status struct {
			Queued int `json:"queued_tools"`
		}
		return json.Unmarshal([]byte(text), &status) == nil && status.Queued == 1
	}, "mutation never entered broker queue")
	descriptors, _ := filepath.Glob(filepath.Join(state, "*.json"))
	if len(descriptors) != 1 {
		t.Fatalf("expected one broker descriptor: %v", descriptors)
	}
	data, err := os.ReadFile(descriptors[0])
	if err != nil {
		t.Fatal(err)
	}
	var endpoint brokerEndpoint
	if err = json.Unmarshal(data, &endpoint); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(endpoint.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-hover:
		if result.err != nil {
			t.Fatal(result.err)
		}
		assertAdapterError(t, result.response, float64(2), true, "connection_lost")
	case <-time.After(5 * time.Second):
		t.Fatal("active request got no broker-loss error")
	}
	line, err := clients[1].reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err = json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	assertAdapterError(t, response, mutationID, true, "connection_lost")
	// The idle adapter cannot emit a response without a request ID, but still
	// reports the same stderr diagnosis and nonzero exit.
	for _, client := range clients {
		waitFailedAdapter(t, client)
	}
	if pids := startedPIDs(root); len(pids) != 1 {
		t.Fatalf("broker loss replayed work or restarted implicitly: %v", pids)
	}
	if _, err = os.Stat(filepath.Join(root, "renames")); !os.IsNotExist(err) {
		t.Fatal("queued mutation executed after broker death")
	}
}

func TestAdapterStartupFailureRepliesToInitialize(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Setenv("LOG_LEVEL", "FATAL")
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	if err := os.WriteFile(filepath.Join(root, "stall-init"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := startTestAdapter(binary, root, state, "--init-timeout", "100ms", "--lock-timeout", "100ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if client.cmd.ProcessState == nil {
			_ = client.cmd.Process.Kill()
			_ = client.cmd.Wait()
		}
		_ = client.input.Close()
	})
	response, err := client.exchange(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	assertAdapterError(t, response, float64(1), false, "startup_failed")
	waitFailedAdapter(t, client)
}

func TestAdapterRejectsTruncatedBrokerFrameWithoutDuplicatingCompletedRequests(t *testing.T) {
	dir, key := t.TempDir(), "framing"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	token := strings.Repeat("a", 64)
	data, _ := json.Marshal(brokerEndpoint{Protocol: brokerProtocol, Key: key, PID: 12345, Address: listener.Addr().String(), Token: token})
	if err = os.WriteFile(filepath.Join(dir, key+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		if _, err = reader.ReadString('\n'); err != nil {
			serverDone <- err
			return
		}
		if _, err = fmt.Fprint(conn, "OK\n"); err != nil {
			serverDone <- err
			return
		}
		if _, err = reader.ReadBytes('\n'); err != nil {
			serverDone <- err
			return
		}
		// Normalize 1.0 to 1 as the real MCP server does.
		if _, err = fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"result":{}}`); err != nil {
			serverDone <- err
			return
		}
		for i := 0; i < 2; i++ {
			if _, err = reader.ReadBytes('\n'); err != nil {
				serverDone <- err
				return
			}
		}
		_, err = fmt.Fprint(conn, `{"jsonrpc":"2.0","id":2,"result":`)
		serverDone <- err
	}()
	input, hostInput := io.Pipe()
	hostOutput, output := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = hostInput.Close(); _ = hostOutput.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- runAdapter(ctx, &config{lockTimeout: time.Second, initTimeout: time.Second}, dir, key, input, output)
	}()
	reader := bufio.NewReader(hostOutput)
	if _, err = fmt.Fprintln(hostInput, `{"jsonrpc":"2.0","id":1.0,"method":"ping"}`); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"result"`) {
		t.Fatalf("completed response lost: %s %v", line, err)
	}
	for _, request := range []string{`{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":"opaque","method":"ping"}`} {
		if _, err = fmt.Fprintln(hostInput, request); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[any]bool{float64(2): false, "opaque": false}
	for i := 0; i < 2; i++ {
		line, err = reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err = json.Unmarshal(line, &response); err != nil {
			t.Fatalf("partial broker frame corrupted MCP output: %s %v", line, err)
		}
		id := response["id"]
		seen, exists := ids[id]
		if !exists || seen {
			t.Fatalf("duplicate/completed/wrong request ID: %v", response)
		}
		ids[id] = true
		assertAdapterError(t, response, id, true, "connection_lost")
	}
	if err = <-done; err == nil || !strings.Contains(err.Error(), "truncated JSON") {
		t.Fatalf("missing truncated-frame diagnosis: %v", err)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	if raw, err := reader.ReadBytes('\n'); len(raw) != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected trailing output: %s %v", raw, err)
	}
}

func TestAdapterFailureFlushIsBoundedWhenClientDoesNotRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	requests := &adapterRequests{pending: make(map[string]*adapterRequest)}
	for i := 0; i < 16; i++ {
		key, _, _ := requests.receive([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, i)))
		requests.sent(key)
	}
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- failAdapter(requests, writer, "connection_lost", io.EOF, "broker.log") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cannot deliver MCP error") {
			t.Fatalf("missing output failure: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("failure reporting blocked indefinitely on stdout")
	}
	if time.Since(started) > adapterFailureGrace+time.Second {
		t.Fatal("failure grace applied per request rather than per flush")
	}
}

func TestBrokerLossExitIsBoundedWithUnreadStdoutPipe(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Setenv("LOG_LEVEL", "FATAL")
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	client, err := startTestAdapter(binary, root, state, "--request-timeout", "10s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if client.cmd.ProcessState == nil {
			_ = client.cmd.Process.Kill()
			_ = client.cmd.Wait()
		}
		_ = client.input.Close()
	})
	if err = client.initialize(); err != nil {
		t.Fatal(err)
	}
	document := filepath.Join(root, "main.go")
	if err = os.WriteFile(document, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "stall"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	// The error response echoes this valid string ID and fills the unread OS
	// stdout pipe. Close must interrupt that native write, not just io.Pipe.
	wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": strings.Repeat("x", 1024*1024), "method": "tools/call", "params": map[string]any{"name": "hover", "arguments": map[string]any{"filePath": document, "line": 1, "column": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.input.Write(append(wire, '\n')); err != nil {
		t.Fatal(err)
	}
	await(t, 2*time.Second, func() bool { _, err := os.Stat(filepath.Join(root, "request-seen")); return err == nil }, "hover did not reach LSP")
	descriptors, _ := filepath.Glob(filepath.Join(state, "*.json"))
	if len(descriptors) != 1 {
		t.Fatal("missing broker descriptor")
	}
	data, err := os.ReadFile(descriptors[0])
	if err != nil {
		t.Fatal(err)
	}
	var endpoint brokerEndpoint
	if err = json.Unmarshal(data, &endpoint); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(endpoint.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Kill(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.cmd.Wait() }()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("unread error produced successful exit")
		}
	case <-time.After(5 * time.Second):
		_ = client.cmd.Process.Kill()
		<-done
		t.Fatal("native stdout pipe blocked adapter failure reporting")
	}
	if !strings.Contains(client.errors.String(), "cannot deliver MCP error") {
		t.Fatalf("missing bounded-flush diagnosis: %s", client.errors.String())
	}
}

type adapterBuffer struct{ bytes.Buffer }

func (*adapterBuffer) Close() error { return nil }

func TestAdapterLargeNumericIDsStayDistinctDuringFailure(t *testing.T) {
	requests := &adapterRequests{pending: make(map[string]*adapterRequest)}
	ids := []string{"9007199254740992", "9007199254740993"}
	keys := map[string]bool{}
	for _, id := range ids {
		key, rejection, accepted := requests.receive([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"ping"}`))
		if !accepted || rejection != "" || keys[key] {
			t.Fatalf("distinct integer ID collided: %s %s", id, rejection)
		}
		keys[key] = true
		requests.sent(key)
	}
	if adapterID(json.RawMessage(`1.0`)) != adapterID(json.RawMessage(`1`)) {
		t.Fatal("equivalent numeric IDs do not correlate")
	}
	var output adapterBuffer
	if err := failAdapter(requests, &output, "connection_lost", io.EOF, "broker.log"); err == nil {
		t.Fatal("failure missing")
	}
	returned := map[string]bool{}
	scanner := bufio.NewScanner(&output)
	for scanner.Scan() {
		var response struct {
			ID    json.RawMessage `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error.Code != brokerUnavailableCode || returned[string(response.ID)] {
			t.Fatalf("wrong or duplicate failure ID: %s", response.ID)
		}
		returned[string(response.ID)] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !returned[id] {
			t.Fatalf("failure lost numeric ID %s", id)
		}
	}
}
func TestSharedBrokerPreservesLargeNumericResponseIDs(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	client, err := startTestAdapter(binary, root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close(t)
	if err = client.initialize(); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"ping", "unknown/test-method"} {
		ids := map[string]bool{"9007199254740992": false, "9007199254740993": false}
		for id := range ids {
			if _, err = fmt.Fprintln(client.input, `{"jsonrpc":"2.0","id":`+id+`,"method":"`+method+`"}`); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < len(ids); i++ {
			line, err := client.reader.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err = json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			key := string(response.ID)
			seen, exists := ids[key]
			if !exists || seen {
				t.Fatalf("broker rounded or duplicated response ID: %s", line)
			}
			ids[key] = true
			if method == "ping" && response.Result == nil {
				t.Fatalf("ping ID falsely rejected: %s", line)
			}
			if method != "ping" && (response.Error == nil || response.Error.Code != -32601) {
				t.Fatalf("method error lost ID: %s", line)
			}
		}
	}
}
