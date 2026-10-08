package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDeadlineInterruptsBlockedPipeAndWaitingWriter(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	c := &Client{stdin: writer, done: make(chan struct{})}
	c.SetRequestTimeout(100 * time.Millisecond)
	result := make(chan error, 2)
	go func() { result <- c.Notify(context.Background(), "large", strings.Repeat("x", 1024)) }()
	// Wait until the first writer owns the gate. The pipe has no reader.
	deadline := time.Now().Add(time.Second)
	for len(c.Activities()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	go func() { result <- c.Notify(ctx, "queued", nil) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("blocked notification succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("pipe write or write-gate wait ignored deadline")
		}
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("partial-frame transport was not closed")
	}
}
func TestTransportEOFReleasesEveryPendingCall(t *testing.T) {
	r, w := io.Pipe()
	c := &Client{stdin: nopWriteCloser{Writer: io.Discard}, stdout: bufio.NewReader(r), done: make(chan struct{}), handlers: make(map[string]chan *Message)}
	go c.handleMessages()
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { results <- c.Call(context.Background(), "test/wait", nil, nil) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		c.handlersMu.RLock()
		n := len(c.handlers)
		c.handlersMu.RUnlock()
		if n == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("calls never reached response wait")
		}
		time.Sleep(time.Millisecond)
	}
	_ = w.Close()
	for i := 0; i < 8; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("call succeeded after EOF")
			}
		case <-time.After(time.Second):
			t.Fatal("pending call remained blocked after EOF")
		}
	}
	_ = r.Close()
}
func TestResponseDeadlineAbortsUnresponsiveLSP(t *testing.T) {
	c := &Client{stdin: nopWriteCloser{Writer: io.Discard}, done: make(chan struct{}), handlers: make(map[string]chan *Message)}
	c.SetRequestTimeout(20 * time.Millisecond)
	if err := c.Call(context.Background(), "test/silent", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("silent transport remained healthy")
	}
}

func TestCancellationDuringFrameWritePreservesTransport(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	c := &Client{stdin: writer, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- c.Notify(ctx, "test/first", map[string]string{"value": "content"}) }()
	// Consume the header, then cancel while the body write is blocked. The
	// writer must complete the frame when the peer resumes reading.
	buffered := bufio.NewReader(reader)
	header, err := buffered.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var size int
	if _, err = fmt.Sscanf(header, "Content-Length: %d", &size); err != nil {
		t.Fatal(err)
	}
	if blank, err := buffered.ReadString('\n'); err != nil || blank != "\r\n" {
		t.Fatalf("header: %q %v", blank, err)
	}
	cancel()
	select {
	case <-c.Done():
		t.Fatal("session cancellation aborted in-progress frame")
	case <-time.After(50 * time.Millisecond):
	}
	body := make([]byte, size)
	if _, err = io.ReadFull(buffered, body); err != nil {
		t.Fatal(err)
	}
	var message Message
	if err = json.Unmarshal(body, &message); err != nil || message.Method != "test/first" {
		t.Fatalf("partial frame after cancellation: %s %v", body, err)
	}
	select {
	case err = <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not finish frame")
	}
	go func() { completed <- c.Notify(context.Background(), "test/second", nil) }()
	next, err := ReadMessage(buffered)
	if err != nil || next.Method != "test/second" {
		t.Fatalf("shared stream unusable after cancellation: %v %v", next, err)
	}
	select {
	case err = <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second writer stuck")
	}
	select {
	case <-c.Done():
		t.Fatal("healthy shared transport closed")
	default:
	}
}
