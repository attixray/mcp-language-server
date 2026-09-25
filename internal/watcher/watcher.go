package watcher

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/isaacphi/mcp-language-server/internal/logging"
	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

var watcherLogger = logging.NewLogger(logging.Watcher)

// WorkspaceWatcher keeps registration updates thread safe; filesystem events,
// pending notifications and shutdown are handled by one event loop.
type WorkspaceWatcher struct {
	client         LSPClient
	config         *WatcherConfig
	registrations  map[string][]protocol.FileSystemWatcher
	registrationMu sync.RWMutex
	gitignore      *GitignoreMatcher
	ready          chan struct{}
}

func NewWorkspaceWatcher(client LSPClient) *WorkspaceWatcher {
	return NewWorkspaceWatcherWithConfig(client, DefaultWatcherConfig())
}

func NewWorkspaceWatcherWithConfig(client LSPClient, config *WatcherConfig) *WorkspaceWatcher {
	w := &WorkspaceWatcher{client: client, config: config, registrations: make(map[string][]protocol.FileSystemWatcher), ready: make(chan struct{})}
	// Bind before initialize, replaying any registrations already received.
	if registrar, ok := client.(interface{ RegisterFileWatchHandler(lsp.FileWatchHandler) }); ok {
		registrar.RegisterFileWatchHandler(func(id string, watchers []protocol.FileSystemWatcher) {
			w.AddRegistrations(context.Background(), id, watchers)
		})
	}
	return w
}

// Ready closes after the initial directory watches are installed.
// WatchWorkspace must be run only once per instance.
func (w *WorkspaceWatcher) Ready() <-chan struct{} { return w.ready }

func (w *WorkspaceWatcher) AddRegistrations(_ context.Context, id string, watchers []protocol.FileSystemWatcher) {
	w.registrationMu.Lock()
	defer w.registrationMu.Unlock()
	w.registrations[id] = append([]protocol.FileSystemWatcher(nil), watchers...)
	// Watched-file registration does not ask us to open all matching documents.
	// TypeScript's explicit initialization handles its document-opening needs.
}

type pendingEvent struct {
	kind protocol.FileChangeType
	due  time.Time
}

func (w *WorkspaceWatcher) WatchWorkspace(ctx context.Context, workspacePath string) {
	var err error
	w.gitignore, err = NewGitignoreMatcher(workspacePath)
	if err != nil {
		watcherLogger.Error("Error loading gitignore: %v", err)
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		watcherLogger.Error("Error creating watcher: %v", err)
		return
	}
	defer func() {
		if err := fs.Close(); err != nil {
			watcherLogger.Error("Error closing watcher: %v", err)
		}
	}()
	dirs := make(map[string]bool)
	files := make(map[string]bool)
	pending := make(map[string]pendingEvent)
	projects := newProjectGate(w.config.ProjectSettleTime)
	queue := func(file string, kind protocol.FileChangeType) {
		if previous, ok := pending[file]; ok && previous.kind == protocol.Created && kind == protocol.Changed {
			kind = protocol.Created
		}
		pending[file] = pendingEvent{kind: kind, due: time.Now().Add(w.config.DebounceTime)}
	}
	scan := func(root string, created bool) {
		err := filepath.WalkDir(root, func(file string, entry os.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				if file != workspacePath && w.shouldExcludeDir(file) {
					return filepath.SkipDir
				}
				if !dirs[file] {
					if err := fs.Add(file); err != nil {
						watcherLogger.Error("Error watching %s: %v", file, err)
					} else {
						dirs[file] = true
					}
				}
			} else if !w.shouldExcludeFile(file) {
				if created && !files[file] {
					queue(file, protocol.Created)
				} else if !created && isProjectFile(file) {
					projects.record(file)
				}
				files[file] = true
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			watcherLogger.Error("Error scanning workspace: %v", err)
		}
	}
	scan(workspacePath, false)
	close(w.ready)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			for file, event := range pending {
				if now.Before(event.due) {
					continue
				}
				delete(pending, file)
				if isProjectFile(file) {
					projects.hold(file, now)
					continue
				}
				w.handleFileEvent(ctx, file, event.kind)
			}
			if changes := projects.due(now); len(changes) != 0 {
				w.handleFileEvents(ctx, changes)
			}
		case event, ok := <-fs.Events:
			if !ok {
				return
			}
			file := filepath.Clean(event.Name)
			if isBuildByproduct(file) {
				projects.touch(time.Now())
				continue
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				if dirs[file] {
					for known := range files {
						if strings.HasPrefix(known, file+string(filepath.Separator)) {
							delete(files, known)
							queue(known, protocol.Deleted)
						}
					}
					for dir := range dirs {
						if dir == file || strings.HasPrefix(dir, file+string(filepath.Separator)) {
							delete(dirs, dir)
						}
					}
				} else if files[file] {
					delete(files, file)
					queue(file, protocol.Deleted)
				}
				continue
			}
			info, err := os.Stat(file)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if event.Op&fsnotify.Create != 0 && !w.shouldExcludeDir(file) {
					scan(file, true)
				}
				continue
			}
			if w.shouldExcludeFile(file) {
				continue
			}
			files[file] = true
			switch {
			case event.Op&fsnotify.Create != 0:
				queue(file, protocol.Created)
			case event.Op&fsnotify.Write != 0:
				queue(file, protocol.Changed)
			}
		case err, ok := <-fs.Errors:
			if !ok {
				return
			}
			watcherLogger.Error("Watcher error: %v", err)
		}
	}
}

func (w *WorkspaceWatcher) handleFileEvent(ctx context.Context, file string, kind protocol.FileChangeType) {
	w.handleFileEvents(ctx, []fileChange{{file, kind}})
}

// handleFileEvents reports changes that belong together in one notification.
func (w *WorkspaceWatcher) handleFileEvents(ctx context.Context, changes []fileChange) {
	var events []protocol.FileEvent
	for _, change := range changes {
		if ctx.Err() != nil {
			return
		}
		file, kind := change.path, change.kind
		// Open-document synchronization is independent of watched-file registration.
		if w.client.IsFileOpen(file) {
			if kind == protocol.Deleted {
				if closer, ok := w.client.(interface {
					CloseFile(context.Context, string) error
				}); ok {
					if err := closer.CloseFile(ctx, file); err != nil {
						watcherLogger.Error("Error closing deleted file: %v", err)
					}
				}
			} else {
				if err := w.client.NotifyChange(ctx, file); err != nil {
					watcherLogger.Error("Error syncing file: %v", err)
				}
			}
		}
		watched, mask := w.isPathWatched(file)
		required := map[protocol.FileChangeType]protocol.WatchKind{protocol.Created: protocol.WatchCreate, protocol.Changed: protocol.WatchChange, protocol.Deleted: protocol.WatchDelete}[kind]
		if watched && mask&required != 0 {
			events = append(events, protocol.FileEvent{URI: protocol.URIFromPath(file), Type: kind})
		}
	}
	if len(events) == 0 {
		return
	}
	if err := w.client.DidChangeWatchedFiles(ctx, protocol.DidChangeWatchedFilesParams{Changes: events}); err != nil {
		watcherLogger.Error("Error notifying file event: %v", err)
	}
}

func (w *WorkspaceWatcher) isPathWatched(file string) (bool, protocol.WatchKind) {
	w.registrationMu.RLock()
	defer w.registrationMu.RUnlock()
	all := protocol.WatchCreate | protocol.WatchChange | protocol.WatchDelete
	if len(w.registrations) == 0 {
		return true, all
	}
	var mask protocol.WatchKind
	for _, registrations := range w.registrations {
		for _, reg := range registrations {
			if w.matchesPattern(file, reg.GlobPattern) {
				if reg.Kind == nil {
					mask |= all
				} else {
					mask |= *reg.Kind
				}
			}
		}
	}
	return mask != 0, mask
}

// matchesGlob handles path segments, **, character classes and brace choices.
func matchesGlob(pattern, file string) bool {
	if start := strings.IndexByte(pattern, '{'); start >= 0 {
		if end := strings.IndexByte(pattern[start:], '}'); end >= 0 {
			end += start
			for _, choice := range strings.Split(pattern[start+1:end], ",") {
				if matchesGlob(pattern[:start]+choice+pattern[end+1:], file) {
					return true
				}
			}
			return false
		}
	}
	parts := strings.Split(pattern, "/")
	names := strings.Split(file, "/")
	var match func(int, int) bool
	match = func(p, n int) bool {
		if p == len(parts) {
			return n == len(names)
		}
		if parts[p] == "**" {
			return match(p+1, n) || (n < len(names) && match(p, n+1))
		}
		if n == len(names) {
			return false
		}
		// LSP uses [!...] for negated classes; path.Match uses [^...].
		segment := strings.ReplaceAll(parts[p], "[!", "[^")
		ok, err := path.Match(segment, names[n])
		return err == nil && ok && match(p+1, n+1)
	}
	return match(0, 0)
}

func (w *WorkspaceWatcher) matchesPattern(file string, pattern protocol.GlobPattern) bool {
	info, err := pattern.AsPattern()
	if err != nil {
		return false
	}
	base := info.GetBasePath()
	glob := filepath.ToSlash(info.GetPattern())
	if base != "" {
		if strings.HasPrefix(base, "file:") {
			base = protocol.DocumentUri(base).Path()
		}
		relative, err := filepath.Rel(base, file)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
		return matchesGlob(glob, filepath.ToSlash(relative))
	}
	return matchesGlob(glob, filepath.ToSlash(file)) || (!strings.Contains(glob, "/") && matchesGlob(glob, filepath.Base(file)))
}

// shouldExcludeDir returns true if the directory should be excluded from watching/opening
func (w *WorkspaceWatcher) shouldExcludeDir(dirPath string) bool {
	dirName := filepath.Base(dirPath)

	// Skip dot directories
	if strings.HasPrefix(dirName, ".") {
		return true
	}

	// Skip common excluded directories
	if w.config.ExcludedDirs[dirName] {
		return true
	}

	// Check gitignore patterns
	if w.gitignore != nil && w.gitignore.ShouldIgnore(dirPath, true) {
		watcherLogger.Debug("Directory %s excluded by gitignore pattern", dirPath)
		return true
	}

	return false
}

// shouldExcludeFile returns true if the file should be excluded from opening
func (w *WorkspaceWatcher) shouldExcludeFile(filePath string) bool {
	fileName := filepath.Base(filePath)

	// Skip dot files
	if strings.HasPrefix(fileName, ".") {
		return true
	}

	// Check file extension
	ext := strings.ToLower(filepath.Ext(filePath))
	if w.config.ExcludedFileExtensions[ext] || w.config.LargeBinaryExtensions[ext] {
		return true
	}

	// Skip temporary files
	if strings.HasSuffix(filePath, "~") || isBuildByproduct(filePath) {
		return true
	}

	// Check gitignore patterns
	if w.gitignore != nil && w.gitignore.ShouldIgnore(filePath, false) {
		watcherLogger.Debug("File %s excluded by gitignore pattern", filePath)
		return true
	}

	// Check file size
	info, err := os.Stat(filePath)
	if err != nil {
		// If we can't stat the file, skip it
		return true
	}

	// Skip large files
	if info.Size() > w.config.MaxFileSize {
		watcherLogger.Debug("Skipping large file: %s (%.2f MB)", filePath, float64(info.Size())/(1024*1024))
		return true
	}

	return false
}
