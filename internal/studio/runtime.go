package studio

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/session"
)

type Snapshot struct {
	Root        *project.Node
	Viewport    image.Point
	Screen      string
	Screens     []string
	Invalid     bool
	Diagnostics []document.Diagnostic
	Scroll      map[string]image.Point
	Transient   interaction.Transient
	StateValues map[string]map[string]any
	Revision    uint64
	HasState    bool
}

type Runtime struct {
	mu                sync.RWMutex
	reloadMu          sync.Mutex
	root              string
	entry             string
	loaded            *project.Loaded
	selected          string
	viewport          image.Point
	viewportExplicit  bool
	diagnostics       []document.Diagnostic
	invalid           bool
	scroll            map[string]image.Point
	state             *interaction.Store
	effectiveRoot     *project.Node
	effectiveSource   *project.Node
	effectiveScreen   string
	effectiveRevision uint64
}

func NewRuntime(root, entry string) (*Runtime, error) {
	runtime := &Runtime{root: root, entry: entry, scroll: make(map[string]image.Point), state: interaction.NewStore()}
	runtime.Reload()
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.loaded == nil {
		return runtime, fmt.Errorf("initial document is invalid")
	}
	return runtime, nil
}

func (runtime *Runtime) Reload() {
	runtime.reloadMu.Lock()
	defer runtime.reloadMu.Unlock()
	runtime.mu.RLock()
	width := runtime.viewport.X
	viewportExplicit := runtime.viewportExplicit
	runtime.mu.RUnlock()
	if width <= 0 {
		width = 1
	}
	runtime.mu.RLock()
	selection := runtime.selected
	runtime.mu.RUnlock()
	loaded, diagnostics := project.LoadSelection(runtime.root, runtime.entry, width, selection)
	if loaded != nil && len(diagnostics) == 0 && !viewportExplicit && loaded.Viewport.Width > 0 && loaded.Viewport.Width != width {
		loaded, diagnostics = project.LoadSelection(runtime.root, runtime.entry, loaded.Viewport.Width, selection)
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if loaded == nil || len(diagnostics) != 0 {
		runtime.diagnostics = append([]document.Diagnostic(nil), diagnostics...)
		runtime.invalid = true
		return
	}
	runtime.loaded = loaded
	runtime.diagnostics = nil
	runtime.invalid = false
	if !runtime.viewportExplicit {
		runtime.viewport = image.Pt(loaded.Viewport.Width, loaded.Viewport.Height)
	}
	if loaded.Document.Kind == document.KindApp {
		if _, ok := loaded.Screens[runtime.selected]; !ok {
			runtime.selected = loaded.Document.Entry
		}
	} else {
		runtime.selected = loaded.Selected
	}
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	specs := make([]interaction.ScopeSpec, len(loaded.StateScopes))
	for index, scope := range loaded.StateScopes {
		specs[index] = interaction.ScopeSpec{ID: scope.ID, Context: scope.Context, State: scope.State, Initial: scope.Initial}
	}
	if loaded.Document.Kind == document.KindComponent {
		runtime.state.ReconcileContext(loaded.Selected, specs)
	} else {
		runtime.state.Reconcile(specs)
	}
	runtime.effectiveRoot = nil
	runtime.pruneScroll(loaded)
}

func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	snapshot := Snapshot{
		Viewport: runtime.viewport, Screen: runtime.selected, Invalid: runtime.invalid,
		Diagnostics: append([]document.Diagnostic(nil), runtime.diagnostics...),
		Scroll:      cloneScroll(runtime.scroll),
		Transient:   runtime.state.Transient(),
		StateValues: runtime.state.AllValues(),
		Revision:    runtime.state.Revision(),
	}
	if runtime.loaded == nil {
		return snapshot
	}
	if runtime.loaded.Document.Kind == document.KindApp {
		snapshot.Root = runtime.loaded.Screens[runtime.selected]
		for name := range runtime.loaded.Screens {
			snapshot.Screens = append(snapshot.Screens, name)
		}
		sort.Strings(snapshot.Screens)
	} else {
		snapshot.Root = runtime.loaded.Root
		snapshot.Screens = append(snapshot.Screens, runtime.loaded.Previews...)
	}
	for _, scope := range runtime.loaded.StateScopes {
		if scope.Context == runtime.selected {
			snapshot.HasState = true
			break
		}
	}
	if snapshot.Root != nil && (runtime.effectiveRoot == nil || runtime.effectiveSource != snapshot.Root || runtime.effectiveScreen != runtime.selected || runtime.effectiveRevision != snapshot.Revision) {
		runtime.effectiveSource = snapshot.Root
		runtime.effectiveScreen = runtime.selected
		runtime.effectiveRevision = snapshot.Revision
		runtime.effectiveRoot = interaction.ResolvePersistentTree(snapshot.Root, snapshot.StateValues)
	}
	snapshot.Root = runtime.effectiveRoot
	return snapshot
}

func (runtime *Runtime) Activate(activation interaction.Activation) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		return fmt.Errorf("interaction state is unavailable")
	}
	if err := runtime.state.Apply(activation.Scope, activation.Actions); err != nil {
		return err
	}
	runtime.effectiveRoot = nil
	return nil
}

func (runtime *Runtime) SetTransient(transient interaction.Transient) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	if runtime.state.Transient() != transient {
		runtime.state.SetTransient(transient)
	}
}

func (runtime *Runtime) ResetState() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state != nil {
		runtime.state.ResetContext(runtime.selected)
		runtime.effectiveRoot = nil
	}
}

func (runtime *Runtime) SelectScreen(screen string) bool {
	runtime.mu.Lock()
	if runtime.loaded == nil {
		runtime.mu.Unlock()
		return false
	}
	if runtime.loaded.Document.Kind == document.KindApp {
		if runtime.loaded.Screens[screen] == nil {
			runtime.mu.Unlock()
			return false
		}
	} else if !stringIn(runtime.loaded.Previews, screen) {
		runtime.mu.Unlock()
		return false
	}
	runtime.selected = screen
	isComponent := runtime.loaded.Document.Kind == document.KindComponent
	runtime.mu.Unlock()
	if isComponent {
		runtime.Reload()
	}
	return true
}

func (runtime *Runtime) SetViewport(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	runtime.mu.Lock()
	runtime.viewport = image.Pt(width, height)
	runtime.viewportExplicit = true
	runtime.mu.Unlock()
	runtime.Reload()
}

func (runtime *Runtime) Scroll(delta int) {
	runtime.scrollAxis("", delta)
}

func (runtime *Runtime) ScrollAxis(axis string, delta int) {
	runtime.scrollAxis(axis, delta)
}

func (runtime *Runtime) scrollAxis(axis string, delta int) {
	if delta == 0 {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var target *project.Node
	var find func(*project.Node)
	find = func(node *project.Node) {
		if node == nil || target != nil {
			return
		}
		if node.Type == "scroll" {
			nodeAxis, _ := node.Props["axis"].(string)
			if nodeAxis == "" {
				nodeAxis = "vertical"
			}
			if axis == "" || axis == nodeAxis {
				target = node
				return
			}
		}
		for _, child := range node.Children {
			find(child)
		}
	}
	if runtime.loaded == nil {
		return
	}
	if runtime.loaded.Document.Kind == document.KindApp {
		find(runtime.loaded.Screens[runtime.selected])
	} else {
		find(runtime.loaded.Root)
	}
	if target == nil {
		return
	}
	key := target.Name
	if key == "" {
		key = target.Handle
	}
	offset := runtime.scroll[key]
	if axis, _ := target.Props["axis"].(string); axis == "horizontal" {
		offset.X = max(0, offset.X+delta)
	} else {
		offset.Y = max(0, offset.Y+delta)
	}
	runtime.scroll[key] = offset
}

func (runtime *Runtime) SetScrollOffset(key, axis string, value int) {
	if key == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	offset := runtime.scroll[key]
	if axis == "horizontal" {
		offset.X = max(0, value)
	} else {
		offset.Y = max(0, value)
	}
	runtime.scroll[key] = offset
}

func (runtime *Runtime) Capture(path string, scale int) (string, error) {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return "", fmt.Errorf("no valid frame is available")
	}
	if err := render.ValidateOutput(path); err != nil {
		return "", err
	}
	if err := render.Capture(path, snapshot.Root, snapshot.Viewport, renderState(snapshot), scale); err != nil {
		return "", err
	}
	if snapshot.Invalid {
		return "source is invalid; captured the last-good frame", nil
	}
	return "", nil
}

func (runtime *Runtime) SessionHandler(focus func()) session.Handler {
	return func(_ context.Context, request session.Request) session.Response {
		switch request.Action {
		case "focus":
			if focus != nil {
				focus()
			}
			return session.Response{OK: true}
		case "render":
			warning, err := runtime.Capture(request.Output, request.Scale)
			if err != nil {
				return session.Response{Error: err.Error()}
			}
			return session.Response{OK: true, Warning: warning}
		default:
			return session.Response{Error: fmt.Sprintf("unknown session action %q", request.Action)}
		}
	}
}

func (runtime *Runtime) Watch(ctx context.Context, changed func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	watched := make(map[string]bool)
	addDirectories := func() {
		runtime.mu.RLock()
		var dependencies []string
		if runtime.loaded != nil {
			dependencies = append(dependencies, runtime.loaded.Dependencies...)
		}
		dependencies = append(dependencies, runtime.entry)
		runtime.mu.RUnlock()
		desired := make(map[string]bool)
		for _, dependency := range dependencies {
			directory := filepath.Dir(dependency)
			desired[directory] = true
			if !watched[directory] {
				if watcher.Add(directory) == nil {
					watched[directory] = true
				}
			}
		}
		for directory := range watched {
			if !desired[directory] {
				_ = watcher.Remove(directory)
				delete(watched, directory)
			}
		}
	}
	addDirectories()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !runtime.watchesPath(event.Name) {
				continue
			}
			if debounce != nil {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(120 * time.Millisecond)
			} else {
				debounce = time.NewTimer(120 * time.Millisecond)
			}
			debounceC = debounce.C
		case <-debounceC:
			debounceC = nil
			runtime.Reload()
			addDirectories()
			if changed != nil {
				changed()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return err
		}
	}
}

func (runtime *Runtime) watchesPath(path string) bool {
	path = filepath.Clean(path)
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if filepath.Clean(runtime.entry) == path {
		return true
	}
	if runtime.loaded == nil {
		return false
	}
	for _, dependency := range runtime.loaded.Dependencies {
		if filepath.Clean(dependency) == path {
			return true
		}
	}
	return false
}

func (runtime *Runtime) pruneScroll(loaded *project.Loaded) {
	names := make(map[string]bool)
	var walk func(*project.Node)
	walk = func(node *project.Node) {
		if node == nil {
			return
		}
		if node.Type == "scroll" && node.Name != "" {
			names[node.Name] = true
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range loaded.Screens {
		walk(root)
	}
	walk(loaded.Root)
	for name := range runtime.scroll {
		if !names[name] {
			delete(runtime.scroll, name)
		}
	}
}

func cloneScroll(in map[string]image.Point) map[string]image.Point {
	out := make(map[string]image.Point, len(in))
	for name, offset := range in {
		out[name] = offset
	}
	return out
}

func stringIn(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
