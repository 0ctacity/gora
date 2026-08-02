package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/navigation"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/semantic"
	"gora/internal/session"
)

type Snapshot struct {
	Root               *project.Node
	Viewport           image.Point
	Screen             string
	Screens            []string
	Invalid            bool
	Diagnostics        []document.Diagnostic
	Scroll             map[string]image.Point
	Transient          interaction.Transient
	StateValues        map[string]map[string]any
	Revision           uint64
	NavigationRevision uint64
	HasState           bool
	CanBack            bool
	CanForward         bool
	Kind               document.Kind
	Document           string
	RuntimeRevision    uint64
}

type Runtime struct {
	mu                 sync.RWMutex
	reloadMu           sync.Mutex
	root               string
	entry              string
	loaded             *project.Loaded
	selected           string
	viewport           image.Point
	viewportExplicit   bool
	diagnostics        []document.Diagnostic
	invalid            bool
	scroll             map[string]image.Point
	state              *interaction.Store
	navigation         *navigation.History
	navigationRevision uint64
	runtimeRevision    uint64
	effectiveRoot      *project.Node
	effectiveSource    *project.Node
	effectiveScreen    string
	effectiveRevision  uint64
	effectiveTransient interaction.Transient
	publishedTree      *semantic.Node
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

// NewRuntimeAllowInvalid creates a headless-compatible runtime while retaining
// diagnostics when the initial source has no valid frame.
func NewRuntimeAllowInvalid(root, entry string) *Runtime {
	runtime := &Runtime{root: root, entry: entry, scroll: make(map[string]image.Point), state: interaction.NewStore()}
	runtime.Reload()
	return runtime
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
		runtime.runtimeRevision++
		return
	}
	previousScreen := runtime.selected
	previousScroll := cloneScroll(runtime.scroll)
	runtime.loaded = loaded
	runtime.diagnostics = nil
	runtime.invalid = false
	if !runtime.viewportExplicit {
		runtime.viewport = image.Pt(loaded.Viewport.Width, loaded.Viewport.Height)
	}
	if loaded.Document.Kind == document.KindApp {
		if runtime.navigation == nil {
			runtime.navigation = navigation.New(loaded.Document.Entry)
			runtime.selected = loaded.Document.Entry
			runtime.scroll = make(map[string]image.Point)
		} else {
			transition := runtime.navigation.Reconcile(scrollNamesByScreen(loaded), loaded.Document.Entry, previousScroll)
			runtime.selected = transition.Screen
			runtime.scroll = cloneScroll(transition.Scroll)
			if runtime.scroll == nil {
				runtime.scroll = make(map[string]image.Point)
			}
		}
		if runtime.selected != previousScreen {
			runtime.navigationRevision++
		}
	} else {
		runtime.navigation = nil
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
	runtime.runtimeRevision++
}

func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	snapshot := Snapshot{
		Viewport: runtime.viewport, Screen: runtime.selected, Invalid: runtime.invalid,
		Diagnostics:        append([]document.Diagnostic(nil), runtime.diagnostics...),
		Scroll:             cloneScroll(runtime.scroll),
		Transient:          runtime.state.Transient(),
		StateValues:        runtime.state.AllValues(),
		Revision:           runtime.state.Revision(),
		NavigationRevision: runtime.navigationRevision,
		Document:           runtime.entry, RuntimeRevision: runtime.runtimeRevision,
	}
	if runtime.loaded == nil {
		return snapshot
	}
	snapshot.Kind = runtime.loaded.Document.Kind
	if runtime.navigation != nil {
		snapshot.CanBack = runtime.navigation.CanBack()
		snapshot.CanForward = runtime.navigation.CanForward()
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
	transientGeometryChanged := runtime.effectiveTransient.OpenSelect != snapshot.Transient.OpenSelect || runtime.effectiveTransient.ActiveOption != snapshot.Transient.ActiveOption
	if snapshot.Root != nil && (runtime.effectiveRoot == nil || runtime.effectiveSource != snapshot.Root || runtime.effectiveScreen != runtime.selected || runtime.effectiveRevision != snapshot.Revision || transientGeometryChanged) {
		runtime.effectiveSource = snapshot.Root
		runtime.effectiveScreen = runtime.selected
		runtime.effectiveRevision = snapshot.Revision
		runtime.effectiveTransient = snapshot.Transient
		if snapshot.Transient.OpenSelect != "" {
			runtime.effectiveRoot = interaction.ResolveTree(snapshot.Root, snapshot.StateValues, snapshot.Transient)
		} else {
			runtime.effectiveRoot = interaction.ResolvePersistentTree(snapshot.Root, snapshot.StateValues)
		}
	}
	snapshot.Root = runtime.effectiveRoot
	return snapshot
}

// Dependencies returns the current last-good project dependency set.
func (runtime *Runtime) Dependencies() []string {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	result := []string{runtime.entry}
	if runtime.loaded != nil {
		result = append(result, runtime.loaded.Dependencies...)
	}
	sort.Strings(result)
	return result
}

func (runtime *Runtime) Activate(activation interaction.Activation) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		return fmt.Errorf("interaction state is unavailable")
	}
	if activation.OpenSelect != "" {
		transient := runtime.state.Transient()
		if transient.OpenSelect == activation.OpenSelect {
			transient.OpenSelect = ""
			transient.ActiveOption = ""
		} else {
			transient.OpenSelect = activation.OpenSelect
			transient.ActiveOption = activation.ActiveOption
		}
		runtime.state.SetTransient(transient)
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
		return nil
	}
	stateRevision := runtime.state.Revision()
	changed := false
	navigationAction, err := runtime.state.ApplyActivation(activation.Scope, activation.Actions)
	if err != nil {
		return err
	}
	changed = runtime.state.Revision() != stateRevision
	if navigationAction != nil && runtime.navigation != nil && runtime.loaded != nil && runtime.loaded.Document.Kind == document.KindApp {
		var transition navigation.Transition
		currentScroll := cloneScroll(runtime.scroll)
		switch navigationAction.Action {
		case "navigate":
			transition = runtime.navigation.Navigate(navigationAction.To, currentScroll)
		case "replace":
			transition = runtime.navigation.Replace(navigationAction.To, currentScroll)
		case "back":
			transition = runtime.navigation.Back(currentScroll)
		case "forward":
			transition = runtime.navigation.Forward(currentScroll)
		}
		if transition.Changed {
			runtime.selected = transition.Screen
			runtime.scroll = cloneScroll(transition.Scroll)
			if runtime.scroll == nil {
				runtime.scroll = make(map[string]image.Point)
			}
			runtime.state.SetTransient(interaction.Transient{})
			runtime.navigationRevision++
			changed = true
		}
	}
	if activation.CloseSelect {
		transient := runtime.state.Transient()
		if transient.OpenSelect != "" || transient.ActiveOption != "" {
			transient.OpenSelect = ""
			transient.ActiveOption = ""
			runtime.state.SetTransient(transient)
			changed = true
		}
	}
	if changed {
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
	}
	return nil
}

func (runtime *Runtime) SetTransient(transient interaction.Transient) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	if runtime.state.Transient() != transient {
		previous := runtime.state.Transient()
		runtime.state.SetTransient(transient)
		if previous.OpenSelect != transient.OpenSelect || previous.ActiveOption != transient.ActiveOption {
			runtime.effectiveRoot = nil
		}
		runtime.runtimeRevision++
	}
}

func (runtime *Runtime) ResetState() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state != nil {
		runtime.state.ResetContext(runtime.selected)
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
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
	if !isComponent {
		if runtime.navigation == nil {
			runtime.navigation = navigation.New(screen)
		} else {
			runtime.navigation.Reset(screen)
		}
		runtime.scroll = make(map[string]image.Point)
		if runtime.state != nil {
			runtime.state.SetTransient(interaction.Transient{})
		}
		runtime.navigationRevision++
		runtime.runtimeRevision++
		runtime.effectiveRoot = nil
	}
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
	runtime.runtimeRevision++
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
	key := project.ScrollKey(target)
	offset := runtime.scroll[key]
	if axis, _ := target.Props["axis"].(string); axis == "horizontal" {
		offset.X = max(0, offset.X+delta)
	} else {
		offset.Y = max(0, offset.Y+delta)
	}
	runtime.scroll[key] = offset
	runtime.runtimeRevision++
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
	runtime.runtimeRevision++
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

// CapturePNG captures the current last-good viewport without Studio chrome.
func (runtime *Runtime) CapturePNG(scale int) ([]byte, string, error) {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return nil, "", fmt.Errorf("no valid frame is available")
	}
	data, err := render.CapturePNG(snapshot.Root, snapshot.Viewport, renderState(snapshot), scale)
	if err != nil {
		return nil, "", err
	}
	if snapshot.Invalid {
		return data, "source is invalid; captured the last-good frame", nil
	}
	return data, "", nil
}

// RuntimeTree builds the canonical headless semantic tree for the current view.
func (runtime *Runtime) RuntimeTree() (*semantic.Node, error) {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return nil, fmt.Errorf("no valid runtime tree is available")
	}
	return render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot)).Tree, nil
}

// ActivateSemanticID performs the completed semantic activation represented by id.
func (runtime *Runtime) ActivateSemanticID(id string) error {
	tree, err := runtime.RuntimeTree()
	if err != nil {
		return err
	}
	for _, node := range semantic.Flatten(tree) {
		if node.ID != id {
			continue
		}
		if node.Type == "select" && node.Visible && node.InViewport && node.Enabled {
			return runtime.Activate(interaction.Activation{OpenSelect: node.Handle, ActiveOption: initialSelectOption(node)})
		}
		if !node.Visible || !node.InViewport || !node.Enabled || len(node.Actions) == 0 {
			return fmt.Errorf("semantic node %q is not activatable", id)
		}
		return runtime.Activate(interaction.Activation{Scope: node.Scope, Actions: node.Actions})
	}
	return fmt.Errorf("unknown semantic node %q", id)
}

func initialSelectOption(selectNode *semantic.Node) string {
	var first string
	for _, node := range semantic.Flatten(selectNode) {
		if node.Role != "option" || !node.Enabled {
			continue
		}
		if first == "" {
			first = node.Handle
		}
		if node.Selected != nil && *node.Selected {
			return node.Handle
		}
	}
	return first
}

// SetControlValue atomically updates the lexical state bound to one visible
// semantic control and returns its normalized value.
func (runtime *Runtime) SetControlValue(id string, value any) (any, error) {
	tree, err := runtime.RuntimeTree()
	if err != nil {
		return nil, err
	}
	var control *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.ID == id {
			control = node
			break
		}
	}
	if control == nil {
		return nil, fmt.Errorf("unknown semantic node %q", id)
	}
	if !control.Visible || !control.InViewport || !control.Enabled || control.Binding == "" || !settableControlType(control.Type) {
		return nil, fmt.Errorf("semantic node %q does not accept a control value", id)
	}
	if (control.Type == "radio_group" || control.Type == "tabs" || control.Type == "select") && !enabledChoiceValue(control, value) {
		return nil, fmt.Errorf("value is not an enabled option of semantic control %q", id)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	before := runtime.state.Revision()
	if err := runtime.state.SetValues(control.Scope, map[string]any{control.Binding: value}); err != nil {
		return nil, err
	}
	normalized := runtime.state.Values(control.Scope)[control.Binding]
	if runtime.state.Revision() != before {
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
	}
	return normalized, nil
}

func settableControlType(nodeType string) bool {
	switch nodeType {
	case "toggle", "checkbox", "radio_group", "tabs", "select", "slider", "stepper":
		return true
	default:
		return false
	}
}

func enabledChoiceValue(root *semantic.Node, value any) bool {
	for _, node := range semantic.Flatten(root) {
		if (node.Type == "radio" || node.Type == "tab" || node.Type == "option") && node.Enabled && equalControlValue(node.Value, value) {
			return true
		}
	}
	return false
}

func equalControlValue(left, right any) bool {
	leftNumber, leftOK := runtimeNumber(left)
	rightNumber, rightOK := runtimeNumber(right)
	if leftOK || rightOK {
		return leftOK && rightOK && leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

func runtimeNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case float64:
		return value, true
	default:
		return 0, false
	}
}

// SetStateValues atomically updates one visible lexical state scope.
func (runtime *Runtime) SetStateValues(scope string, values map[string]any) error {
	tree, err := runtime.RuntimeTree()
	if err != nil {
		return err
	}
	visible := false
	for _, node := range semantic.Flatten(tree) {
		if node.Visible && node.Scope == scope {
			visible = true
			break
		}
	}
	if !visible {
		return fmt.Errorf("state scope %q is not visible", scope)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.state.SetValues(scope, values); err != nil {
		return err
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return nil
}

// ResetStateScope resets one visible lexical scope.
func (runtime *Runtime) ResetStateScope(scope string) error {
	if scope == "" {
		runtime.ResetState()
		return nil
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		return err
	}
	visible := false
	for _, node := range semantic.Flatten(tree) {
		if node.Visible && node.Scope == scope {
			visible = true
			break
		}
	}
	if !visible {
		return fmt.Errorf("state scope %q is not visible", scope)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.state.ResetScope(scope); err != nil {
		return err
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return nil
}

// ScrollSemanticID changes one visible scroll node and clamps it to content extents.
func (runtime *Runtime) ScrollSemanticID(id, mode string, x, y int) error {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return fmt.Errorf("no valid runtime tree is available")
	}
	result := render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot))
	var semanticNode *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.ID == id {
			semanticNode = node
			break
		}
	}
	if semanticNode == nil || semanticNode.Type != "scroll" || !semanticNode.Visible || semanticNode.Bounds == nil || len(semanticNode.Children) != 1 || semanticNode.Children[0].Bounds == nil {
		return fmt.Errorf("semantic node %q is not a visible scroll node", id)
	}
	var source *project.Node
	var find func(*project.Node)
	find = func(node *project.Node) {
		if node == nil || source != nil {
			return
		}
		if node.Handle == semanticNode.Handle {
			source = node
			return
		}
		for _, child := range node.Children {
			find(child)
		}
	}
	find(snapshot.Root)
	if source == nil {
		return fmt.Errorf("scroll source for %q is unavailable", id)
	}
	axis, _ := source.Props["axis"].(string)
	if axis == "" {
		axis = "vertical"
	}
	key := project.ScrollKey(source)
	current := snapshot.Scroll[key]
	if mode == "by" {
		x += current.X
		y += current.Y
	} else if mode != "to" {
		return fmt.Errorf("scroll mode must be by or to")
	}
	if axis == "horizontal" {
		if y != 0 {
			return fmt.Errorf("horizontal scroll does not accept a vertical offset")
		}
		maximum := max(0, semanticNode.Children[0].Bounds.Width-semanticNode.Bounds.Width)
		runtime.SetScrollOffset(key, axis, min(max(0, x), maximum))
	} else {
		if x != 0 {
			return fmt.Errorf("vertical scroll does not accept a horizontal offset")
		}
		maximum := max(0, semanticNode.Children[0].Bounds.Height-semanticNode.Bounds.Height)
		runtime.SetScrollOffset(key, axis, min(max(0, y), maximum))
	}
	return nil
}

func (runtime *Runtime) PublishTree(tree *semantic.Node) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.publishedTree = tree
}

func (runtime *Runtime) Inspect(hostMode string) ([]byte, string, error) {
	snapshot := runtime.Snapshot()
	envelope := semantic.Envelope{
		SchemaVersion: 1, Document: snapshot.Document, HostMode: hostMode,
		Valid: !snapshot.Invalid, Diagnostics: snapshot.Diagnostics,
		RuntimeRevision:     snapshot.RuntimeRevision,
		AvailableSelections: append([]string(nil), snapshot.Screens...),
		Viewport:            semantic.Viewport{Width: snapshot.Viewport.X, Height: snapshot.Viewport.Y},
		CanBack:             snapshot.CanBack, CanForward: snapshot.CanForward,
	}
	if envelope.Diagnostics == nil {
		envelope.Diagnostics = []document.Diagnostic{}
	}
	if envelope.AvailableSelections == nil {
		envelope.AvailableSelections = []string{}
	}
	if snapshot.Kind == document.KindApp {
		envelope.CurrentScreen = snapshot.Screen
	} else if snapshot.Kind == document.KindComponent {
		envelope.CurrentFixture = snapshot.Screen
	}
	if snapshot.Root != nil {
		if hostMode != "headless" {
			runtime.mu.RLock()
			envelope.Root = runtime.publishedTree
			runtime.mu.RUnlock()
		}
		if envelope.Root == nil {
			envelope.Root = render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot)).Tree
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	warning := ""
	if snapshot.Invalid && snapshot.Root != nil {
		warning = "source is invalid; inspected the last-good frame"
	}
	return encoded, warning, nil
}

func (runtime *Runtime) SessionHandler(hostMode string, focus func()) session.Handler {
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
		case "inspect":
			data, warning, err := runtime.Inspect(hostMode)
			if err != nil {
				return session.Response{Error: err.Error()}
			}
			return session.Response{OK: true, Warning: warning, Data: data}
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

func scrollNamesByScreen(loaded *project.Loaded) map[string]map[string]bool {
	result := make(map[string]map[string]bool, len(loaded.Screens))
	for screen, root := range loaded.Screens {
		names := make(map[string]bool)
		var walk func(*project.Node)
		walk = func(node *project.Node) {
			if node == nil {
				return
			}
			if node.Type == "scroll" && node.Name != "" {
				names[project.ScrollKey(node)] = true
			}
			for _, child := range node.Children {
				walk(child)
			}
		}
		walk(root)
		result[screen] = names
	}
	return result
}

func (runtime *Runtime) pruneScroll(loaded *project.Loaded) {
	names := make(map[string]bool)
	var walk func(*project.Node)
	walk = func(node *project.Node) {
		if node == nil {
			return
		}
		if node.Type == "scroll" && node.Name != "" {
			names[project.ScrollKey(node)] = true
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
