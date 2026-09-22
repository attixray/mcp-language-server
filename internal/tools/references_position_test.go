package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func TestReferencesPositionProtocol(t *testing.T) {
	t.Setenv("LSP_CONTEXT_LINES", "0")
	path := filepath.Join(t.TempDir(), "example # %.cs")
	if err := os.WriteFile(path, []byte("// header\n  Example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"position", "name"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("REFERENCES_HELPER_MODE", mode)
			t.Setenv("REFERENCES_HELPER_PATH", path)
			client, err := lsp.NewClient(os.Args[0], "-test.run=^TestReferencesHelperProcess$")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			var got string
			if mode == "position" {
				got, err = FindReferencesAt(context.Background(), client, path, 2, 3)
			} else {
				got, err = FindReferences(context.Background(), client, "Example")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "References in File: 1") || !strings.Contains(got, "At: L2:C3") || !strings.Contains(got, "  Example") {
				t.Fatalf("unexpected reference output: %s", got)
			}
			if mode == "position" {
				_, err = FindReferencesAt(context.Background(), client, path+".missing", 1, 1)
				if err == nil || !strings.Contains(err.Error(), "could not open") {
					t.Fatalf("missing file must fail visibly: %v", err)
				}
			}
		})
	}
}

// A real stdio subprocess tests the wire contract without another language server.
func TestReferencesHelperProcess(t *testing.T) {
	mode := os.Getenv("REFERENCES_HELPER_MODE")
	if mode == "" {
		return
	}
	uri := protocol.URIFromPath(os.Getenv("REFERENCES_HELPER_PATH"))
	location := protocol.Location{URI: uri, Range: protocol.Range{
		Start: protocol.Position{Line: 1, Character: 2},
		End:   protocol.Position{Line: 1, Character: 9},
	}}
	reader := bufio.NewReader(os.Stdin)
	opened := false
	for {
		message, err := lsp.ReadMessage(reader)
		if err != nil {
			os.Exit(0)
		}
		if message.Method == "textDocument/didOpen" {
			var params protocol.DidOpenTextDocumentParams
			if err := json.Unmarshal(message.Params, &params); err == nil {
				opened = params.TextDocument.URI == uri
			}
			continue
		}
		if message.ID == nil || message.ID.Value == nil {
			continue
		}
		response := &lsp.Message{JSONRPC: "2.0", ID: message.ID}
		switch message.Method {
		case "workspace/symbol":
			if mode != "name" {
				response.Error = &lsp.ResponseError{Code: -32600, Message: "position lookup must not search by name"}
			} else {
				response.Result, err = json.Marshal([]protocol.SymbolInformation{{Name: "Example", Kind: 5, Location: location}})
			}
		case "textDocument/references":
			var params protocol.ReferenceParams
			if err = json.Unmarshal(message.Params, &params); err != nil || !opened || params.TextDocument.URI != uri || params.Position != location.Range.Start || params.Context.IncludeDeclaration {
				response.Error = &lsp.ResponseError{Code: -32602, Message: "incorrect URI, position, declaration flag or didOpen"}
			} else {
				response.Result, err = json.Marshal([]protocol.Location{location})
			}
		default:
			response.Error = &lsp.ResponseError{Code: -32601, Message: "unexpected request: " + message.Method}
		}
		if err != nil {
			response.Error = &lsp.ResponseError{Code: -32603, Message: err.Error()}
		}
		if err := lsp.WriteMessage(os.Stdout, response); err != nil {
			os.Exit(1)
		}
	}
}
