package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *mcpServer) registerStatusTool() {
	s.mcpServer.AddTool(mcp.NewTool("lsp_status", mcp.WithDescription("Report shared LSP health, outstanding request IDs, generation, restart budget and diagnostic location. Does not restart the LSP.")), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Status must remain available while a tool is stalled or recovery owns the gate.
		s.supervisor.historyMu.Lock()
		attempts := 0
		for _, started := range s.supervisor.restarts {
			if time.Since(started) < s.config.restartWindow {
				attempts++
			}
		}
		s.supervisor.historyMu.Unlock()
		state := map[string]any{
			"broker_pid": os.Getpid(), "workspace": s.config.workspaceDir,
			"generation": s.supervisor.generation.Load(), "poisoned": s.supervisor.poisoned.Load(),
			"request_timeout": s.config.requestTimeout.String(), "restart_limit": s.config.restartLimit,
			"restart_window": s.config.restartWindow.String(), "restarts_in_window": attempts,
			"time": time.Now(), "queued_tools": s.supervisor.queued.Load(), "active_tool": s.supervisor.active.Load(),
		}
		dir, err := brokerStateDir(&s.config)
		if err == nil {
			state["state_directory"] = dir
		}
		if client := s.currentClient(); client != nil {
			if client.Cmd != nil && client.Cmd.Process != nil {
				state["lsp_pid"] = client.Cmd.Process.Pid
			}
			state["requests"] = client.Activities()
			select {
			case <-client.Done():
				state["healthy_transport"] = false
			default:
				state["healthy_transport"] = true
			}
		}
		data, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(data)), nil
	})
}
