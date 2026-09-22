// Package internal contains shared helpers for Clangd tests
package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isaacphi/mcp-language-server/integrationtests/tests/common"
)

// GetTestSuite returns a test suite for Clangd language server tests
func GetTestSuite(t *testing.T) *common.TestSuite {
	// Configure Clangd LSP
	repoRoot, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatalf("Failed to get repo root: %v", err)
	}

	config := common.LSPTestConfig{
		Name:             "clangd",
		Command:          "clangd",
		WorkspaceDir:     filepath.Join(repoRoot, "integrationtests/workspaces/clangd"),
		InitializeTimeMs: 2000,
		PrepareWorkspace: func(workspace string) error {
			return relocateCompileCommands(filepath.Join(repoRoot, "integrationtests/workspaces/clangd"), workspace)
		},
	}

	// Create a test suite
	suite := common.NewTestSuite(t, config)

	// Set up the suite
	if err := suite.Setup(); err != nil {
		t.Fatalf("Failed to set up test suite: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		suite.Cleanup()
	})

	return suite
}

// Bear records absolute paths. Relocate them with the copied fixture so clangd
// never indexes the shared template alongside the files opened by this test.
func relocateCompileCommands(source, workspace string) error {
	name := filepath.Join(workspace, "compile_commands.json")
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	for _, entry := range entries {
		for key, value := range entry {
			switch value := value.(type) {
			case string:
				entry[key] = strings.ReplaceAll(value, source, workspace)
			case []any:
				for i, argument := range value {
					if argument, ok := argument.(string); ok {
						value[i] = strings.ReplaceAll(argument, source, workspace)
					}
				}
			}
		}
	}
	data, err = json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, data, 0644)
}
