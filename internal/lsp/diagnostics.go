package lsp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

type diagnosticState struct {
	Diagnostics []protocol.Diagnostic
	Version     int32
}

func (c *Client) storeDiagnostics(uri protocol.DocumentUri, diagnostics []protocol.Diagnostic, version int32) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	if c.diagnostics == nil {
		c.diagnostics = make(map[protocol.DocumentUri]diagnosticState)
	}
	if c.diagnosticsChanged == nil {
		c.diagnosticsChanged = make(chan struct{})
	}
	state := diagnosticState{
		Diagnostics: append([]protocol.Diagnostic(nil), diagnostics...),
		Version:     version,
	}
	c.diagnostics[uri] = state
	close(c.diagnosticsChanged)
	c.diagnosticsChanged = make(chan struct{})
}

func (c *Client) diagnosticSnapshot(uri protocol.DocumentUri) (diagnosticState, bool, <-chan struct{}) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	if c.diagnosticsChanged == nil {
		c.diagnosticsChanged = make(chan struct{})
	}
	state, ok := c.diagnostics[uri]
	return state, ok, c.diagnosticsChanged
}

// DiagnosticsForFile synchronizes the current file content and returns a
// diagnostic report. Versioned publications are checked against the synchronized
// content; unversioned publications can only be checked by their arrival order.
func (c *Client) DiagnosticsForFile(ctx context.Context, filePath string) ([]protocol.Diagnostic, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uri := protocol.URIFromPath(filePath)
	version, _, err := c.ensureFileOpen(ctx, filePath)
	if err != nil {
		return nil, err
	}

	if c.pullDiagnostics.Load() {
		params := protocol.DocumentDiagnosticParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Identifier:   c.diagnosticIdentifier,
		}
		// Decode the kind explicitly: the generated union decoder accepts an
		// unchanged report as a full report with nil items.
		var report struct {
			Kind  string                `json:"kind"`
			Items []protocol.Diagnostic `json:"items"`
		}
		pullErr := c.Call(ctx, "textDocument/diagnostic", params, &report)
		if pullErr == nil {
			switch report.Kind {
			case "full":
				if report.Items == nil {
					return nil, fmt.Errorf("full diagnostic report is missing items")
				}
				c.openFilesMu.RLock()
				defer c.openFilesMu.RUnlock()
				if current := c.openFiles[string(uri)]; current == nil || current.Version != version {
					return nil, fmt.Errorf("file changed while diagnostics were being computed; retry the request")
				}
				c.storeDiagnostics(uri, report.Items, version)
				return append([]protocol.Diagnostic(nil), report.Items...), nil
			case "unchanged":
				return nil, fmt.Errorf("language server returned unchanged diagnostics without a previous result ID")
			default:
				return nil, fmt.Errorf("language server returned an unsupported diagnostic report kind %q", report.Kind)
			}
		}

		var responseError *ResponseError
		if !errors.As(pullErr, &responseError) || responseError.Code != int(protocol.MethodNotFound) {
			return nil, fmt.Errorf("pull diagnostics failed: %w", pullErr)
		}
		c.pullDiagnostics.Store(false)
	}

	for {
		c.openFilesMu.RLock()
		current := c.openFiles[string(uri)]
		sameVersion := current != nil && current.Version == version
		c.openFilesMu.RUnlock()
		if !sameVersion {
			return nil, fmt.Errorf("file changed while waiting for diagnostics; retry the request")
		}
		state, ok, changedSignal := c.diagnosticSnapshot(uri)
		if ok {
			if state.Version == version {
				return append([]protocol.Diagnostic(nil), state.Diagnostics...), nil
			}
		}

		select {
		case <-changedSignal:
		case <-c.done:
			return nil, fmt.Errorf("LSP connection closed while waiting for diagnostics")
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for diagnostics for %s: %w", filePath, ctx.Err())
		}
	}
}
