package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShutdownHelper(t *testing.T) {
	mode := os.Getenv("LSP_SHUTDOWN_HELPER")
	if mode == "" {
		return
	}
	if mode == "blocked" {
		for {
			time.Sleep(time.Second)
		}
	}
	if _, err := os.Stderr.WriteString(strings.Repeat("x", 256*1024) + "\n"); err != nil {
		os.Exit(2)
	}
	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := ReadMessage(r)
		if err != nil || msg.Method == "exit" {
			os.Exit(0)
		}
		if msg.ID != nil {
			if err := WriteMessage(os.Stdout, &Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`null`)}); err != nil {
				os.Exit(3)
			}
		}
	}
}

func TestLongStderrDoesNotBlockRPC(t *testing.T) {
	t.Setenv("LSP_SHUTDOWN_HELPER", "stderr")
	c, err := NewClient(os.Args[0], "-test.run=^TestShutdownHelper$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCloseUnblocksFullPipe(t *testing.T) {
	t.Setenv("LSP_SHUTDOWN_HELPER", "blocked")
	c, err := NewClient(os.Args[0], "-test.run=^TestShutdownHelper$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	written := make(chan struct{})
	go func() {
		defer close(written)
		_ = c.Notify(context.Background(), "large", strings.Repeat("x", 2*1024*1024))
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Close() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Close blocked on a full stdin pipe")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("writer was not released")
	}
	if c.Cmd.ProcessState == nil {
		t.Fatal("child was not reaped")
	}
}
