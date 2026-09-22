package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
)

func TestHoverFallbackKeepsLastLineAndExistingNewline(t *testing.T) {
	// This stdio helper returns null to requests other than rename.
	t.Setenv("RENAME_FRESHNESS_HELPER", "hover")
	c, err := lsp.NewClient(os.Args[0], "-test.run=^TestRenameFreshnessHelper$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, content := range []string{"final line", "final line\n"} {
		file := filepath.Join(t.TempDir(), "last.cs")
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		got, err := GetHoverInfo(ctx, c, file, 1, 1)
		cancel()
		if err != nil || got != "No hover information available for this position on the following line:\n"+content {
			t.Fatalf("last-line fallback: %q %v", got, err)
		}
	}
}
