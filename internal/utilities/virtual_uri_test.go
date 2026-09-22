package utilities

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func TestVirtualURIRejectsEntireWorkspaceEdit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "unchanged.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	edit := protocol.WorkspaceEdit{Changes: map[protocol.DocumentUri][]protocol.TextEdit{protocol.URIFromPath(file): {{NewText: "overwrite"}}}, DocumentChanges: []protocol.DocumentChange{{DeleteFile: &protocol.DeleteFile{URI: "jdt://virtual"}}}}
	if err := ApplyWorkspaceEdit(edit); err == nil {
		t.Fatal("accepted non-file URI")
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "original" {
		t.Fatalf("partial edit before URI rejection: %q %v", got, err)
	}
}
