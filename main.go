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
	workspaceDir   string
	lspCommand     string
	lspArgs        []string
	projectSettle  time.Duration
	shared         bool
	brokerChild    bool
	brokerDir      string
	requestTimeout time.Duration
	initTimeout    time.Duration
	lockTimeout    time.Duration
	idleTimeout    time.Duration
	restartLimit   int
	restartWindow  time.Duration
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
	supervisor       *supervisor
	watcherCancel    context.CancelFunc
	watcherDone      chan struct{}
}

func parseConfig() (*config, error) {
	cfg := &config{}
	flag.StringVar(&cfg.workspaceDir, "workspace", "", "Path to workspace directory")
	flag.StringVar(&cfg.lspCommand, "lsp", "", "LSP command to run (args should be passed after --)")
	flag.DurationVar(&cfg.projectSettle, "project-settle", watcher.DefaultWatcherConfig().ProjectSettleTime,
		"How long project and solution files must stay unchanged before their changes are reported")
	flag.BoolVar(&cfg.shared, "shared", true, "Share one supervised LSP per workspace and configuration")
	flag.BoolVar(&cfg.brokerChild, "broker-child", false, "Internal: run the shared broker")
	flag.StringVar(&cfg.brokerDir, "broker-dir", "", "Broker state directory (default: per-user cache)")
	flag.DurationVar(&cfg.requestTimeout, "request-timeout", 60*time.Second, "Total tool deadline, including queue time")
	flag.DurationVar(&cfg.initTimeout, "init-timeout", 120*time.Second, "LSP initialization deadline")
	flag.DurationVar(&cfg.lockTimeout, "lock-timeout", 15*time.Second, "Broker owner lock wait deadline")
	flag.DurationVar(&cfg.idleTimeout, "idle-timeout", 2*time.Minute, "Stop broker after last client disconnects")
	flag.IntVar(&cfg.restartLimit, "restart-limit", 3, "Maximum LSP restarts per window")
	flag.DurationVar(&cfg.restartWindow, "restart-window", 5*time.Minute, "LSP restart accounting window")
	flag.Parse()
	if cfg.requestTimeout <= 0 || cfg.initTimeout <= 0 || cfg.lockTimeout <= 0 || cfg.idleTimeout <= 0 || cfg.restartWindow <= 0 || cfg.restartLimit < 0 {
		return nil, fmt.Errorf("timeouts must be positive and restart-limit must be nonnegative")
	}

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

	command, err := exec.LookPath(cfg.lspCommand)
	if err != nil {
		return nil, fmt.Errorf("LSP command not found: %s", cfg.lspCommand)
	}
	cfg.lspCommand, err = filepath.Abs(command)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve LSP command: %w", err)
	}

	return cfg, nil
}

func newServer(config *config) (*mcpServer, error) {
	applyDefaults(config)
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpServer{
		config:     *config,
		ctx:        ctx,
		cancelFunc: cancel,
		supervisor: newSupervisor(),
	}, nil
}

func (s *mcpServer) initializeLSP() error { return s.initializeLSPContext(s.ctx) }

func (s *mcpServer) initializeLSPContext(parent context.Context) error {

	s.lifecycleMu.Lock()
	if s.closing {
		s.lifecycleMu.Unlock()
		return context.Canceled
	}
	client, err := lsp.NewClientInWorkspace(s.config.workspaceDir, s.config.requestTimeout, s.config.lspCommand, s.config.lspArgs...)
	if err != nil {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("failed to create LSP client: %v", err)
	}
	s.lspClient = client
	generationCtx, generationCancel := context.WithCancel(s.ctx)
	s.watcherCancel = generationCancel
	s.watcherDone = nil
	s.lifecycleMu.Unlock()
	watcherConfig := watcher.DefaultWatcherConfig()
	if s.config.projectSettle > 0 {
		watcherConfig.ProjectSettleTime = s.config.projectSettle
	}
	s.workspaceWatcher = watcher.NewWorkspaceWatcherWithConfig(client, watcherConfig)
	initCtx, initCancel := context.WithTimeout(parent, s.config.initTimeout)
	defer initCancel()

	initResult, err := client.InitializeLSPClient(initCtx, s.config.workspaceDir)
	if err != nil {
		return fmt.Errorf("initialize failed: %v", err)
	}

	coreLogger.Debug("Server capabilities: %+v", initResult.Capabilities)

	if err := client.WaitForServerReady(initCtx); err != nil {
		return err
	}
	for _, path := range s.supervisor.paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if err := client.OpenFile(initCtx, path); err != nil {
			return fmt.Errorf("restore %s: %w", path, err)
		}
	}
	s.lifecycleMu.Lock()
	s.watcherDone = make(chan struct{})
	w, done := s.workspaceWatcher, s.watcherDone
	s.lifecycleMu.Unlock()
	go func() { defer close(done); w.WatchWorkspace(generationCtx, s.config.workspaceDir) }()
	return nil
}

func (s *mcpServer) prepare() error {
	if err := s.initializeLSP(); err != nil {
		return err
	}

	s.mcpServer = server.NewMCPServer(
		"MCP Language Server",
		"v0.0.2",
		server.WithRecovery(),
		server.WithToolHandlerMiddleware(s.supervisedTool),
	)

	err := s.registerTools()
	if err != nil {
		return fmt.Errorf("tool registration failed: %v", err)
	}

	go s.watchLSP()
	return nil
}

func (s *mcpServer) start() error {
	if err := s.prepare(); err != nil {
		return err
	}
	return server.ServeStdio(s.mcpServer)
}

func main() {
	coreLogger.Info("MCP Language Server starting")
	cfg, err := parseConfig()
	if err != nil {
		coreLogger.Fatal("%v", err)
	}
	if err := os.Chdir(cfg.workspaceDir); err != nil {
		coreLogger.Fatal("Cannot enter workspace: %v", err)
	}
	if cfg.shared || cfg.brokerChild {
		if err := runShared(cfg); err != nil {
			var failure *adapterFailure
			if errors.As(err, &failure) {
				// A transport failure must remain visible even with logging disabled
				// or redirected. stdout is reserved for framed MCP messages.
				_, _ = fmt.Fprintln(os.Stderr, "MCP adapter:", failure)
			} else {
				coreLogger.Error("Shared LSP: %v", err)
			}
			os.Exit(1)
		}
		return
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
		watcherCancel := s.watcherCancel
		s.lifecycleMu.Unlock()
		s.cancelFunc()
		if watcherCancel != nil {
			watcherCancel()
		}
		if client != nil {
			if err := client.Close(); err != nil {
				coreLogger.Debug("LSP cleanup: %v", err)
			}
		}
	})
}
