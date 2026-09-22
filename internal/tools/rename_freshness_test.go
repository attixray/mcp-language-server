package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func TestRenameRefreshesReferencesBeforeApplyingEdits(t *testing.T) {
	for _, mode := range []string{"rename", "changed-during-request"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			declaration := filepath.Join(dir, "declaration.cs")
			reference := filepath.Join(dir, "reference # %.cs")
			for _, file := range []string{declaration, reference} {
				if err := os.WriteFile(file, []byte("Name\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("RENAME_FRESHNESS_HELPER", mode)
			t.Setenv("RENAME_FRESHNESS_REFERENCE", reference)
			c, err := lsp.NewClient(os.Args[0], "-test.run=^TestRenameFreshnessHelper$")
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
			if err := c.OpenFile(ctx, reference); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reference, []byte("// shifted\nName\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = RenameSymbol(ctx, c, declaration, 1, 1, "NewName")
			want := "// shifted\nNewName\n"
			if mode == "changed-during-request" {
				want = "concurrent edit\n"
				if err == nil || !strings.Contains(err.Error(), "changed while planning") {
					t.Fatalf("did not reject changed input: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, readErr := os.ReadFile(reference)
			if readErr != nil || string(got) != want {
				t.Fatalf("unexpected file: %q (%v)", got, readErr)
			}
		})
	}
}

func TestRenameFreshnessHelper(t *testing.T) {
	mode := os.Getenv("RENAME_FRESHNESS_HELPER")
	if mode == "" {
		return
	}
	file := os.Getenv("RENAME_FRESHNESS_REFERENCE")
	uri := protocol.URIFromPath(file)
	texts := map[protocol.DocumentUri]string{}
	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := lsp.ReadMessage(r)
		if err != nil || msg.Method == "exit" {
			os.Exit(0)
		}
		switch msg.Method {
		case "textDocument/didOpen":
			var p protocol.DidOpenTextDocumentParams
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				os.Exit(2)
			}
			texts[p.TextDocument.URI] = p.TextDocument.Text
		case "textDocument/didChange":
			var p struct {
				TextDocument   protocol.VersionedTextDocumentIdentifier `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				os.Exit(2)
			}
			texts[p.TextDocument.URI] = p.ContentChanges[0].Text
		}
		if msg.ID == nil {
			continue
		}
		response := &lsp.Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`null`)}
		if msg.Method == "textDocument/rename" {
			if texts[uri] != "// shifted\nName\n" {
				response.Error = &lsp.ResponseError{Code: -32603, Message: "rename used stale reference buffer"}
			} else {
				if mode == "changed-during-request" {
					if err := os.WriteFile(file, []byte("concurrent edit\n"), 0600); err != nil {
						os.Exit(3)
					}
				}
				edit := protocol.WorkspaceEdit{Changes: map[protocol.DocumentUri][]protocol.TextEdit{uri: {{Range: protocol.Range{Start: protocol.Position{Line: 1}, End: protocol.Position{Line: 1, Character: 4}}, NewText: "NewName"}}}}
				response.Result, err = json.Marshal(edit)
				if err != nil {
					os.Exit(4)
				}
			}
		}
		if err := lsp.WriteMessage(os.Stdout, response); err != nil {
			os.Exit(5)
		}
	}
}
