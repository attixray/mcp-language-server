package lsp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func TestCallHonorsContextCancellation(t *testing.T) {
	client := &Client{
		stdin:    nopWriteCloser{Writer: io.Discard},
		handlers: make(map[string]chan *Message),
		done:     make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := client.Call(ctx, "test/wait", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context deadline exceeded", err)
	}
	client.handlersMu.RLock()
	defer client.handlersMu.RUnlock()
	if len(client.handlers) != 0 {
		t.Fatalf("Call() left %d response handlers registered", len(client.handlers))
	}
}

func TestConcurrentNotificationsWriteCompleteFrames(t *testing.T) {
	var output bytes.Buffer
	client := &Client{stdin: nopWriteCloser{Writer: &output}}

	const messageCount = 40
	var wg sync.WaitGroup
	for i := 0; i < messageCount; i++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			if err := client.Notify(context.Background(), "test/notify", map[string]int{"value": value}); err != nil {
				t.Errorf("Notify() error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	for i := 0; i < messageCount; i++ {
		message, err := ReadMessage(reader)
		if err != nil {
			t.Fatalf("ReadMessage(%d) error = %v", i, err)
		}
		if message.Method != "test/notify" {
			t.Fatalf("message %d method = %q", i, message.Method)
		}
	}
	if _, err := reader.Peek(1); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected bytes after %d messages: %v", messageCount, err)
	}
}
