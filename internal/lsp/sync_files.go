package lsp

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// SyncOpenFiles refreshes every open buffer before a workspace-wide mutation.
// It returns the synchronized content for checking edits planned by the server.
func (c *Client) SyncOpenFiles(ctx context.Context) (map[protocol.DocumentUri]string, error) {
	c.openFilesMu.RLock()
	files := make([]string, 0, len(c.openFiles))
	for uri := range c.openFiles {
		files = append(files, uri)
	}
	c.openFilesMu.RUnlock()
	sort.Strings(files)
	snapshot := make(map[protocol.DocumentUri]string, len(files))
	for _, name := range files {
		uri := protocol.DocumentUri(name)
		if err := c.OpenFile(ctx, uri.Path()); err != nil {
			return nil, fmt.Errorf("cannot synchronize %s before rename: %w", uri, err)
		}
		c.openFilesMu.RLock()
		info := c.openFiles[name]
		if info != nil {
			snapshot[uri] = info.Text
		}
		c.openFilesMu.RUnlock()
		if info == nil {
			return nil, fmt.Errorf("document closed while preparing rename: %s", uri)
		}
	}
	return snapshot, nil
}

// ValidateFileSnapshot rejects disk changes made while the server planned edits.
// This is a preflight, not an atomic transaction with external filesystem writers.
func ValidateFileSnapshot(snapshot map[protocol.DocumentUri]string) error {
	for uri, expected := range snapshot {
		content, err := os.ReadFile(uri.Path())
		if err != nil {
			return fmt.Errorf("cannot verify rename input %s: %w", uri, err)
		}
		if string(content) != expected {
			return fmt.Errorf("file changed while planning rename: %s; retry the operation", uri)
		}
	}
	return nil
}
