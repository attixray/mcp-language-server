package watcher

import (
	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"path/filepath"
	"testing"
)

func TestGlobPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"**/*.{go,mod}", "nested/file.go", true},
		{"**/*.{go,mod}", "go.mod", true},
		{"**/*.{go,mod}", "file.cs", false},
		{"**/test_*.go", "nested/test_one.go", true},
		{"**/test_*.go", "nested/other.go", false},
		{"src/**/a?.[!x]", "src/a1.c", true},
		{"src/**/a?.[!x]", "src/deep/a1.x", false},
	} {
		if got := matchesGlob(tc.pattern, tc.file); got != tc.want {
			t.Errorf("%q / %q = %v", tc.pattern, tc.file, got)
		}
	}
}

func TestRelativePatternDoesNotEscapeBase(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "folder # %")
	w := &WorkspaceWatcher{}
	for _, glob := range []string{"**/*", "**/*.cs", "**/*.{cs,fs}"} {
		pattern := protocol.GlobPattern{Value: protocol.RelativePattern{
			BaseURI: protocol.Or_RelativePattern_baseUri{Value: protocol.URIFromPath(base)}, Pattern: glob,
		}}
		if !w.matchesPattern(filepath.Join(base, "a.cs"), pattern) {
			t.Errorf("inside base did not match %s", glob)
		}
		if w.matchesPattern(filepath.Join(root, "other", "a.cs"), pattern) {
			t.Errorf("escaped base matched %s", glob)
		}
	}
}
