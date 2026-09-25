package watcher

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// Project and solution files make .NET language servers reload the whole
// solution and run design-time builds. Build tools such as bari delete and
// regenerate them on every build, usually with identical content, and the
// WPF markup compiler creates and deletes temporary projects in the source
// tree while it builds. Forwarding that churn starts reloads in the middle of
// the build, so project files are held until they settle and only content
// changes are reported.

var projectExtensions = map[string]bool{
	".csproj": true, ".fsproj": true, ".vbproj": true,
	".sln": true, ".slnx": true, ".props": true, ".targets": true,
}

func isProjectFile(file string) bool {
	return projectExtensions[strings.ToLower(filepath.Ext(file))]
}

// isBuildByproduct matches project files a build creates and deletes next to
// real projects, such as the WPF markup compiler's <Project>_<random>_wpftmp.csproj.
func isBuildByproduct(file string) bool {
	return isProjectFile(file) && strings.Contains(strings.ToLower(filepath.Base(file)), "_wpftmp.")
}

type fileChange struct {
	path string
	kind protocol.FileChangeType
}

type projectGate struct {
	settle   time.Duration
	reported map[string][sha256.Size]byte // content the server last saw; absent files are missing
	held     map[string]bool
	activity time.Time
}

func newProjectGate(settle time.Duration) *projectGate {
	return &projectGate{settle: settle, reported: make(map[string][sha256.Size]byte), held: make(map[string]bool)}
}

// record notes the content the language server loads at startup.
func (g *projectGate) record(file string) {
	if sum, exists, err := contentDigest(file); err == nil && exists {
		g.reported[file] = sum
	}
}

func (g *projectGate) hold(file string, now time.Time) {
	g.held[file] = true
	g.activity = now
}

// touch reports build activity, which keeps held files waiting.
func (g *projectGate) touch(now time.Time) {
	if len(g.held) != 0 {
		g.activity = now
	}
}

// due returns the net changes of held files once they have been quiet for
// the settle time. Files rewritten with the content the server last saw,
// including deleted and regenerated ones, produce no change.
func (g *projectGate) due(now time.Time) []fileChange {
	if len(g.held) == 0 || now.Sub(g.activity) < g.settle {
		return nil
	}
	var changes []fileChange
	for file := range g.held {
		sum, exists, err := contentDigest(file)
		if err != nil {
			// Still being written or locked: look again after another quiet period.
			g.activity = now
			continue
		}
		delete(g.held, file)
		previous, known := g.reported[file]
		switch {
		case exists && !known:
			changes = append(changes, fileChange{file, protocol.Created})
		case !exists && known:
			changes = append(changes, fileChange{file, protocol.Deleted})
		case exists && sum != previous:
			changes = append(changes, fileChange{file, protocol.Changed})
		}
		if exists {
			g.reported[file] = sum
		} else {
			delete(g.reported, file)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })
	return changes
}

func contentDigest(file string) (sum [sha256.Size]byte, exists bool, err error) {
	data, err := readSharingDelete(file)
	if errors.Is(err, os.ErrNotExist) {
		return sum, false, nil
	}
	if err != nil {
		return sum, false, err
	}
	return sha256.Sum256(data), true, nil
}
