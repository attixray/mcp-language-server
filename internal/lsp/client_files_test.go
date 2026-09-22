package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bufferWriteCloser struct{ bytes.Buffer }

func (b *bufferWriteCloser) Close() error { return nil }

func TestFileLifecycleUsesCanonicalURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file # percent %.cs")
	if err := os.WriteFile(path, []byte("class Example {}"), 0600); err != nil {
		t.Fatal(err)
	}
	output := &bufferWriteCloser{}
	c := &Client{stdin: output, openFiles: make(map[string]*OpenFileInfo)}
	ctx := context.Background()
	if err := c.OpenFile(ctx, path); err != nil {
		t.Fatal(err)
	}
	if !c.IsFileOpen(path) {
		t.Fatal("file not registered as open")
	}
	if err := c.NotifyChange(ctx, path); err != nil {
		t.Fatal(err)
	}
	c.CloseAllFiles(ctx)
	if c.IsFileOpen(path) {
		t.Fatal("CloseAllFiles did not close escaped path")
	}

	r := bufio.NewReader(bytes.NewReader(output.Bytes()))
	var firstURI string
	for _, method := range []string{"textDocument/didOpen", "textDocument/didChange", "textDocument/didClose"} {
		message, err := ReadMessage(r)
		if err != nil {
			t.Fatal(err)
		}
		if message.Method != method {
			t.Fatalf("method = %q, want %q", message.Method, method)
		}
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			t.Fatal(err)
		}
		uri := params.TextDocument.URI
		parsed, err := url.Parse(uri)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.Fragment != "" {
			t.Fatalf("invalid file URI: %q (%v)", uri, err)
		}
		if strings.Contains(uri, `\`) || !strings.Contains(uri, "%23") || !strings.Contains(uri, "%25") {
			t.Fatalf("path was not encoded as a URI: %q", uri)
		}
		if firstURI == "" {
			firstURI = uri
		} else if uri != firstURI {
			t.Fatalf("lifecycle URI changed: %q != %q", uri, firstURI)
		}
	}
}
