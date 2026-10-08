package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/mark3labs/mcp-go/mcp"
)

// The test binary doubles as a real LSP subprocess. All production adapters
// and brokers are separately built executables, using actual OS locks/sockets.
func TestBrokerFakeLSP(t *testing.T) {
	root := os.Getenv("BROKER_FAKE_ROOT")
	if root == "" {
		return
	}
	file, err := os.OpenFile(filepath.Join(root, "starts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(file, os.Getpid())
	_ = file.Close()
	if _, err = os.Stat(filepath.Join(root, "stall-init")); err == nil {
		for {
			time.Sleep(time.Second)
		}
	}
	documents := map[string]string{}
	reader := bufio.NewReader(os.Stdin)
	for {
		msg, err := lsp.ReadMessage(reader)
		if err != nil || msg.Method == "exit" {
			os.Exit(0)
		}
		if msg.Method == "textDocument/didOpen" || msg.Method == "textDocument/didChange" {
			var params struct {
				TextDocument struct {
					URI  string `json:"uri"`
					Text string `json:"text"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			text := params.TextDocument.Text
			if len(params.ContentChanges) > 0 {
				text = params.ContentChanges[0].Text
			}
			documents[params.TextDocument.URI] = text
		}
		if msg.ID == nil {
			continue
		}
		result := json.RawMessage(`null`)
		switch msg.Method {
		case "initialize":
			result = json.RawMessage(`{"capabilities":{}}`)
		case "textDocument/rename":
			count, err := os.OpenFile(filepath.Join(root, "renames"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				_, _ = fmt.Fprintln(count, "rename")
				_ = count.Close()
			}
			if _, err = os.Stat(filepath.Join(root, "stall-rename")); err == nil {
				continue
			}
			result = json.RawMessage(`{"changes":{}}`)
		case "textDocument/hover":
			if _, err = os.Stat(filepath.Join(root, "stall")); err == nil {
				_ = os.WriteFile(filepath.Join(root, "request-seen"), []byte("ok"), 0600)
				continue
			}
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			result, _ = json.Marshal(map[string]any{"contents": map[string]string{"kind": "plaintext", "value": fmt.Sprintf("pid=%d text=%s", os.Getpid(), documents[params.TextDocument.URI])}})
		}
		if err = lsp.WriteMessage(os.Stdout, &lsp.Message{JSONRPC: "2.0", ID: msg.ID, Result: result}); err != nil {
			os.Exit(3)
		}
	}
}

type adapterTestClient struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	reader *bufio.Reader
	errors bytes.Buffer
}

func startTestAdapter(binary, workspace, state string, extra ...string) (*adapterTestClient, error) {
	args := []string{"--workspace", workspace, "--lsp", os.Args[0], "--broker-dir", state, "--request-timeout", "3s", "--init-timeout", "4s", "--lock-timeout", "1s", "--idle-timeout", "1s", "--restart-limit", "2", "--restart-window", "30s"}
	args = append(args, extra...)
	args = append(args, "--", "-test.run=^TestBrokerFakeLSP$")
	client := &adapterTestClient{cmd: exec.Command(binary, args...)}
	client.cmd.Stderr = &client.errors
	input, err := client.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	client.input = input
	output, err := client.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client.reader = bufio.NewReader(output)
	if err = client.cmd.Start(); err != nil {
		return nil, err
	}
	return client, nil
}
func (c *adapterTestClient) exchange(id int, method string, params any) (map[string]any, error) {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err = c.input.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response map[string]any
	if err = json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	if response["id"] != float64(id) {
		return nil, fmt.Errorf("response for wrong session/ID: %s", line)
	}
	return response, nil
}
func (c *adapterTestClient) initialize() error {
	_, err := c.exchange(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test-agent", "version": "1"}})
	return err
}
func (c *adapterTestClient) close(t *testing.T) {
	t.Helper()
	_ = c.input.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("adapter exit: %v\n%s", err, c.errors.String())
		}
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
		t.Error("adapter remained alive after stdin EOF")
	}
}
func responseText(response map[string]any) string {
	data, _ := json.Marshal(response)
	return string(data)
}
func startedPIDs(root string) []int {
	data, _ := os.ReadFile(filepath.Join(root, "starts"))
	result := []int{}
	for _, line := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(line)
		if err == nil {
			result = append(result, pid)
		}
	}
	return result
}
func await(t *testing.T, timeout time.Duration, predicate func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(message)
}
func buildBrokerBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mcp-language-server.exe")
	args := []string{"build"}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "-o", binary, ".")
	cmd := exec.Command("go", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build broker: %v\n%s", err, output)
	}
	return binary
}
func cleanupTestBroker(t *testing.T, state string) {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(state, "*.log"))
	for _, path := range logs {
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte("WARNING: DATA RACE")) {
			t.Errorf("broker race detector: %s", data)
		}
	}
	entries, _ := filepath.Glob(filepath.Join(state, "*.json"))
	for _, path := range entries {
		data, _ := os.ReadFile(path)
		var endpoint brokerEndpoint
		if json.Unmarshal(data, &endpoint) == nil && endpoint.PID > 0 {
			if p, err := os.FindProcess(endpoint.PID); err == nil {
				_ = p.Kill()
			}
		}
	}
}
func TestSharedBrokerAcrossProcesses(t *testing.T) {
	binary := buildBrokerBinary(t)
	root := t.TempDir()
	state := t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	document := filepath.Join(root, "main.go")
	if err := os.WriteFile(document, []byte("initial-content"), 0600); err != nil {
		t.Fatal(err)
	}
	const count = 6
	clients := make([]*adapterTestClient, count)
	results := make(chan error, count)
	for i := range clients {
		go func(i int) {
			client, err := startTestAdapter(binary, root, state)
			clients[i] = client
			if err == nil {
				err = client.initialize()
			}
			results <- err
		}(i)
	}
	for i := 0; i < count; i++ {
		if err := <-results; err != nil {
			t.Fatalf("simultaneous startup: %v", err)
		}
	}
	for _, client := range clients {
		defer func(c *adapterTestClient) {
			if c.cmd.ProcessState == nil {
				c.close(t)
			}
		}(client)
	}
	if got := len(startedPIDs(root)); got != 1 {
		t.Fatalf("started %d LSPs for %d agents", got, count)
	}
	params := map[string]any{"name": "hover", "arguments": map[string]any{"filePath": document, "line": 1, "column": 1}}
	var wg sync.WaitGroup
	errors := make(chan error, count)
	for _, client := range clients {
		wg.Add(1)
		go func(c *adapterTestClient) {
			defer wg.Done()
			response, err := c.exchange(2, "tools/call", params)
			if err == nil && !strings.Contains(responseText(response), "initial-content") {
				err = fmt.Errorf("bad hover: %v", response)
			}
			errors <- err
		}(client)
	}
	wg.Wait()
	for i := 0; i < count; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	clients[0].close(t)
	response, err := clients[1].exchange(3, "tools/call", params)
	if err != nil || !strings.Contains(responseText(response), "initial-content") {
		t.Fatalf("disconnect killed shared LSP: %v %v", response, err)
	}
	// Stall one request. Its response must be bounded and the next generation
	// must see disk changes made while the previous LSP was hung.
	_ = os.WriteFile(filepath.Join(root, "stall"), []byte("yes"), 0600)
	started := time.Now()
	type pendingOutcome struct {
		response map[string]any
		err      error
	}
	pending := make(chan pendingOutcome, 1)
	go func() {
		response, err := clients[1].exchange(4, "tools/call", params)
		pending <- pendingOutcome{response, err}
	}()
	await(t, time.Second, func() bool { _, err := os.Stat(filepath.Join(root, "request-seen")); return err == nil }, "silent request never reached LSP")
	liveStatus, statusErr := clients[3].exchange(4, "tools/call", map[string]any{"name": "lsp_status", "arguments": map[string]any{}})
	if statusErr != nil || !strings.Contains(responseText(liveStatus), "textDocument/hover") {
		t.Fatalf("status blocked behind stalled tool: %v %v", liveStatus, statusErr)
	}
	outcome := <-pending
	response, err = outcome.response, outcome.err
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 6*time.Second || !strings.Contains(responseText(response), "isError\":true") {
		t.Fatalf("silent LSP was not interrupted: %v", response)
	}
	_ = os.WriteFile(document, []byte("updated-after-stall"), 0600)
	_ = os.Remove(filepath.Join(root, "stall"))
	await(t, 8*time.Second, func() bool { return len(startedPIDs(root)) == 2 }, "watchdog did not start a second LSP")
	response, err = clients[2].exchange(5, "tools/call", params)
	if err != nil || !strings.Contains(responseText(response), "updated-after-stall") {
		t.Fatalf("restart did not restore documents: %v %v", response, err)
	}
	dumps, _ := filepath.Glob(filepath.Join(state, "diagnostics", "*.json"))
	if len(dumps) == 0 {
		t.Fatal("stall produced no diagnostic snapshot")
	}
	sawRequest := false
	for _, path := range dumps {
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte("textDocument/hover")) {
			sawRequest = true
		}
		if bytes.Contains(data, []byte("initial-content")) {
			t.Fatal("diagnostic leaked document contents")
		}
	}
	if !sawRequest {
		t.Fatal("diagnostics lost the stalled request state")
	}
	// Exhaust the restart budget. Requests must return errors without starting
	// more children or looping forever, while lsp_status remains available.
	for expected := 3; expected <= 4; expected++ {
		_ = os.WriteFile(filepath.Join(root, "stall"), []byte("yes"), 0600)
		_, err = clients[2].exchange(10+expected, "tools/call", params)
		if err != nil {
			t.Fatal(err)
		}
		if expected == 3 {
			await(t, 8*time.Second, func() bool { return len(startedPIDs(root)) == 3 }, "second restart did not happen")
		}
	}
	response, err = clients[3].exchange(20, "tools/call", params)
	if err != nil || !strings.Contains(responseText(response), "restart limit") {
		t.Fatalf("restart storm was not stopped: %v %v", response, err)
	}
	if got := len(startedPIDs(root)); got != 3 {
		t.Fatalf("restart budget allowed %d starts", got)
	}
	response, err = clients[3].exchange(21, "tools/call", map[string]any{"name": "lsp_status", "arguments": map[string]any{}})
	if err != nil || !strings.Contains(responseText(response), "healthy_transport") {
		t.Fatalf("status unavailable after restart limit: %v %v", response, err)
	}
	for _, client := range clients[1:] {
		client.close(t)
	}
	await(t, 8*time.Second, func() bool { entries, _ := filepath.Glob(filepath.Join(state, "*.json")); return len(entries) == 0 }, "broker did not stop after the last client disconnected")
}

func TestBrokerOwnerLockReleasedOnProcessDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnerLockHelper$")
	cmd.Env = append(os.Environ(), "OWNER_LOCK_HELPER="+path)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("lock helper: %q %v", line, err)
	}
	owner, err := tryOwnerLock(path)
	if err != nil || owner != nil {
		if owner != nil {
			_ = owner.Close()
		}
		t.Fatalf("two processes acquired the owner lock: %v", err)
	}
	_ = cmd.Process.Kill()
	await(t, 3*time.Second, func() bool {
		file, err := tryOwnerLock(path)
		if err != nil || file == nil {
			return false
		}
		_ = file.Close()
		return true
	}, "kernel lock stayed held after process death")
}
func TestOwnerLockHelper(t *testing.T) {
	path := os.Getenv("OWNER_LOCK_HELPER")
	if path == "" {
		return
	}
	owner, err := tryOwnerLock(path)
	if err != nil || owner == nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, "locked")
	for {
		time.Sleep(time.Second)
	}
}

func TestSupervisorQueueHasTotalDeadline(t *testing.T) {
	s, err := newServer(&config{requestTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.cancelFunc()
	s.supervisor.gate <- struct{}{}
	defer func() { <-s.supervisor.gate }()
	called := false
	handler := s.supervisedTool(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return nil, nil
	})
	started := time.Now()
	result, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil || result == nil || !result.IsError || called || time.Since(started) > time.Second {
		t.Fatalf("queue deadline failed: %v %v called=%t", result, err, called)
	}
}

func TestSharedBrokerNeverReplaysMutation(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	document := filepath.Join(root, "main.go")
	_ = os.WriteFile(document, []byte("package main\nvar value = 1\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "stall-rename"), []byte("yes"), 0600)
	client, err := startTestAdapter(binary, root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close(t)
	if err = client.initialize(); err != nil {
		t.Fatal(err)
	}
	response, err := client.exchange(2, "tools/call", map[string]any{"name": "rename_symbol", "arguments": map[string]any{"filePath": document, "line": 2, "column": 5, "newName": "other"}})
	if err != nil || !strings.Contains(responseText(response), "isError\":true") {
		t.Fatalf("stalled mutation: %v %v", response, err)
	}
	await(t, 8*time.Second, func() bool { return len(startedPIDs(root)) == 2 }, "mutation stall did not recover")
	data, _ := os.ReadFile(filepath.Join(root, "renames"))
	if len(strings.Fields(string(data))) != 1 {
		t.Fatalf("rename was replayed: %q", data)
	}
	current, _ := os.ReadFile(document)
	if !strings.Contains(string(current), "value") {
		t.Fatalf("failed rename modified file: %s", current)
	}
}
func TestSharedBrokerStartupIsBounded(t *testing.T) {
	binary := buildBrokerBinary(t)
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BROKER_FAKE_ROOT", root)
	t.Cleanup(func() { cleanupTestBroker(t, state) })
	_ = os.WriteFile(filepath.Join(root, "stall-init"), []byte("yes"), 0600)
	client, err := startTestAdapter(binary, root, state, "--init-timeout", "250ms", "--lock-timeout", "250ms")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.initialize() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("silent initialization succeeded")
		}
	case <-time.After(5 * time.Second):
		_ = client.cmd.Process.Kill()
		t.Fatal("initialization blocked indefinitely")
	}
	_ = client.input.Close()
	if err = client.cmd.Wait(); err == nil {
		t.Fatal("failed startup did not return an error exit")
	}
	if !strings.Contains(client.errors.String(), "broker unavailable") {
		t.Fatalf("missing startup diagnostics: %s", client.errors.String())
	}
}
func TestBrokerIdentitySeparatesWorktreesAndLSPConfiguration(t *testing.T) {
	a := &config{workspaceDir: t.TempDir(), lspCommand: os.Args[0], lspArgs: []string{"--one"}}
	applyDefaults(a)
	first, err := brokerIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	same := *a
	again, err := brokerIdentity(&same)
	if err != nil || again != first {
		t.Fatalf("identical configuration did not share identity: %v", err)
	}
	other := *a
	other.workspaceDir = t.TempDir()
	different, err := brokerIdentity(&other)
	if err != nil || different == first {
		t.Fatalf("worktrees shared state: %v", err)
	}
	other = *a
	other.lspArgs = []string{"--two"}
	different, err = brokerIdentity(&other)
	if err != nil || different == first {
		t.Fatalf("different LSP arguments shared state: %v", err)
	}
	t.Setenv("LSP_CONFIGURATION_TEST", "changed")
	different, err = brokerIdentity(a)
	if err != nil || different == first {
		t.Fatalf("different LSP environment shared state: %v", err)
	}
}
