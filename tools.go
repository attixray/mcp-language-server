package main

import (
	"context"
	"fmt"
	"math"

	"github.com/isaacphi/mcp-language-server/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
)

func (s *mcpServer) registerTools() error {
	coreLogger.Debug("Registering MCP tools")

	applyTextEditTool := mcp.NewTool("edit_file",
		mcp.WithDescription("Apply multiple text edits to a file."),
		mcp.WithArray("edits",
			mcp.Required(),
			mcp.Description("List of edits to apply"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"startLine": map[string]any{
						"type":        "number",
						"description": "Start line to replace, inclusive, one-indexed",
					},
					"endLine": map[string]any{
						"type":        "number",
						"description": "End line to replace, inclusive, one-indexed",
					},
					"newText": map[string]any{
						"type":        "string",
						"description": "Replacement text. Replace with the new text. Leave blank to remove lines.",
					},
				},
				"required": []string{"startLine", "endLine"},
			}),
		),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("Path to the file to edit"),
		),
	)

	s.mcpServer.AddTool(applyTextEditTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		filePath, ok := request.Params.Arguments["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		// Extract edits array
		editsArg, ok := request.Params.Arguments["edits"]
		if !ok {
			return mcp.NewToolResultError("edits is required"), nil
		}

		// Type assert and convert the edits
		editsArray, ok := editsArg.([]any)
		if !ok {
			return mcp.NewToolResultError("edits must be an array"), nil
		}

		var edits []tools.TextEdit
		for _, editItem := range editsArray {
			editMap, ok := editItem.(map[string]any)
			if !ok {
				return mcp.NewToolResultError("each edit must be an object"), nil
			}

			startLine, ok := editMap["startLine"].(float64)
			if !ok {
				return mcp.NewToolResultError("startLine must be a number"), nil
			}

			endLine, ok := editMap["endLine"].(float64)
			if !ok {
				return mcp.NewToolResultError("endLine must be a number"), nil
			}

			newText, _ := editMap["newText"].(string) // newText can be empty

			edits = append(edits, tools.TextEdit{
				StartLine: int(startLine),
				EndLine:   int(endLine),
				NewText:   newText,
			})
		}

		coreLogger.Debug("Executing edit_file for file: %s", filePath)
		response, err := tools.ApplyTextEdits(ctx, s.lspClient, filePath, edits)
		if err != nil {
			coreLogger.Error("Failed to apply edits: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to apply edits: %v", err)), nil
		}
		return mcp.NewToolResultText(response), nil
	})

	readDefinitionTool := mcp.NewTool("definition",
		mcp.WithDescription("Read the source code definition of a symbol (function, type, constant, etc.) from the codebase. Returns the complete implementation code where the symbol is defined."),
		mcp.WithString("symbolName",
			mcp.Required(),
			mcp.Description("The name of the symbol whose definition you want to find (e.g. 'mypackage.MyFunction', 'MyType.MyMethod')"),
		),
	)

	s.mcpServer.AddTool(readDefinitionTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		symbolName, ok := request.Params.Arguments["symbolName"].(string)
		if !ok {
			return mcp.NewToolResultError("symbolName must be a string"), nil
		}

		coreLogger.Debug("Executing definition for symbol: %s", symbolName)
		text, err := tools.ReadDefinition(ctx, s.lspClient, symbolName)
		if err != nil {
			coreLogger.Error("Failed to get definition: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get definition: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	findReferencesTool := mcp.NewTool("references",
		mcp.WithDescription("Find all usages and references of a symbol throughout the codebase. Returns a list of all files and locations where the symbol appears."),
		mcp.WithString("symbolName",
			mcp.Required(),
			mcp.Description("The name of the symbol to search for (e.g. 'mypackage.MyFunction', 'MyType')"),
		),
	)

	s.mcpServer.AddTool(findReferencesTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		symbolName, ok := request.Params.Arguments["symbolName"].(string)
		if !ok {
			return mcp.NewToolResultError("symbolName must be a string"), nil
		}

		coreLogger.Debug("Executing references for symbol: %s", symbolName)
		text, err := tools.FindReferences(ctx, s.lspClient, symbolName)
		if err != nil {
			coreLogger.Error("Failed to find references: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to find references: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	findReferencesAtTool := mcp.NewTool("references_at",
		mcp.WithDescription("Find references at a known source position without a workspace-wide name search. Returns the same file locations and context as references. Set LSP_CONTEXT_LINES=0 to return reference lines without container lookups."),
		mcp.WithString("filePath", mcp.Required(), mcp.Description("Path to the source file containing the symbol")),
		mcp.WithNumber("line", mcp.Required(), mcp.Description("1-based line number")),
		mcp.WithNumber("column", mcp.Required(), mcp.Description("1-based column in UTF-16 code units")),
	)
	s.mcpServer.AddTool(findReferencesAtTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, line, column, err := referencePositionArguments(request.Params.Arguments)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		text, err := tools.FindReferencesAt(ctx, s.lspClient, filePath, line, column)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to find references: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	getDiagnosticsTool := mcp.NewTool("diagnostics",
		mcp.WithDescription("Get diagnostic information for a specific file from the language server."),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("The path to the file to get diagnostics for"),
		),
		mcp.WithNumber("contextLines",
			mcp.Description("Lines to include around each diagnostic."),
			mcp.DefaultNumber(5),
		),
		mcp.WithBoolean("showLineNumbers",
			mcp.Description("If true, adds line numbers to the output"),
			mcp.DefaultBool(true),
		),
	)

	s.mcpServer.AddTool(getDiagnosticsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		filePath, ok := request.Params.Arguments["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		contextLines := 5 // default value
		if raw, exists := request.Params.Arguments["contextLines"]; exists {
			var value float64
			switch v := raw.(type) {
			case int:
				value = float64(v)
			case float64:
				value = v
			default:
				return mcp.NewToolResultError("contextLines must be a non-negative integer"), nil
			}
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > math.MaxInt32 || math.Trunc(value) != value {
				return mcp.NewToolResultError("contextLines must be a non-negative integer no greater than 2147483647"), nil
			}
			contextLines = int(value)
		}

		showLineNumbers := true // default value
		if showLineNumbersArg, ok := request.Params.Arguments["showLineNumbers"].(bool); ok {
			showLineNumbers = showLineNumbersArg
		}

		coreLogger.Debug("Executing diagnostics for file: %s", filePath)
		text, err := tools.GetDiagnosticsForFile(ctx, s.lspClient, filePath, contextLines, showLineNumbers)
		if err != nil {
			coreLogger.Error("Failed to get diagnostics: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get diagnostics: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	// Uncomment to add codelens tools
	//
	// getCodeLensTool := mcp.NewTool("get_codelens",
	// 	mcp.WithDescription("Get code lens hints for a given file from the language server."),
	// 	mcp.WithString("filePath",
	// 		mcp.Required(),
	// 		mcp.Description("The path to the file to get code lens information for"),
	// 	),
	// )
	//
	// s.mcpServer.AddTool(getCodeLensTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// 	// Extract arguments
	// 	filePath, ok := request.Params.Arguments["filePath"].(string)
	// 	if !ok {
	// 		return mcp.NewToolResultError("filePath must be a string"), nil
	// 	}
	//
	// 	coreLogger.Debug("Executing get_codelens for file: %s", filePath)
	// 	text, err := tools.GetCodeLens(ctx, s.lspClient, filePath)
	// 	if err != nil {
	// 		coreLogger.Error("Failed to get code lens: %v", err)
	// 		return mcp.NewToolResultError(fmt.Sprintf("failed to get code lens: %v", err)), nil
	// 	}
	// 	return mcp.NewToolResultText(text), nil
	// })
	//
	// executeCodeLensTool := mcp.NewTool("execute_codelens",
	// 	mcp.WithDescription("Execute a code lens command for a given file and lens index."),
	// 	mcp.WithString("filePath",
	// 		mcp.Required(),
	// 		mcp.Description("The path to the file containing the code lens to execute"),
	// 	),
	// 	mcp.WithNumber("index",
	// 		mcp.Required(),
	// 		mcp.Description("The index of the code lens to execute (from get_codelens output), 1 indexed"),
	// 	),
	// )
	//
	// s.mcpServer.AddTool(executeCodeLensTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// 	// Extract arguments
	// 	filePath, ok := request.Params.Arguments["filePath"].(string)
	// 	if !ok {
	// 		return mcp.NewToolResultError("filePath must be a string"), nil
	// 	}
	//
	// 	// Handle both float64 and int for index due to JSON parsing
	// 	var index int
	// 	switch v := request.Params.Arguments["index"].(type) {
	// 	case float64:
	// 		index = int(v)
	// 	case int:
	// 		index = v
	// 	default:
	// 		return mcp.NewToolResultError("index must be a number"), nil
	// 	}
	//
	// 	coreLogger.Debug("Executing execute_codelens for file: %s index: %d", filePath, index)
	// 	text, err := tools.ExecuteCodeLens(ctx, s.lspClient, filePath, index)
	// 	if err != nil {
	// 		coreLogger.Error("Failed to execute code lens: %v", err)
	// 		return mcp.NewToolResultError(fmt.Sprintf("failed to execute code lens: %v", err)), nil
	// 	}
	// 	return mcp.NewToolResultText(text), nil
	// })

	hoverTool := mcp.NewTool("hover",
		mcp.WithDescription("Get hover information (type, documentation) for a symbol at the specified position."),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("The path to the file to get hover information for"),
		),
		mcp.WithNumber("line",
			mcp.Required(),
			mcp.Description("The line number where the hover is requested (1-indexed)"),
		),
		mcp.WithNumber("column",
			mcp.Required(),
			mcp.Description("The column number where the hover is requested (1-indexed)"),
		),
	)

	s.mcpServer.AddTool(hoverTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		filePath, ok := request.Params.Arguments["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		// Handle both float64 and int for line and column due to JSON parsing
		var line, column int
		switch v := request.Params.Arguments["line"].(type) {
		case float64:
			line = int(v)
		case int:
			line = v
		default:
			return mcp.NewToolResultError("line must be a number"), nil
		}

		switch v := request.Params.Arguments["column"].(type) {
		case float64:
			column = int(v)
		case int:
			column = v
		default:
			return mcp.NewToolResultError("column must be a number"), nil
		}

		coreLogger.Debug("Executing hover for file: %s line: %d column: %d", filePath, line, column)
		text, err := tools.GetHoverInfo(ctx, s.lspClient, filePath, line, column)
		if err != nil {
			coreLogger.Error("Failed to get hover information: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get hover information: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	renameSymbolTool := mcp.NewTool("rename_symbol",
		mcp.WithDescription("Rename a symbol (variable, function, class, etc.) at the specified position and update all references throughout the codebase."),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("The path to the file containing the symbol to rename"),
		),
		mcp.WithNumber("line",
			mcp.Required(),
			mcp.Description("The line number where the symbol is located (1-indexed)"),
		),
		mcp.WithNumber("column",
			mcp.Required(),
			mcp.Description("The column number where the symbol is located (1-indexed)"),
		),
		mcp.WithString("newName",
			mcp.Required(),
			mcp.Description("The new name for the symbol"),
		),
	)

	s.mcpServer.AddTool(renameSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments
		filePath, ok := request.Params.Arguments["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		newName, ok := request.Params.Arguments["newName"].(string)
		if !ok {
			return mcp.NewToolResultError("newName must be a string"), nil
		}

		// Handle both float64 and int for line and column due to JSON parsing
		var line, column int
		switch v := request.Params.Arguments["line"].(type) {
		case float64:
			line = int(v)
		case int:
			line = v
		default:
			return mcp.NewToolResultError("line must be a number"), nil
		}

		switch v := request.Params.Arguments["column"].(type) {
		case float64:
			column = int(v)
		case int:
			column = v
		default:
			return mcp.NewToolResultError("column must be a number"), nil
		}

		coreLogger.Debug("Executing rename_symbol for file: %s line: %d column: %d newName: %s", filePath, line, column, newName)
		text, err := tools.RenameSymbol(ctx, s.lspClient, filePath, line, column, newName)
		if err != nil {
			coreLogger.Error("Failed to rename symbol: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to rename symbol: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	coreLogger.Info("Successfully registered all MCP tools")
	return nil
}
