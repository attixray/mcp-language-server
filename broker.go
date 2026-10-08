package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

type brokerRequestIDKey struct{}

const brokerProtocol = 1
const maxMCPFrame = 16 * 1024 * 1024

type brokerEndpoint struct {
	Protocol int    `json:"protocol"`
	Key      string `json:"key"`
	PID      int    `json:"pid"`
	Address  string `json:"address"`
	Token    string `json:"token"`
}

func brokerStateDir(c *config) (string, error) {
	if c.brokerDir != "" {
		return filepath.Abs(c.brokerDir)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mcp-language-server", "brokers"), nil
}
func brokerIdentity(c *config) (string, error) {
	workspace, err := filepath.EvalSymlinks(c.workspaceDir)
	if err != nil {
		return "", err
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	command, err := exec.LookPath(c.lspCommand)
	if err != nil {
		return "", err
	}
	command, err = filepath.EvalSymlinks(command)
	if err != nil {
		return "", err
	}
	command, err = filepath.Abs(command)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		workspace = strings.ToLower(workspace)
		command = strings.ToLower(command)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executableInfo, err := os.Stat(executable)
	if err != nil {
		return "", err
	}
	// Environment is part of the effective LSP configuration. Agent/client IDs
	// and shell bookkeeping are excluded; compiler/runtime/cache settings remain.
	env := []string{}
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "CODEX_") || upper == "PWD" || upper == "OLDPWD" || upper == "SHLVL" || upper == "_" {
			continue
		}
		env = append(env, value)
	}
	sort.Strings(env)
	data, err := json.Marshal([]any{brokerProtocol, workspace, command, c.lspArgs, env, executable, executableInfo.Size(), executableInfo.ModTime().UnixNano(), c.projectSettle, c.requestTimeout, c.initTimeout, c.restartLimit, c.restartWindow, c.idleTimeout})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func runShared(c *config) error {
	applyDefaults(c)
	dir, err := brokerStateDir(c)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	key, err := brokerIdentity(c)
	if err != nil {
		return err
	}
	if c.brokerChild {
		return runBroker(c, dir, key)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-watchParent():
			cancel()
		case <-ctx.Done():
		}
	}()
	conn, err := connectBroker(ctx, c, dir, key)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// The adapter owns no LSP and no owner lock. EOF disconnects only this client.
	copied := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		copied <- err
	}()
	go func() { _, err := io.Copy(os.Stdout, conn); copied <- err }()
	select {
	case err := <-copied:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func readEndpoint(path, key string) (*brokerEndpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var endpoint brokerEndpoint
	if err = json.Unmarshal(data, &endpoint); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(endpoint.Address)
	if err != nil || host != "127.0.0.1" || endpoint.Protocol != brokerProtocol || endpoint.Key != key || len(endpoint.Token) != 64 {
		return nil, fmt.Errorf("invalid broker descriptor")
	}
	return &endpoint, nil
}
func dialEndpoint(ctx context.Context, endpoint *brokerEndpoint) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", endpoint.Address)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err = fmt.Fprintln(conn, endpoint.Token); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Read exactly the ACK, preserving subsequent MCP bytes for the proxy.
	ack := make([]byte, 3)
	if _, err = io.ReadFull(conn, ack); err != nil || string(ack) != "OK\n" {
		_ = conn.Close()
		return nil, fmt.Errorf("broker authentication/readiness failed: %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
func connectBroker(parent context.Context, c *config, dir, key string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(parent, c.lockTimeout+c.initTimeout)
	defer cancel()
	descriptor := filepath.Join(dir, key+".json")
	spawned := false
	started := time.Now()
	for {
		if endpoint, err := readEndpoint(descriptor, key); err == nil {
			if conn, err := dialEndpoint(ctx, endpoint); err == nil {
				return conn, nil
			}
		}
		owner, err := tryOwnerLock(filepath.Join(dir, key+".lock"))
		if err != nil {
			return nil, err
		}
		if owner != nil {
			if !spawned {
				// Kernel ownership, never PID age or a stale heartbeat, permits takeover.
				err = spawnBroker(c, dir, key)
				spawned = err == nil
			}
			_ = owner.Close()
			if err != nil {
				return nil, err
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("broker unavailable after %s (owner may be starting or stalled): %w", time.Since(started), ctx.Err())
		case <-timer.C:
		}
	}
}
func spawnBroker(c *config, dir, key string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--broker-child", "--workspace", c.workspaceDir, "--lsp", c.lspCommand, "--broker-dir", dir, "--request-timeout", c.requestTimeout.String(), "--init-timeout", c.initTimeout.String(), "--lock-timeout", c.lockTimeout.String(), "--idle-timeout", c.idleTimeout.String(), "--restart-limit", fmt.Sprint(c.restartLimit), "--restart-window", c.restartWindow.String(), "--project-settle", c.projectSettle.String(), "--"}
	args = append(args, c.lspArgs...)
	logFile, err := os.OpenFile(filepath.Join(dir, key+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(executable, args...)
	configureBrokerProcess(cmd)
	cmd.Stderr = logFile
	if err = cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

type brokerSession struct {
	id            string
	notifications chan mcp.JSONRPCNotification
	initialized   atomic.Bool
}

func (s *brokerSession) SessionID() string                                   { return s.id }
func (s *brokerSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return s.notifications }
func (s *brokerSession) Initialize()                                         { s.initialized.Store(true) }
func (s *brokerSession) Initialized() bool                                   { return s.initialized.Load() }

// Each connection has a distinct MCP session and request-ID/cancellation space.
// The mcp-go stdio transport has a global singleton session, so it cannot safely
// be instantiated once per agent. Only its MCPServer message dispatcher is shared.
func serveBrokerConnection(parent context.Context, conn net.Conn, s *mcpServer, token string, id uint64) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	authBytes, err := reader.ReadSlice('\n')
	auth := string(authBytes)
	if err != nil || len(auth) != 65 || subtle.ConstantTimeCompare([]byte(strings.TrimSuffix(auth, "\n")), []byte(token)) != 1 {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	session := &brokerSession{id: fmt.Sprintf("broker-%d", id), notifications: make(chan mcp.JSONRPCNotification, 32)}
	if err = s.mcpServer.RegisterSession(ctx, session); err != nil {
		return
	}
	defer s.mcpServer.UnregisterSession(ctx, session.id)
	ctx = s.mcpServer.WithContext(ctx, session)
	var writeMu sync.Mutex
	write := func(message any) error {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write(append(data, '\n'))
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(conn, "OK\n"); err != nil {
		return
	}
	var requestsMu sync.Mutex
	requests := map[string]context.CancelFunc{}
	slots := make(chan struct{}, 16)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case n := <-session.notifications:
				if write(n) != nil {
					cancel()
					_ = conn.Close()
					return
				}
			}
		}
	}()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxMCPFrame)
	for scanner.Scan() {
		raw := append(json.RawMessage(nil), scanner.Bytes()...)
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				RequestID json.RawMessage `json:"requestId"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if envelope.Method == "notifications/cancelled" {
			requestsMu.Lock()
			stop := requests[string(envelope.Params.RequestID)]
			requestsMu.Unlock()
			if stop != nil {
				stop()
			}
			continue
		}
		requestID := string(envelope.ID)
		// Protocol notifications are cheap and remain ordered on this session.
		if requestID == "" {
			if response := s.mcpServer.HandleMessage(ctx, raw); response != nil {
				if write(response) != nil {
					return
				}
			}
			continue
		}
		requestCtx, stop := context.WithTimeout(ctx, s.config.requestTimeout)
		requestCtx = context.WithValue(requestCtx, brokerRequestIDKey{}, requestID)
		requestsMu.Lock()
		_, duplicate := requests[requestID]
		if !duplicate {
			requests[requestID] = stop
		}
		requestsMu.Unlock()
		if duplicate {
			stop()
			_ = write(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "error": map[string]any{"code": -32600, "message": "duplicate in-flight request ID"}})
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			stop()
			requestsMu.Lock()
			delete(requests, requestID)
			requestsMu.Unlock()
			_ = write(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "error": map[string]any{"code": -32000, "message": "broker session queue is full"}})
			continue
		}
		go func(raw json.RawMessage, requestID string, requestCtx context.Context, stop context.CancelFunc) {
			defer func() { stop(); requestsMu.Lock(); delete(requests, requestID); requestsMu.Unlock(); <-slots }()
			if response := s.mcpServer.HandleMessage(requestCtx, raw); response != nil {
				if write(response) != nil {
					cancel()
					_ = conn.Close()
				}
			}
		}(raw, requestID, requestCtx, stop)
	}
	if err := scanner.Err(); err != nil {
		coreLogger.Warn("Broker session %s read: %v", session.id, err)
	}
}

func runBroker(c *config, dir, key string) error {
	lockCtx, cancelLock := context.WithTimeout(context.Background(), c.lockTimeout)
	defer cancelLock()
	var owner *os.File
	for owner == nil {
		var err error
		owner, err = tryOwnerLock(filepath.Join(dir, key+".lock"))
		if err != nil {
			return err
		}
		if owner != nil {
			break
		}
		// Another broker won the startup race. Never spawn an LSP as a follower.
		if _, err = readEndpoint(filepath.Join(dir, key+".json"), key); err == nil {
			return nil
		}
		select {
		case <-lockCtx.Done():
			return fmt.Errorf("broker owner lock wait: %w", lockCtx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() { _ = owner.Close() }()
	descriptor := filepath.Join(dir, key+".json")
	_ = os.Remove(descriptor)
	if err := containChildren(); err != nil {
		return fmt.Errorf("cannot contain broker children: %w", err)
	}
	s, err := newServer(c)
	if err != nil {
		return err
	}
	defer s.cleanup()
	signals, stopSignals := signal.NotifyContext(s.ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		select {
		case <-signals.Done():
			s.cancelFunc()
		case <-s.ctx.Done():
		}
	}()
	if err = s.prepare(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	go func() { <-s.ctx.Done(); _ = listener.Close() }()
	token, err := randomToken()
	if err != nil {
		return err
	}
	endpoint := brokerEndpoint{brokerProtocol, key, os.Getpid(), listener.Addr().String(), token}
	data, _ := json.Marshal(endpoint)
	temp, err := os.CreateTemp(dir, key+"-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempName, descriptor); err != nil {
		return err
	}
	defer func() { _ = os.Remove(descriptor) }()
	coreLogger.Info("Shared broker pid=%d workspace=%s", os.Getpid(), c.workspaceDir)
	var clients atomic.Int64
	var sequence atomic.Uint64
	var lastUsed atomic.Int64
	lastUsed.Store(time.Now().UnixNano())
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if clients.Load() == 0 && time.Since(time.Unix(0, lastUsed.Load())) >= c.idleTimeout {
					s.cancelFunc()
					_ = listener.Close()
					return
				}
			}
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if clients.Add(1) > 128 {
			clients.Add(-1)
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { clients.Add(-1); lastUsed.Store(time.Now().UnixNano()) }()
			serveBrokerConnection(s.ctx, conn, s, token, sequence.Add(1))
		}()
	}
}
func pruneDiagnostics(files []string, count int) {
	sort.Slice(files, func(i, j int) bool {
		a, e1 := os.Stat(files[i])
		b, e2 := os.Stat(files[j])
		if e1 != nil || e2 != nil {
			return files[i] < files[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	for _, file := range files[:count] {
		_ = os.Remove(file)
		_ = os.Remove(file + ".goroutines.txt")
	}
}
