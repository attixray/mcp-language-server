package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const brokerUnavailableCode = -32001
const maxAdapterPending = 128
const adapterWriteTimeout = 5 * time.Second
const adapterFailureGrace = 2 * time.Second

// Only received client requests are tracked; notifications and responses to
// server requests do not need synthetic responses. IDs are normalized for
// matching, but their original JSON is preserved in errors (including strings).
type adapterRequest struct {
	ID   json.RawMessage
	sent bool
}
type adapterRequests struct {
	mu      sync.Mutex
	pending map[string]*adapterRequest
	stopped bool
}
type adapterEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func adapterID(raw json.RawMessage) string {
	var id any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&id) != nil {
		return ""
	}
	switch id := id.(type) {
	case string:
		return "s:" + id
	case json.Number:
		text := string(id)
		// Normalize equivalent spellings such as 1.0 and 1 without float64 rounding.
		// Bound rational work for pathological exponent/number sizes. The broker
		// echoes raw IDs, so exact raw keys remain usable outside this budget.
		bounded := len(text) <= 1024
		if exponent := strings.IndexAny(text, "eE"); bounded && exponent >= 0 {
			value, err := strconv.Atoi(text[exponent+1:])
			bounded = err == nil && value >= -1024 && value <= 1024
		}
		if bounded {
			if number, ok := new(big.Rat).SetString(text); ok {
				return "n:" + number.RatString()
			}
		}
		return "raw-number:" + text
	default:
		return ""
	}
}

func (r *adapterRequests) receive(raw []byte) (string, string, bool) {
	var envelope adapterEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Method == "" {
		return "", "", true
	}
	key := adapterID(envelope.ID)
	if key == "" {
		return "", "", true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return "", "", false
	}
	if _, exists := r.pending[key]; exists {
		return key, "duplicate in-flight request ID", true
	}
	if len(r.pending) >= maxAdapterPending {
		return key, "MCP adapter request limit reached", true
	}
	r.pending[key] = &adapterRequest{ID: append(json.RawMessage(nil), envelope.ID...)}
	return key, "", true
}
func (r *adapterRequests) sent(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if request := r.pending[key]; request != nil {
		request.sent = true
	}
}
func (r *adapterRequests) complete(raw []byte) {
	var envelope adapterEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Method != "" || (envelope.Result == nil && envelope.Error == nil) {
		return
	}
	r.mu.Lock()
	delete(r.pending, adapterID(envelope.ID))
	r.mu.Unlock()
}
func (r *adapterRequests) stop() []*adapterRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	pending := make([]*adapterRequest, 0, len(r.pending))
	for _, request := range r.pending {
		copy := *request
		pending = append(pending, &copy)
	}
	clear(r.pending)
	return pending
}

type adapterFrame struct {
	raw       []byte
	key       string
	rejection string
	err       error
}

func readAdapterFrames(ctx context.Context, input io.Reader, requests *adapterRequests) <-chan adapterFrame {
	frames := make(chan adapterFrame, 1)
	go func() {
		send := func(frame adapterFrame) bool {
			select {
			case frames <- frame:
				return true
			case <-ctx.Done():
				return false
			}
		}
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), maxMCPFrame)
		for scanner.Scan() {
			frame := adapterFrame{raw: append([]byte(nil), scanner.Bytes()...)}
			if requests != nil {
				var accepted bool
				frame.key, frame.rejection, accepted = requests.receive(frame.raw)
				if !accepted {
					return
				}
			} else if !json.Valid(frame.raw) {
				send(adapterFrame{err: fmt.Errorf("broker sent an invalid or truncated JSON frame")})
				return
			}
			if !send(frame) {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		send(adapterFrame{err: err})
	}()
	return frames
}

// Closing a pipe is the backstop for a blocked stdout write. All writes are
// serialized by the routing loop, so broker frames and local errors cannot mix.
func writeAdapterFrame(output io.WriteCloser, raw []byte) error {
	timer := time.AfterFunc(adapterWriteTimeout, func() { _ = output.Close() })
	defer timer.Stop()
	frame := append(append([]byte(nil), raw...), '\n')
	n, err := output.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return err
}

type adapterRPCError struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Error   adapterErrorBody `json:"error"`
}
type adapterErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func adapterErrorFrame(id json.RawMessage, code int, message string, data any) []byte {
	raw, _ := json.Marshal(adapterRPCError{"2.0", id, adapterErrorBody{code, message, data}})
	return raw
}

type adapterFailure struct {
	reason  string
	cause   error
	logPath string
}

func (f *adapterFailure) Error() string {
	if f.reason == "output_failed" {
		return fmt.Sprintf("MCP client output unavailable: %v; adapter disconnected. Verify file state before retrying pending mutations. Broker log: %s", f.cause, f.logPath)
	}
	return fmt.Sprintf("shared LSP broker unavailable (%s): %v; restart the MCP adapter to reconnect. Pending requests were not replayed; verify file state before retrying mutations. Broker log: %s", f.reason, f.cause, f.logPath)
}
func (f *adapterFailure) Unwrap() error { return f.cause }
func failAdapter(requests *adapterRequests, output io.WriteCloser, reason string, cause error, logPath string) error {
	failure := &adapterFailure{reason, cause, logPath}
	pending := requests.stop()
	// Bound the whole failure flush, rather than spending five seconds on each
	// request if the MCP client stopped reading stdout.
	timer := time.AfterFunc(adapterFailureGrace, func() { _ = output.Close() })
	defer timer.Stop()
	for _, request := range pending {
		message := "Shared LSP broker unavailable; this request was not sent. Restart the MCP adapter to reconnect."
		if request.sent {
			message = "Shared LSP broker connection lost; the request outcome is unknown. The request was not replayed. Restart the MCP adapter to reconnect and verify file state before retrying mutations."
		}
		raw := adapterErrorFrame(request.ID, brokerUnavailableCode, message, map[string]any{"reason": reason, "outcome_unknown": request.sent, "request_replayed": false, "broker_log": logPath})
		if err := writeAdapterFrame(output, raw); err != nil {
			failure.cause = errors.Join(cause, fmt.Errorf("cannot deliver MCP error: %w", err))
			break
		}
	}
	return failure
}

// The adapter owns neither an LSP nor the owner lock. It frames the stdio/TCP
// proxy so a broker failure can be returned to outstanding MCP request IDs.
func runAdapter(parent context.Context, c *config, dir, key string, input io.ReadCloser, output io.WriteCloser) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() { _ = input.Close(); _ = output.Close() }()
	requests := &adapterRequests{pending: make(map[string]*adapterRequest)}
	inputFrames := readAdapterFrames(ctx, input, requests)
	type connected struct {
		conn net.Conn
		err  error
	}
	connections := make(chan connected)
	go func() {
		conn, err := connectBroker(ctx, c, dir, key)
		select {
		case connections <- connected{conn, err}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	var conn net.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	var brokerFrames <-chan adapterFrame
	queued := []adapterFrame{}
	queuedBytes := 0
	logPath := filepath.Join(dir, key+".log")
	fail := func(reason string, cause error) error {
		if ctx.Err() != nil {
			return nil
		} // Parent shutdown is not broker failure.
		if conn != nil {
			_ = conn.Close()
		}
		return failAdapter(requests, output, reason, cause, logPath)
	}
	forward := func(frame adapterFrame) error {
		requests.sent(frame.key) // A failed/partial write can have an unknown outcome.
		_ = conn.SetWriteDeadline(time.Now().Add(adapterWriteTimeout))
		wire := append(append([]byte(nil), frame.raw...), '\n')
		n, err := conn.Write(wire)
		if err == nil && n != len(wire) {
			err = io.ErrShortWrite
		}
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil // The adapter's client/parent shut down.
		case connected := <-connections:
			if connected.err != nil {
				return fail("startup_failed", connected.err)
			}
			conn = connected.conn
			connections = nil
			brokerFrames = readAdapterFrames(ctx, conn, nil)
			for _, frame := range queued {
				if err := forward(frame); err != nil {
					return fail("write_failed", err)
				}
			}
			queued = nil
		case frame := <-inputFrames:
			if frame.err != nil {
				if errors.Is(frame.err, io.EOF) {
					return nil
				}
				return fmt.Errorf("MCP input read failed: %w", frame.err)
			}
			if frame.rejection != "" {
				var envelope adapterEnvelope
				_ = json.Unmarshal(frame.raw, &envelope)
				if err := writeAdapterFrame(output, adapterErrorFrame(envelope.ID, -32600, frame.rejection, nil)); err != nil {
					return &adapterFailure{"output_failed", err, logPath}
				}
				continue
			}
			if conn == nil {
				if len(queued) >= maxAdapterPending || queuedBytes+len(frame.raw) > maxMCPFrame {
					return fail("startup_queue_full", fmt.Errorf("MCP startup queue limit reached"))
				}
				queued = append(queued, frame)
				queuedBytes += len(frame.raw)
			} else if err := forward(frame); err != nil {
				return fail("write_failed", err)
			}
		case frame := <-brokerFrames:
			if frame.err != nil {
				return fail("connection_lost", frame.err)
			}
			// Remove before delivery so a client can immediately reuse its completed
			// request ID when it reads the response. Output failure closes this stream.
			requests.complete(frame.raw)
			if err := writeAdapterFrame(output, frame.raw); err != nil {
				return &adapterFailure{"output_failed", err, logPath}
			}
		}
	}
}
