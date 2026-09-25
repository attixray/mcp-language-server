package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/logging"
	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/watcher"
	"github.com/mark3labs/mcp-go/server"
)

// Create a logger for the core component
var coreLogger = logging.NewLogger(logging.Core)

type config struct {
	workspaceDir  string
	lspCommand    string
	lspArgs       []string
	projectSettle time.Duration
}

type mcpServer struct {
	config           config
	lspClient        *lsp.Client
	mcpServer        *server.MCPServer
	ctx              context.Context
	cancelFunc       context.CancelFunc
	workspaceWatcher *watcher.WorkspaceWatcher
	lifecycleMu      sync.Mutex
	closing          bool
	cleanupOnce      sync.Once
}

func parseConfig() (*config, error) {
	cfg := &config{}
	flag.StringVar(&cfg.workspaceDir, "workspace", "", "Path to workspace directory")
	flag.StringVar(&cfg.lspCommand, "lsp", "", "LSP command to run (args should be passed after --)")
	flag.DurationVar(&cfg.projectSettle, "project-settle", watcher.DefaultWatcherConfig().ProjectSettleTime,
		"How long project and solution files must stay unchanged before their changes are reported")
	flag.Parse()

	// Get remaining args after -- as LSP arguments
	cfg.lspArgs = flag.Args()

	// Validate workspace directory
	if cfg.workspaceDir == "" {
		return nil, fmt.Errorf("workspace directory is required")
	}

	workspaceDir, err := filepath.Abs(cfg.workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for workspace: %v", err)
	}
	cfg.workspaceDir = workspaceDir

	if _, err := os.Stat(cfg.workspaceDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace directory does not exist: %s", cfg.workspaceDir)
	}

	// Validate LSP command
	if cfg.lspCommand == "" {
		return nil, fmt.Errorf("LSP command is required")
	}

	if _, err := exec.LookPath(cfg.lspCommand); err != nil {
		return nil, fmt.Errorf("LSP command not found: %s", cfg.lspCommand)
	}

	return cfg, nil
}

func newServer(config *config) (*mcpServer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpServer{
		config:     *config,
		ctx:        ctx,
		cancelFunc: cancel,
	}, nil
}

func (s *mcpServer) initializeLSP() error {
	if err := os.Chdir(s.config.workspaceDir); err != nil {
		return fmt.Errorf("failed to change to workspace directory: %v", err)
	}

	s.lifecycleMu.Lock()
	if s.closing {
		s.lifecycleMu.Unlock()
		return context.Canceled
	}
	client, err := lsp.NewClient(s.config.lspCommand, s.config.lspArgs...)
	if err != nil {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("failed to create LSP client: %v", err)
	}
	s.lspClient = client
	s.lifecycleMu.Unlock()
	watcherConfig := watcher.DefaultWatcherConfig()
	if s.config.projectSettle > 0 {
		watcherConfig.ProjectSettleTime = s.config.projectSettle
	}
	s.workspaceWatcher = watcher.NewWorkspaceWatcherWithConfig(client, watcherConfig)

	initResult, err := client.InitializeLSPClient(s.ctx, s.config.workspaceDir)
	if err != nil {
		return fmt.Errorf("initialize failed: %v", err)
	}

	coreLogger.Debug("Server capabilities: %+v", initResult.Capabilities)

	go s.workspaceWatcher.WatchWorkspace(s.ctx, s.config.workspaceDir)
	return client.WaitForServerReady(s.ctx)
}

func (s *mcpServer) start() error {
	if err := s.initializeLSP(); err != nil {
		return err
	}

	s.mcpServer = server.NewMCPServer(
		"MCP Language Server",
		"v0.0.2",
		server.WithRecovery(),
	)

	err := s.registerTools()
	if err != nil {
		return fmt.Errorf("tool registration failed: %v", err)
	}

	return server.ServeStdio(s.mcpServer)
}

func main() {
	coreLogger.Info("MCP Language Server starting")
	cfg, err := parseConfig()
	if err != nil {
		coreLogger.Fatal("%v", err)
	}
	s, err := newServer(cfg)
	if err != nil {
		coreLogger.Fatal("%v", err)
	}
	if err := containChildren(); err != nil {
		coreLogger.Warn("Language server processes may outlive the bridge: %v", err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := runServer(s, signals); err != nil {
		coreLogger.Error("Server error: %v", err)
		os.Exit(1)
	}
}

// Keep the main goroutine able to handle shutdown even if initialization or
// ServeStdio is blocked. EOF, signals and parent death converge on one cleanup.
func runServer(s *mcpServer, signals <-chan os.Signal) error {
	startDone := make(chan error, 1)
	go func() { startDone <- s.start() }()
	var result error
	select {
	case result = <-startDone:
	case <-signals:
	case <-watchParent():
		coreLogger.Info("Parent process exited; shutting down")
	}
	s.cleanup()
	if errors.Is(result, context.Canceled) {
		return nil
	}
	return result
}

func (s *mcpServer) cleanup() {
	s.cleanupOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closing = true
		client := s.lspClient
		s.lifecycleMu.Unlock()
		s.cancelFunc()
		if client != nil {
			if err := client.Close(); err != nil {
				coreLogger.Debug("LSP cleanup: %v", err)
			}
		}
	})
}
