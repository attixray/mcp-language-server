package testing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/isaacphi/mcp-language-server/internal/watcher"
)

// A build that deletes and regenerates project files with the same content,
// with WPF temporary projects coming and going, must not reach the language
// server; a real project change reaches it once, after the files settle, and
// source edits are not held back.
func TestBuildChurnDoesNotReachLanguageServer(t *testing.T) {
	workspace := t.TempDir()
	projectDir := filepath.Join(workspace, "src", "App", "cs")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(projectDir, "App.csproj")
	source := filepath.Join(projectDir, "Program.cs")
	for file, content := range map[string]string{project: "<Project />", source: "class Program {}"} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	client := NewMockLSPClient()
	config := watcher.DefaultWatcherConfig()
	config.ProjectSettleTime = time.Second
	w := watcher.NewWorkspaceWatcherWithConfig(client, config)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); w.WatchWorkspace(ctx, workspace) }()
	defer func() { cancel(); <-done }()
	select {
	case <-w.Ready():
	case <-ctx.Done():
		t.Fatal("watcher failed to initialize")
	}
	kind := protocol.WatchCreate | protocol.WatchChange | protocol.WatchDelete
	// csharp-ls 0.28 registers this pattern and reloads the solution on any project event.
	w.AddRegistrations(ctx, "csharp-ls", []protocol.FileSystemWatcher{{GlobPattern: protocol.GlobPattern{Value: "**/*.{cs,cshtml,csproj,sln,slnx}"}, Kind: &kind}})

	// bari clean, then build: delete and regenerate the project, while the
	// markup compiler creates and removes its temporary project.
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := os.WriteFile(project, []byte("<Project />"), 0o644); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(projectDir, "App_w5kuco1e_wpftmp.csproj")
	for range 3 {
		if err := os.WriteFile(temporary, []byte("<Project />"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond)
		if err := os.Remove(temporary); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond)
	}
	objDir := filepath.Join(projectDir, "obj")
	if err := os.MkdirAll(objDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objDir, "App.AssemblyInfo.cs"), []byte("// generated"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if events := client.GetEvents(); len(events) != 0 {
		t.Fatalf("build churn reached the language server: %+v", events)
	}

	// Source edits still go through at the usual debounce.
	if err := os.WriteFile(source, []byte("class Program { }"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
	defer waitCancel()
	if !client.WaitForSpecificEvent(waitCtx, protocol.URIFromPath(source), protocol.Changed) {
		t.Fatalf("source change not reported: %+v", client.GetEvents())
	}

	// A real project change is reported once, after it settles.
	client.ResetEvents()
	changedAt := time.Now()
	for i := range 3 {
		if err := os.WriteFile(project, []byte("<Project Sdk=\"Microsoft.NET.Sdk\" />"+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	settleCtx, settleCancel := context.WithTimeout(ctx, 5*time.Second)
	defer settleCancel()
	if !client.WaitForSpecificEvent(settleCtx, protocol.URIFromPath(project), protocol.Changed) {
		t.Fatalf("project change not reported: %+v", client.GetEvents())
	}
	if elapsed := time.Since(changedAt); elapsed < time.Second {
		t.Fatalf("project change reported after %s, before settling", elapsed)
	}
	time.Sleep(1500 * time.Millisecond)
	if events := client.GetEvents(); len(events) != 1 {
		t.Fatalf("got %d notifications, want one: %+v", len(events), events)
	}
}
