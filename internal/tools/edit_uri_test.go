package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/isaacphi/mcp-language-server/internal/utilities"
)

func TestMixedLineEndingEditUsesLogicalLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "edit # %.txt")
	if err := os.WriteFile(file, []byte("first\nsecond\r\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	location, err := getRange(2, 2, file)
	if err != nil {
		t.Fatal(err)
	}
	if location.Start.Line != 1 || location.End.Character != 6 {
		t.Fatalf("wrong line range: %v", location)
	}
	if err := utilities.ApplyTextEdits(protocol.URIFromPath(file), []protocol.TextEdit{{Range: location, NewText: "updated"}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\r\nupdated\r\nthird\r\n" {
		t.Fatalf("edited wrong line: %q", got)
	}
}
