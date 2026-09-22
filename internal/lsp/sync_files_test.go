package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func TestSyncOpenFilesRefreshesOtherDocumentsAndRejectsLaterChanges(t *testing.T) {
	c := diagnosticTestClient(t, func(*Message) []*Message { return nil })
	ctx := context.Background()
	files := []string{filepath.Join(t.TempDir(), "declaration.cs"), filepath.Join(t.TempDir(), "reference # %.cs")}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.OpenFile(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(files[1], []byte("shifted reference"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.SyncOpenFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot[protocol.URIFromPath(files[1])] != "shifted reference" {
		t.Fatal("referencing file stayed stale")
	}
	if err := ValidateFileSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	c.openFilesMu.RLock()
	version := c.openFiles[string(protocol.URIFromPath(files[0]))].Version
	c.openFilesMu.RUnlock()
	if version != 1 {
		t.Fatal("unchanged document was needlessly versioned")
	}
	if err := os.WriteFile(files[1], []byte("concurrent edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFileSnapshot(snapshot); err == nil {
		t.Fatal("accepted concurrent disk change")
	}
	if err := os.Remove(files[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncOpenFiles(ctx); err == nil {
		t.Fatal("ignored failed synchronization")
	}
}
