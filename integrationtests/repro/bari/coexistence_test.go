// Package bari reproduces bari builds colliding with csharp-ls sessions that
// run through the bridge. It needs Windows, the .NET SDK with the WPF targets,
// csharp-ls and pwsh, so it only runs when BARI_REPRO=1. See README.md.
package bari

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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reproConfig struct {
	bridge    string
	csharpLS  string
	lspArgs   []string
	sessions  int
	cycles    int
	modules   int
	workspace string
	output    string
	env       []string
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value >= 0 {
		return value
	}
	return fallback
}

func loadConfig(t *testing.T) reproConfig {
	t.Helper()
	cfg := reproConfig{
		bridge:   os.Getenv("BRIDGE_BIN"),
		csharpLS: os.Getenv("CSHARP_LS"),
		sessions: envInt("REPRO_SESSIONS", 2),
		cycles:   envInt("REPRO_CYCLES", 3),
		modules:  envInt("REPRO_MODULES", 6),
		output:   os.Getenv("REPRO_OUTPUT"),
	}
	if args := os.Getenv("REPRO_LSP_ARGS"); args != "" {
		cfg.lspArgs = strings.Fields(args)
	}
	if cfg.csharpLS == "" {
		cfg.csharpLS = "csharp-ls"
	}
	if cfg.sessions > 0 {
		if cfg.bridge == "" {
			t.Fatal("BRIDGE_BIN must name the mcp-language-server binary under test")
		}
		for _, name := range []*string{&cfg.bridge, &cfg.csharpLS} {
			path, err := exec.LookPath(*name)
			if err != nil {
				t.Fatalf("%s: %v", *name, err)
			}
			*name = path
		}
	}
	if cfg.output == "" {
		cfg.output = filepath.Join(os.TempDir(), "bari-repro")
	}
	cfg.output, _ = filepath.Abs(cfg.output)
	cfg.workspace = filepath.Join(cfg.output, "workspace")
	// Extra environment for the bridge and its language server, e.g.
	// REPRO_SESSION_ENV="CustomBeforeMicrosoftCommonTargets=C:\x.targets".
	for _, entry := range strings.Split(os.Getenv("REPRO_SESSION_ENV"), ";;") {
		if strings.Contains(entry, "=") {
			cfg.env = append(cfg.env, entry)
		}
	}
	return cfg
}

// ------------------------------------------------------------------- bari

type stepResult struct {
	name        string
	exitCode    int
	duration    time.Duration
	cs2001      int
	bg1002      int
	sharing     int
	targetClean int
	errors      []string
	reloads     []int64
	isolated    int
	holders     []string
}

func (r stepResult) failed() bool { return r.exitCode != 0 || len(r.errors) != 0 }

var (
	errorLine = regexp.MustCompile(`(?m)^.*(?:: error [A-Z]+\d+|being used by another process|Failed to clean target root).*$`)
	holders   = regexp.MustCompile(`(?m)^REPRO-HOLDERS .*$`)
)

func runBari(t *testing.T, cfg reproConfig, command, logName string) stepResult {
	t.Helper()
	script, err := filepath.Abs("bari.ps1")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cmd := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-File", script,
		"-Command", command, "-Workspace", cfg.workspace, "-Modules", strconv.Itoa(cfg.modules))
	cmd.Dir = cfg.workspace
	// Keep the build's MSBuild nodes from outliving the step like a user's
	// default node reuse would; they are counted separately from the language server.
	cmd.Env = append(os.Environ(), "MSBUILDDISABLENODEREUSE=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
	output, runErr := cmd.CombinedOutput()
	result := stepResult{name: logName, duration: time.Since(start)}
	if runErr != nil {
		result.exitCode = -1
		if exit, ok := runErr.(*exec.ExitError); ok {
			result.exitCode = exit.ExitCode()
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.output, logName+".log"), output, 0o644); err != nil {
		t.Fatal(err)
	}
	text := string(output)
	result.cs2001 = strings.Count(text, "error CS2001")
	result.bg1002 = strings.Count(text, "error BG1002")
	result.sharing = strings.Count(text, "being used by another process")
	result.targetClean = strings.Count(text, "Failed to clean target root")
	seen := map[string]bool{}
	for _, line := range errorLine.FindAllString(text, -1) {
		line = strings.TrimSpace(line)
		if !seen[line] {
			seen[line] = true
			result.errors = append(result.errors, line)
		}
	}
	result.holders = holders.FindAllString(text, -1)
	result.isolated = countIsolated(filepath.Join(cfg.workspace, "target", "tmp"))
	return result
}

func isDesignTimePath(path string) bool {
	return strings.Contains(path, string(filepath.Separator)+"designtime"+string(filepath.Separator))
}

// countIsolated counts files design-time builds wrote to their own directory.
func countIsolated(root string) (count int) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && isDesignTimePath(path) {
			count++
		}
		return nil
	})
	return count
}

// markupOutputsSince counts the WPF markup compiler's outputs in the build's
// own intermediate directories written after since. Outside Visual Studio the
// markup compiler runs in real-build mode during design-time builds too, so
// it writes (and deletes) *.g.cs, *.baml and its state cache, not *.g.i.cs.
func markupOutputsSince(root string, since time.Time) (count int) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || isDesignTimePath(path) {
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".g.cs") && !strings.HasSuffix(name, ".g.i.cs") &&
			!strings.HasSuffix(name, ".baml") && !strings.HasSuffix(name, "_MarkupCompile.cache") {
			return nil
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(since) {
			count++
		}
		return nil
	})
	return count
}

// ------------------------------------------------------------------- MCP

type session struct {
	id      int
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	log     *os.File
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	reloads atomic.Int64
	exited  chan struct{}
	exitErr error
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func startSession(t *testing.T, cfg reproConfig, id int) *session {
	t.Helper()
	args := []string{"--workspace", cfg.workspace, "--lsp", cfg.csharpLS}
	if len(cfg.lspArgs) != 0 {
		args = append(append(args, "--"), cfg.lspArgs...)
	}
	cmd := exec.Command(cfg.bridge, args...)
	cmd.Dir = cfg.workspace
	// Wire logging records the server's window/logMessage notifications.
	cmd.Env = append(append(os.Environ(), "LOG_LEVEL=INFO", "LOG_COMPONENT_LEVELS=wire:DEBUG", "LSP_CONTEXT_LINES=0"), cfg.env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(cfg.output, fmt.Sprintf("session-%d.log", id)))
	if err != nil {
		t.Fatal(err)
	}
	s := &session{id: id, cmd: cmd, stdin: stdin, log: logFile, pending: map[int]chan rpcResponse{}, exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go s.readStderr(stderr)
	go s.readStdout(stdout)
	go func() {
		s.exitErr = cmd.Wait()
		close(s.exited)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "bari-repro", "version": "0"},
	}); err != nil {
		t.Fatalf("session %d initialize: %v", id, err)
	}
	if err := s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *session) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "will reload solution") {
			s.reloads.Add(1)
		}
		_, _ = fmt.Fprintf(s.log, "%s %s\n", time.Now().Format("15:04:05.000"), line)
	}
}

func (s *session) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var message struct {
			ID *int `json:"id"`
			rpcResponse
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID == nil {
			continue
		}
		s.mu.Lock()
		reply := s.pending[*message.ID]
		delete(s.pending, *message.ID)
		s.mu.Unlock()
		if reply != nil {
			reply <- message.rpcResponse
		}
	}
}

func (s *session) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	reply := make(chan rpcResponse, 1)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = reply
	s.mu.Unlock()
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case response := <-reply:
		if response.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, response.Error.Message)
		}
		return response.Result, nil
	case <-s.exited:
		return nil, fmt.Errorf("bridge exited: %v", s.exitErr)
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *session) tool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := s.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	var text strings.Builder
	for _, content := range result.Content {
		text.WriteString(content.Text)
	}
	if result.IsError {
		return "", fmt.Errorf("%s", text.String())
	}
	return text.String(), nil
}

// answersCorrectly checks definition and references against the generated sources.
func (s *session) answersCorrectly(ctx context.Context, modules int) error {
	definition, err := s.tool(ctx, "definition", map[string]any{"symbolName": "WidgetCatalog"})
	if err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if !strings.Contains(definition, "WidgetCatalog.cs") {
		return fmt.Errorf("definition did not return WidgetCatalog.cs: %.300s", definition)
	}
	references, err := s.tool(ctx, "references", map[string]any{"symbolName": "WidgetCatalog"})
	if err != nil {
		return fmt.Errorf("references: %w", err)
	}
	for m := 1; m <= modules; m++ {
		if !strings.Contains(references, fmt.Sprintf("Extensions.Mod%d", m)) {
			return fmt.Errorf("references miss Extensions.Mod%d: %.300s", m, references)
		}
	}
	return nil
}

func (s *session) waitCorrect(ctx context.Context, modules int) (time.Duration, error) {
	start := time.Now()
	var last error
	for {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		last = s.answersCorrectly(callCtx, modules)
		cancel()
		if last == nil {
			return time.Since(start), nil
		}
		select {
		case <-ctx.Done():
			return time.Since(start), fmt.Errorf("%w (last: %v)", ctx.Err(), last)
		case <-s.exited:
			return time.Since(start), fmt.Errorf("bridge exited: %v", s.exitErr)
		case <-time.After(5 * time.Second):
		}
	}
}

// serverMessages extracts the language server's own log and messages from a
// session log, leaving out reload notices and other wire traffic.
func serverMessages(file string, limit int) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return err.Error()
	}
	var kept []string
	dropped := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "will reload solution") {
			continue
		}
		if !strings.Contains(line, "window/logMessage") && !strings.Contains(line, "[lsp-process]") &&
			!strings.Contains(line, "Server ") && !strings.Contains(line, "[ERROR]") && !strings.Contains(line, "[WARN]") {
			continue
		}
		if len(kept) >= limit {
			dropped++
			continue
		}
		if len(line) > 400 {
			line = line[:400] + "..."
		}
		kept = append(kept, line)
	}
	if dropped != 0 {
		kept = append(kept, fmt.Sprintf("... %d more lines", dropped))
	}
	return strings.Join(kept, "\n")
}

// listTree lists files below dir with sizes and modification times.
func listTree(dir string, limit int) string {
	var lines []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		lines = append(lines, fmt.Sprintf("%s %7d %s", info.ModTime().Format("15:04:05.000"), info.Size(), rel))
		return nil
	})
	if len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("... %d more files", len(lines)-limit))
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------- processes

type process struct {
	PID     int    `json:"ProcessId"`
	Parent  int    `json:"ParentProcessId"`
	Name    string `json:"Name"`
	Command string `json:"CommandLine"`
}

func listProcesses(t *testing.T) []process {
	t.Helper()
	out, err := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine | ConvertTo-Json -Compress").Output()
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	var processes []process
	if err := json.Unmarshal(bytes.TrimSpace(out), &processes); err != nil {
		t.Fatalf("parsing process list: %v", err)
	}
	return processes
}

func descendants(processes []process, root int) []process {
	children := map[int][]process{}
	for _, p := range processes {
		children[p.Parent] = append(children[p.Parent], p)
	}
	var result []process
	queue := []int{root}
	for len(queue) != 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if child.PID != root {
				result = append(result, child)
				queue = append(queue, child.PID)
			}
		}
	}
	return result
}

func survivors(t *testing.T, before []process, wait time.Duration) []process {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		alive := map[int]process{}
		for _, p := range listProcesses(t) {
			alive[p.PID] = p
		}
		var left []process
		for _, p := range before {
			if now, ok := alive[p.PID]; ok && strings.EqualFold(now.Name, p.Name) {
				left = append(left, p)
			}
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(time.Second)
	}
}

func describe(processes []process) string {
	var parts []string
	for _, p := range processes {
		command := p.Command
		if len(command) > 140 {
			command = command[:140] + "..."
		}
		parts = append(parts, fmt.Sprintf("%d %s (%s)", p.PID, p.Name, command))
	}
	return strings.Join(parts, "; ")
}

// ------------------------------------------------------------------- report

type report struct {
	t        *testing.T
	cfg      reproConfig
	summary  strings.Builder
	failures []string
}

func newReport(t *testing.T, cfg reproConfig, title string) *report {
	r := &report{t: t, cfg: cfg}
	r.printf("### %s: %s\n\n", title, os.Getenv("REPRO_NAME"))
	r.printf("sessions=%d cycles=%d modules=%d language server env=%q\n\n", cfg.sessions, cfg.cycles, cfg.modules, cfg.env)
	return r
}

func (r *report) printf(format string, args ...any) { fmt.Fprintf(&r.summary, format, args...) }

func (r *report) fail(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *report) stepHeader() {
	r.printf("| step | exit | time | CS2001 | BG1002 | sharing violation | target clean failed | reload notices | files in designtime dirs |\n")
	r.printf("|---|---|---|---|---|---|---|---|---|\n")
}

func (r *report) step(result stepResult) {
	r.printf("| %s | %d | %s | %d | %d | %d | %d | %v | %d |\n", result.name, result.exitCode,
		result.duration.Round(time.Second), result.cs2001, result.bg1002, result.sharing, result.targetClean,
		result.reloads, result.isolated)
	if result.failed() {
		r.fail("%s: exit %d: %s", result.name, result.exitCode, strings.Join(result.errors, " | "))
		r.failures = append(r.failures, result.holders...)
	}
}

// close publishes the summary and fails the test if anything failed.
func (r *report) close() {
	if len(r.failures) != 0 {
		r.printf("\nFailures:\n\n")
		for _, failure := range r.failures {
			r.printf("- %s\n", failure)
		}
	} else {
		r.printf("\nNo failures.\n")
	}
	text := r.summary.String()
	r.t.Log("\n" + text)
	_ = os.WriteFile(filepath.Join(r.cfg.output, "summary.md"), []byte(text), 0o644)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			_, _ = f.WriteString(text)
			_ = f.Close()
		}
	}
	if len(r.failures) != 0 {
		r.t.Errorf("%d failure(s); see the summary", len(r.failures))
	}
}

// ------------------------------------------------------------------- tests

func setup(t *testing.T) reproConfig {
	t.Helper()
	if os.Getenv("BARI_REPRO") != "1" {
		t.Skip("set BARI_REPRO=1 to run the bari reproduction")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the reproduction needs Windows and the WPF build targets")
	}
	cfg := loadConfig(t)
	if err := os.RemoveAll(cfg.output); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestBariCoexistence loads csharp-ls through the bridge and cycles bari
// clean, build and rebuild underneath it.
func TestBariCoexistence(t *testing.T) {
	cfg := setup(t)
	r := newReport(t, cfg, "bari coexistence")
	defer r.close()
	tmp := filepath.Join(cfg.workspace, "target", "tmp")

	initial := runBari(t, cfg, "build", "00-initial-build")
	if initial.failed() {
		t.Fatalf("initial build without language servers failed (exit %d): %v", initial.exitCode, initial.errors)
	}
	built := time.Now()

	var sessions []*session
	for i := 1; i <= cfg.sessions; i++ {
		sessions = append(sessions, startSession(t, cfg, i))
	}
	defer func() {
		for _, s := range sessions {
			if s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
			_ = s.log.Close()
		}
	}()
	for _, s := range sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		took, err := s.waitCorrect(ctx, cfg.modules)
		cancel()
		if err != nil {
			t.Fatalf("session %d never answered correctly: %v", s.id, err)
		}
		r.printf("session %d loaded in %s\n", s.id, took.Round(time.Second))
	}
	probe := filepath.Join(tmp, "Mod1", "Extensions.Mod1")
	if len(sessions) != 0 {
		time.Sleep(15 * time.Second)
		r.printf("\nWPF markup outputs in the build's intermediate directories rewritten since the initial build: %d; files in designtime dirs: %d\n\n",
			markupOutputsSince(tmp, built), countIsolated(tmp))
		r.printf("Extensions.Mod1 intermediate files after the sessions loaded:\n```\n%s\n```\n\n", listTree(probe, 80))
	}

	r.stepHeader()
	for i, command := range cycleSteps(cfg.cycles, "clean", "build", "rebuild") {
		before := make([]int64, len(sessions))
		for j, s := range sessions {
			before[j] = s.reloads.Load()
		}
		result := runBari(t, cfg, command, fmt.Sprintf("%02d-%s", i+1, command))
		for j, s := range sessions {
			result.reloads = append(result.reloads, s.reloads.Load()-before[j])
		}
		r.step(result)
	}

	r.printf("\nRecovery after the last build:\n\n")
	for _, s := range sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		took, err := s.waitCorrect(ctx, cfg.modules)
		cancel()
		if err != nil {
			r.fail("session %d did not recover: %v", s.id, err)
			r.printf("- session %d: **not recovered**: %v\n", s.id, err)
		} else {
			r.printf("- session %d: correct definition/references after %s\n", s.id, took.Round(time.Second))
		}
	}
	if len(sessions) == 0 {
		return
	}
	r.printf("\nExtensions.Mod1 intermediate files at the end:\n```\n%s\n```\n", listTree(probe, 80))
	r.printf("\nSession 1 server messages:\n```\n%s\n```\n", serverMessages(filepath.Join(cfg.output, "session-1.log"), 150))
	r.printf("\nLifecycle:\n\n")
	processes := listProcesses(t)
	for i, s := range sessions {
		tree := descendants(processes, s.cmd.Process.Pid)
		how := "stdin closed (client exited)"
		if i%2 == 1 {
			how = "bridge killed (TerminateProcess)"
			_ = s.cmd.Process.Kill()
		} else {
			_ = s.stdin.Close()
		}
		left := survivors(t, tree, 20*time.Second)
		if len(left) != 0 {
			r.fail("session %d, %s: descendants survived: %s", s.id, how, describe(left))
			r.printf("- session %d, %s: **%d of %d descendants survived**: %s\n", s.id, how, len(left), len(tree), describe(left))
		} else {
			r.printf("- session %d, %s: all %d descendants exited: %s\n", s.id, how, len(tree), describe(tree))
		}
	}
}

// TestDesignTimeRace runs design-time builds the way Roslyn's build host does,
// back to back, while bari builds: the collision without a language server's
// reload timing in the way. REPRO_SESSION_ENV applies to the design-time builds.
func TestDesignTimeRace(t *testing.T) {
	cfg := setup(t)
	r := newReport(t, cfg, "design-time race")
	defer r.close()
	tmp := filepath.Join(cfg.workspace, "target", "tmp")

	initial := runBari(t, cfg, "build", "00-initial-build")
	if initial.failed() {
		t.Fatalf("initial build failed (exit %d): %v", initial.exitCode, initial.errors)
	}
	built := time.Now()
	script, err := filepath.Abs("bari.ps1")
	if err != nil {
		t.Fatal(err)
	}
	loop := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-File", script,
		"-Command", "designtime", "-Workspace", cfg.workspace, "-Modules", strconv.Itoa(cfg.modules))
	loop.Env = append(append(os.Environ(), "MSBUILDDISABLENODEREUSE=1"), cfg.env...)
	var loopOutput bytes.Buffer
	loop.Stdout, loop.Stderr = &loopOutput, &loopOutput
	if err := loop.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loop.Process.Kill() }()
	time.Sleep(30 * time.Second)
	r.printf("WPF markup outputs in the build's intermediate directories rewritten by design-time builds alone: %d; files in designtime dirs: %d\n\n",
		markupOutputsSince(tmp, built), countIsolated(tmp))

	r.stepHeader()
	for i, command := range cycleSteps(cfg.cycles, "build", "rebuild") {
		r.step(runBari(t, cfg, command, fmt.Sprintf("%02d-%s", i+1, command)))
	}

	if err := os.WriteFile(filepath.Join(cfg.workspace, "designtime.stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- loop.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		t.Fatal("design-time loop did not stop")
	}
	r.printf("\n%s\n", strings.TrimSpace(loopOutput.String()))
}

func cycleSteps(cycles int, commands ...string) []string {
	var steps []string
	for range cycles {
		steps = append(steps, commands...)
	}
	return steps
}
