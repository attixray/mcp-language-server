package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type supervisor struct {
	gate         chan struct{}
	paths        []string
	restarts     []time.Time
	historyMu    sync.Mutex
	queued       atomic.Int64
	generation   atomic.Uint64
	active       atomic.Pointer[toolActivity]
	poisoned     atomic.Bool
	retired      *lsp.Client
	diagnosticMu sync.Mutex
	lastDump     time.Time
}
type toolActivity struct {
	Tool      string    `json:"tool"`
	Session   string    `json:"session,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Started   time.Time `json:"started"`
	Deadline  time.Time `json:"deadline"`
}

func newSupervisor() *supervisor { return &supervisor{gate: make(chan struct{}, 1)} }
func applyDefaults(c *config) {
	if c.requestTimeout == 0 {
		c.requestTimeout = 60 * time.Second
	}
	if c.initTimeout == 0 {
		c.initTimeout = 120 * time.Second
	}
	if c.lockTimeout == 0 {
		c.lockTimeout = 15 * time.Second
	}
	if c.idleTimeout == 0 {
		c.idleTimeout = 2 * time.Minute
	}
	if c.restartWindow == 0 {
		c.restartWindow = 5 * time.Minute
	}
	// Programmatic construction, used by existing tests, has no automatic retry.
}
func (s *mcpServer) currentClient() *lsp.Client {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.lspClient
}
func (s *mcpServer) supervisedTool(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(parent context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if request.Params.Name == "lsp_status" {
			return next(parent, request)
		}
		ctx, cancel := context.WithTimeout(parent, s.config.requestTimeout)
		defer cancel()
		queued := time.Now()
		s.supervisor.queued.Add(1)
		select {
		case s.supervisor.gate <- struct{}{}:
			s.supervisor.queued.Add(-1)
		case <-ctx.Done():
			s.supervisor.queued.Add(-1)
			coreLogger.Warn("Tool %s queue deadline after %s", request.Params.Name, time.Since(queued))
			return mcp.NewToolResultError("LSP queue wait expired; retry when the workspace is available"), nil
		case <-s.ctx.Done():
			s.supervisor.queued.Add(-1)
			return mcp.NewToolResultError("LSP broker shutting down"), nil
		}
		defer func() { <-s.supervisor.gate }()
		if s.supervisor.poisoned.Load() {
			return mcp.NewToolResultError("LSP worker did not stop; broker requires restart"), nil
		}
		if err := ctx.Err(); err != nil {
			return mcp.NewToolResultError("LSP queue deadline expired"), nil
		}
		if err := s.ensureLSP(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		client := s.currentClient()
		deadline, _ := ctx.Deadline()
		activity := &toolActivity{Tool: request.Params.Name, Started: time.Now(), Deadline: deadline}
		if session := server.ClientSessionFromContext(ctx); session != nil {
			activity.Session = session.SessionID()
		}
		activity.RequestID, _ = ctx.Value(brokerRequestIDKey{}).(string)
		s.supervisor.active.Store(activity)
		defer s.supervisor.active.Store(nil)
		coreLogger.Debug("Tool %s generation=%d queue=%s", request.Params.Name, s.supervisor.generation.Load(), time.Since(queued))
		type outcome struct {
			result *mcp.CallToolResult
			err    error
		}
		completed := make(chan outcome, 1)
		go func() {
			// The MCP recovery middleware surrounds this function, not this worker.
			defer func() {
				if recovered := recover(); recovered != nil {
					completed <- outcome{err: fmt.Errorf("tool panic: %v", recovered)}
				}
			}()
			result, err := next(ctx, request)
			completed <- outcome{result, err}
		}()
		select {
		case result := <-completed:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				client.Abort()
				s.dumpDiagnostic("tool deadline", client)
				return mcp.NewToolResultError("LSP operation exceeded its deadline; verify mutation results before retrying"), nil
			}
			return result.result, result.err
		case <-ctx.Done():
			client.Abort()
			s.dumpDiagnostic("tool deadline or cancellation", client)
			// Never replace the shared client while a mutating worker could still run.
			select {
			case <-completed:
			case <-time.After(2 * time.Second):
				s.supervisor.poisoned.Store(true)
			}
			return mcp.NewToolResultError("LSP operation interrupted; its transport was closed. Mutations are never automatically replayed; check file state before retrying."), nil
		case <-s.ctx.Done():
			client.Abort()
			return mcp.NewToolResultError("LSP broker shutting down"), nil
		}
	}
}

// Called only while holding the tool gate. The old watcher is canceled and
// the old process tree is reaped before the new client can become visible.
func (s *mcpServer) ensureLSP(ctx context.Context) error {
	client := s.currentClient()
	if client != nil {
		select {
		case <-client.Done():
		default:
			return nil
		}
	}
	if client != nil && s.supervisor.retired != client {
		s.supervisor.retired = client
		s.dumpDiagnostic("transport stopped; restarting", client)
		pathSet := map[string]bool{}
		for _, path := range append(s.supervisor.paths, client.OpenPaths()...) {
			pathSet[path] = true
		}
		s.supervisor.paths = nil
		for path := range pathSet {
			s.supervisor.paths = append(s.supervisor.paths, path)
		}
		if s.watcherCancel != nil {
			s.watcherCancel()
		}
		client.Abort()
		_ = client.Close()
		if s.watcherDone != nil {
			select {
			case <-s.watcherDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	now := time.Now()
	s.supervisor.historyMu.Lock()
	kept := s.supervisor.restarts[:0]
	for _, t := range s.supervisor.restarts {
		if now.Sub(t) < s.config.restartWindow {
			kept = append(kept, t)
		}
	}
	s.supervisor.restarts = kept
	attempts := len(kept)
	s.supervisor.historyMu.Unlock()
	if attempts >= s.config.restartLimit {
		return fmt.Errorf("LSP restart limit reached (%d per %s); retry after the restart window", s.config.restartLimit, s.config.restartWindow)
	}
	delay := time.Second << min(attempts, 4)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
	}
	s.supervisor.historyMu.Lock()
	s.supervisor.restarts = append(s.supervisor.restarts, time.Now())
	s.supervisor.historyMu.Unlock()
	coreLogger.Warn("Restarting LSP generation=%d attempt=%d", s.supervisor.generation.Load(), attempts+1)
	if err := s.initializeLSPContext(ctx); err != nil {
		if c := s.currentClient(); c != nil {
			c.Abort()
		}
		return fmt.Errorf("LSP restart failed: %w", err)
	}
	s.supervisor.generation.Add(1)
	return nil
}

// A separate goroutine can close the transport without acquiring any gate.
// Restart itself acquires the gate so mutation and generation changes cannot race.
func (s *mcpServer) watchLSP() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			client := s.currentClient()
			if client == nil {
				continue
			}
			if a := s.supervisor.active.Load(); a != nil && time.Now().After(a.Deadline) {
				client.Abort()
				go s.dumpDiagnostic("watchdog deadline", client)
			}
			if s.supervisor.poisoned.Load() {
				continue
			}
			select {
			case <-client.Done():
			default:
				continue
			}
			select {
			case s.supervisor.gate <- struct{}{}:
				ctx, cancel := context.WithTimeout(s.ctx, s.config.initTimeout+20*time.Second)
				if err := s.ensureLSP(ctx); err != nil {
					coreLogger.Debug("Watchdog recovery: %v", err)
				}
				cancel()
				<-s.supervisor.gate
			default:
			}
		}
	}
}

func (s *mcpServer) dumpDiagnostic(reason string, client *lsp.Client) {
	s.supervisor.diagnosticMu.Lock()
	defer s.supervisor.diagnosticMu.Unlock()
	if time.Since(s.supervisor.lastDump) < time.Second {
		return
	}
	s.supervisor.lastDump = time.Now()
	dir, err := brokerStateDir(&s.config)
	if err != nil {
		coreLogger.Warn("Cannot locate diagnostic directory: %v", err)
		return
	}
	dir = filepath.Join(dir, "diagnostics")
	if err = os.MkdirAll(dir, 0700); err != nil {
		coreLogger.Warn("Cannot create diagnostics: %v", err)
		return
	}
	pid := 0
	if client.Cmd != nil && client.Cmd.Process != nil {
		pid = client.Cmd.Process.Pid
	}
	requests := client.Activities()
	if len(requests) == 0 {
		requests = client.FailedActivities()
	}
	state := struct {
		Reason     string         `json:"reason"`
		Time       time.Time      `json:"time"`
		BrokerPID  int            `json:"broker_pid"`
		LSPPID     int            `json:"lsp_pid"`
		Generation uint64         `json:"generation"`
		Active     *toolActivity  `json:"active_tool,omitempty"`
		Requests   []lsp.Activity `json:"requests"`
	}{reason, time.Now(), os.Getpid(), pid, s.supervisor.generation.Load(), s.supervisor.active.Load(), requests}
	data, _ := json.MarshalIndent(state, "", "  ")
	file, err := os.CreateTemp(dir, "stall-*.json")
	if err != nil {
		coreLogger.Warn("Cannot save diagnostics: %v", err)
		return
	}
	name := file.Name()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		coreLogger.Warn("Cannot write diagnostics: %v %v", writeErr, closeErr)
		return
	}
	buf := make([]byte, 1024*1024)
	n := runtime.Stack(buf, true)
	if err := os.WriteFile(name+".goroutines.txt", buf[:n], 0600); err != nil {
		coreLogger.Warn("Cannot save goroutines: %v", err)
	}
	coreLogger.Warn("LSP stalled: %s; diagnostics=%s", reason, name)
	// Bounded retention prevents an unattended watchdog filling the disk.
	files, _ := filepath.Glob(filepath.Join(dir, "stall-*.json"))
	if len(files) > 20 {
		// CreateTemp names are random; sort by mtime, not name.
		pruneDiagnostics(files, len(files)-20)
	}
}
