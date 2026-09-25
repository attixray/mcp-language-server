package watcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func writeFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectGateDropsIdenticalRegeneration(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "App.csproj")
	writeFile(t, project, "<Project />")
	gate := newProjectGate(time.Second)
	gate.record(project)
	start := time.Now()

	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	gate.hold(project, start)
	writeFile(t, project, "<Project />")
	gate.hold(project, start.Add(100*time.Millisecond))

	if changes := gate.due(start.Add(time.Second)); changes != nil {
		t.Fatalf("reported before settling: %v", changes)
	}
	if changes := gate.due(start.Add(2 * time.Second)); len(changes) != 0 {
		t.Fatalf("identical regeneration reported: %v", changes)
	}
	if len(gate.held) != 0 {
		t.Fatalf("held files left: %v", gate.held)
	}
}

func TestProjectGateIgnoresNewProjectGUID(t *testing.T) {
	project := filepath.Join(t.TempDir(), "App.csproj")
	writeFile(t, project, "<Project><PropertyGroup><ProjectGuid>{11111111-1111-1111-1111-111111111111}</ProjectGuid></PropertyGroup></Project>")
	gate := newProjectGate(time.Second)
	gate.record(project)
	now := time.Now()

	writeFile(t, project, "<Project><PropertyGroup><ProjectGuid>{22222222-2222-2222-2222-222222222222}</ProjectGuid></PropertyGroup></Project>")
	gate.hold(project, now)
	if changes := gate.due(now.Add(time.Second)); len(changes) != 0 {
		t.Fatalf("new GUID reported: %v", changes)
	}
	writeFile(t, project, "<Project><PropertyGroup><ProjectGuid>{22222222-2222-2222-2222-222222222222}</ProjectGuid><Nullable>enable</Nullable></PropertyGroup></Project>")
	gate.hold(project, now)
	if changes := gate.due(now.Add(time.Second)); len(changes) != 1 || changes[0].kind != protocol.Changed {
		t.Fatalf("got %v, want one change", changes)
	}
}

func TestProjectGateReportsNetChangesOnce(t *testing.T) {
	dir := t.TempDir()
	changed, deleted, created := filepath.Join(dir, "a.csproj"), filepath.Join(dir, "b.csproj"), filepath.Join(dir, "c.sln")
	writeFile(t, changed, "<Project />")
	writeFile(t, deleted, "<Project />")
	gate := newProjectGate(time.Second)
	gate.record(changed)
	gate.record(deleted)
	now := time.Now()

	writeFile(t, changed, "<Project Sdk=\"Microsoft.NET.Sdk\" />")
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	writeFile(t, created, "")
	for _, file := range []string{changed, deleted, created} {
		gate.hold(file, now)
	}
	want := []fileChange{{changed, protocol.Changed}, {deleted, protocol.Deleted}, {created, protocol.Created}}
	if got := gate.due(now.Add(time.Second)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, file := range []string{changed, deleted, created} {
		gate.hold(file, now.Add(time.Second))
	}
	if got := gate.due(now.Add(3 * time.Second)); len(got) != 0 {
		t.Fatalf("unchanged files reported again: %v", got)
	}
}

func TestProjectGateWaitsWhileBuildIsActive(t *testing.T) {
	project := filepath.Join(t.TempDir(), "App.csproj")
	writeFile(t, project, "<Project />")
	gate := newProjectGate(time.Second)
	start := time.Now()
	gate.hold(project, start)
	gate.touch(start.Add(900 * time.Millisecond))
	if changes := gate.due(start.Add(1500 * time.Millisecond)); changes != nil {
		t.Fatalf("reported during build activity: %v", changes)
	}
	if changes := gate.due(start.Add(1900 * time.Millisecond)); len(changes) != 1 || changes[0].kind != protocol.Created {
		t.Fatalf("got %v, want one creation", changes)
	}
}

func TestBuildByproducts(t *testing.T) {
	for file, want := range map[string]bool{
		`src\ValveSeat\Extensions.ValveSeat\cs\Extensions.ValveSeat_w5kuco1e_wpftmp.csproj`: true,
		"App_abc_WPFTMP.csproj": true,
		"App.csproj":            false,
		"notes_wpftmp.txt":      false,
	} {
		if got := isBuildByproduct(file); got != want {
			t.Errorf("isBuildByproduct(%q) = %v, want %v", file, got, want)
		}
	}
}
