package mcpserver

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type suppressedWrite struct {
	sum   [32]byte
	until time.Time
}

type projectWatcher struct {
	project *Project
	watcher *fsnotify.Watcher
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	files   map[string]bool
	dirs    map[string]bool
	skip    map[string]suppressedWrite
	pending map[string]bool
}

func newProjectWatcher(project *Project) (*projectWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	result := &projectWatcher{project: project, watcher: watcher, done: make(chan struct{}), files: make(map[string]bool), dirs: make(map[string]bool), skip: make(map[string]suppressedWrite), pending: make(map[string]bool)}
	go result.run()
	return result, nil
}

func (watch *projectWatcher) Close() {
	watch.once.Do(func() {
		_ = watch.watcher.Close()
		<-watch.done
	})
}

func (watch *projectWatcher) SetFiles(paths []string) {
	watch.mu.Lock()
	defer watch.mu.Unlock()
	files := make(map[string]bool, len(paths))
	dirs := make(map[string]bool)
	for _, path := range paths {
		path = filepath.Clean(path)
		files[path] = true
		dirs[filepath.Dir(path)] = true
	}
	for dir := range dirs {
		if !watch.dirs[dir] {
			_ = watch.watcher.Add(dir)
		}
	}
	for dir := range watch.dirs {
		if !dirs[dir] {
			_ = watch.watcher.Remove(dir)
		}
	}
	watch.files = files
	watch.dirs = dirs
}

func (watch *projectWatcher) Suppress(path string, data []byte) {
	watch.mu.Lock()
	watch.skip[filepath.Clean(path)] = suppressedWrite{sum: sha256.Sum256(data), until: time.Now().Add(2 * time.Second)}
	watch.mu.Unlock()
}

func (watch *projectWatcher) relevant(path string) bool {
	path = filepath.Clean(path)
	watch.mu.Lock()
	defer watch.mu.Unlock()
	if !watch.files[path] {
		return false
	}
	if suppressed, ok := watch.skip[path]; ok {
		if time.Now().Before(suppressed.until) {
			if data, err := os.ReadFile(path); err == nil && sha256.Sum256(data) == suppressed.sum {
				return false
			}
		}
		delete(watch.skip, path)
	}
	watch.pending[path] = true
	return true
}

func (watch *projectWatcher) run() {
	defer close(watch.done)
	var timer *time.Timer
	var timerChannel <-chan time.Time
	for {
		select {
		case event, ok := <-watch.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 || !watch.relevant(event.Name) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(120 * time.Millisecond)
			} else if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
				timer.Reset(120 * time.Millisecond)
			} else {
				timer.Reset(120 * time.Millisecond)
			}
			timerChannel = timer.C
		case <-timerChannel:
			timerChannel = nil
			watch.reload()
		case _, ok := <-watch.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (watch *projectWatcher) reload() {
	watch.mu.Lock()
	changed := watch.pending
	watch.pending = make(map[string]bool)
	watch.mu.Unlock()
	project := watch.project
	project.mu.Lock()
	project.reloadAffectedViewsLocked(changed)
	project.revision++
	project.refreshWatchLocked()
	viewIDs := make([]string, 0, len(project.views))
	for id := range project.views {
		viewIDs = append(viewIDs, id)
	}
	notify := project.notify
	projectID := project.id
	project.mu.Unlock()
	sort.Strings(viewIDs)
	if notify != nil {
		notify(projectID, viewIDs)
	}
}

func (p *Project) refreshWatchLocked() {
	if p.watch == nil {
		return
	}
	paths := make([]string, 0, len(p.sources))
	for path := range p.sources {
		paths = append(paths, path)
	}
	for _, view := range p.views {
		if view.runtime != nil {
			paths = append(paths, view.runtime.Dependencies()...)
		}
	}
	p.watch.SetFiles(paths)
}
