package lsp

import (
	"encoding/json"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/isaacphi/mcp-language-server/internal/utilities"
)

// FileWatchHandler is called when file watchers are registered by the server
type FileWatchHandler func(id string, watchers []protocol.FileSystemWatcher)

// RegisterFileWatchHandler registers a handler for file watcher registrations
func (c *Client) RegisterFileWatchHandler(handler FileWatchHandler) {
	c.fileWatchMu.Lock()
	defer c.fileWatchMu.Unlock()
	c.fileWatchHandler = handler
	for id, watchers := range c.fileWatchRegistrations {
		handler(id, watchers)
	}
}

// Requests

func HandleWorkspaceConfiguration(params json.RawMessage) (any, error) {
	var request protocol.ConfigurationParams
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	result := make([]map[string]any, len(request.Items))
	for i := range result {
		result[i] = map[string]any{}
	}
	return result, nil
}

func (c *Client) HandleRegisterCapability(params json.RawMessage) (any, error) {
	var registerParams protocol.RegistrationParams
	if err := json.Unmarshal(params, &registerParams); err != nil {
		lspLogger.Error("Error unmarshaling registration params: %v", err)
		return nil, err
	}

	for _, reg := range registerParams.Registrations {
		lspLogger.Info("Registration received for method: %s, id: %s", reg.Method, reg.ID)

		// Special handling for file watcher registrations
		if reg.Method == "workspace/didChangeWatchedFiles" {
			// Parse the options into the appropriate type
			var opts protocol.DidChangeWatchedFilesRegistrationOptions
			optJson, err := json.Marshal(reg.RegisterOptions)
			if err != nil {
				lspLogger.Error("Error marshaling registration options: %v", err)
				continue
			}

			err = json.Unmarshal(optJson, &opts)
			if err != nil {
				lspLogger.Error("Error unmarshaling registration options: %v", err)
				continue
			}

			// Notify file watchers
			c.fileWatchMu.Lock()
			if c.fileWatchRegistrations == nil {
				c.fileWatchRegistrations = make(map[string][]protocol.FileSystemWatcher)
			}
			c.fileWatchRegistrations[reg.ID] = opts.Watchers
			if c.fileWatchHandler != nil {
				c.fileWatchHandler(reg.ID, opts.Watchers)
			}
			c.fileWatchMu.Unlock()
		}
	}

	return nil, nil
}

func HandleApplyEdit(params json.RawMessage) (any, error) {
	var workspaceEdit protocol.ApplyWorkspaceEditParams
	if err := json.Unmarshal(params, &workspaceEdit); err != nil {
		return protocol.ApplyWorkspaceEditResult{Applied: false}, err
	}

	// Apply the edits
	err := utilities.ApplyWorkspaceEdit(workspaceEdit.Edit)
	if err != nil {
		lspLogger.Error("Error applying workspace edit: %v", err)
		return protocol.ApplyWorkspaceEditResult{
			Applied:       false,
			FailureReason: workspaceEditFailure(err),
		}, nil
	}

	return protocol.ApplyWorkspaceEditResult{
		Applied: true,
	}, nil
}

func workspaceEditFailure(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Notifications

// HandleServerMessage processes window/showMessage notifications from the server
func HandleServerMessage(params json.RawMessage) {
	var msg protocol.ShowMessageParams
	if err := json.Unmarshal(params, &msg); err != nil {
		lspLogger.Error("Error unmarshaling server message: %v", err)
		return
	}

	// Log the message with appropriate level
	switch msg.Type {
	case protocol.Error:
		lspLogger.Error("Server error: %s", msg.Message)
	case protocol.Warning:
		lspLogger.Warn("Server warning: %s", msg.Message)
	case protocol.Info:
		lspLogger.Info("Server info: %s", msg.Message)
	default:
		lspLogger.Debug("Server message: %s", msg.Message)
	}
}

// HandleDiagnostics processes textDocument/publishDiagnostics notifications
func HandleDiagnostics(client *Client, params json.RawMessage) {
	var diagParams protocol.PublishDiagnosticsParams
	if err := json.Unmarshal(params, &diagParams); err != nil {
		lspLogger.Error("Error unmarshaling diagnostic params: %v", err)
		return
	}

	var metadata struct {
		Version *int32 `json:"version"`
	}
	if err := json.Unmarshal(params, &metadata); err != nil {
		lspLogger.Error("Error unmarshaling diagnostic metadata: %v", err)
		return
	}

	client.openFilesMu.RLock()
	defer client.openFilesMu.RUnlock()
	openFile := client.openFiles[string(diagParams.URI)]
	if openFile == nil {
		return // Diagnostics from a previous open/close cycle are not reusable.
	}
	if metadata.Version != nil && *metadata.Version != openFile.Version {
		return
	}
	// Servers may omit the version. Associate those publications with the
	// synchronized version at receipt; the protocol cannot prove their age.
	version := openFile.Version
	client.storeDiagnostics(diagParams.URI, diagParams.Diagnostics, version)

	lspLogger.Info("Received diagnostics for %s: %d items", diagParams.URI, len(diagParams.Diagnostics))
}
