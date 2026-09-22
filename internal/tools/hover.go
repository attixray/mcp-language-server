package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// GetHoverInfo retrieves hover information (type, documentation) for a symbol at the specified position
func GetHoverInfo(ctx context.Context, client *lsp.Client, filePath string, line, column int) (string, error) {
	// Open the file if not already open
	err := client.OpenFile(ctx, filePath)
	if err != nil {
		return "", fmt.Errorf("could not open file: %v", err)
	}

	params := protocol.HoverParams{}

	// Convert 1-indexed line/column to 0-indexed for LSP protocol
	position := protocol.Position{
		Line:      uint32(line - 1),
		Character: uint32(column - 1),
	}
	uri := protocol.URIFromPath(filePath)
	params.TextDocument = protocol.TextDocumentIdentifier{
		URI: uri,
	}
	params.Position = position

	// Execute the hover request
	hoverResult, err := client.Hover(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to get hover information: %v", err)
	}

	var result strings.Builder

	// Process the hover contents based on Markup content
	if hoverResult.Contents.Value == "" {
		// Extract the line where the hover was requested
		content, err := os.ReadFile(filePath)
		lineText := ""
		if err != nil {
			toolsLogger.Warn("failed to extract line at position: %v", err)
		} else {
			lines := strings.Split(string(content), "\n")
			if int(position.Line) < len(lines) {
				lineText = strings.TrimSuffix(lines[position.Line], "\r")
				if int(position.Line) < len(lines)-1 {
					lineText += "\n"
				}
			}
		}
		result.WriteString(fmt.Sprintf("No hover information available for this position on the following line:\n%s", lineText))
	} else {
		result.WriteString(hoverResult.Contents.Value)
	}

	return result.String(), nil
}
