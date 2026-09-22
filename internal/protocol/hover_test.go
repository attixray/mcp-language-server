package protocol

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestHoverWireShapes(t *testing.T) {
	for _, tc := range []struct{ wire, want string }{
		{`{"contents":{"kind":"markdown","value":"hello"}}`, "hello"},
		{`{"contents":"legacy"}`, "legacy"},
		{`{"contents":{"language":"java","value":"class X"}}`, "```java\nclass X\n```"},
		{`{"contents":["text",{"language":"cs","value":"int x"}]}`, "text\n\n```cs\nint x\n```"},
		{`null`, ""},
	} {
		var h Hover
		if err := json.Unmarshal([]byte(tc.wire), &h); err != nil || h.Contents.Value != tc.want {
			t.Fatalf("%s: %q, %v", tc.wire, h.Contents.Value, err)
		}
	}
}

func TestFilePathRejectsVirtualDocuments(t *testing.T) {
	for _, uri := range []DocumentUri{"jdt://contents/Foo.class", "untitled:test", "", "file:///%ZZ"} {
		if _, err := uri.FilePath(); err == nil {
			t.Errorf("accepted %s", uri)
		}
	}
	name := filepath.Join(t.TempDir(), "file # %25.cs")
	got, err := URIFromPath(name).FilePath()
	if err != nil || got != name {
		t.Fatalf("escaped path: %q %v", got, err)
	}
}
