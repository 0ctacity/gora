package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"gora/internal/automation"
	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/navigation"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/scrollinput"
	"gora/internal/semantic"
	"gora/internal/session"
)

type Snapshot struct {
	Root                      *project.Node
	Viewport                  image.Point
	Screen                    string
	Screens                   []string
	Invalid                   bool
	Diagnostics               []document.Diagnostic
	Scroll                    map[string]image.Point
	Transient                 interaction.Transient
	StateValues               map[string]map[string]any
	Editing                   map[string]interaction.EditingState
	EditingRevision           uint64
	Revision                  uint64
	NavigationRevision        uint64
	HasState                  bool
	CanBack                   bool
	CanForward                bool
	Kind                      document.Kind
	Document                  string
	RuntimeRevision           uint64
	FrameRevision             uint64
	GeometryRevision          uint64
	PublishedRuntimeRevision  uint64
	PublishedGeometryRevision uint64
	ReloadRevision            uint64
	AutomationInputRevision   uint64
	PublishedValid            bool
	Clock                     ViewClockSnapshot
	ClipboardLength           int
	BlinkVisible              bool
	publicationStreak         uint64
}

// AutomationSnapshot is the immutable, JSON-ready Phase-1 synchronization and
// transient-inspection envelope for one headless view.
type AutomationSnapshot struct {
	SchemaVersion             int    `json:"schema_version"`
	RuntimeRevision           uint64 `json:"runtime_revision"`
	GeometryRevision          uint64 `json:"geometry_revision"`
	FrameRevision             uint64 `json:"frame_revision"`
	PublishedRuntimeRevision  uint64 `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64 `json:"published_geometry_revision"`
	ReloadRevision            uint64 `json:"reload_revision"`
	AutomationInputRevision   uint64 `json:"automation_input_revision"`
	publicationStreak         uint64
	Agreement                 bool                             `json:"agreement"`
	RuntimePublished          bool                             `json:"runtime_published"`
	GeometryPublished         bool                             `json:"geometry_published"`
	Idle                      bool                             `json:"idle"`
	IdleReasons               []string                         `json:"idle_reasons"`
	PendingAutomationInput    bool                             `json:"pending_automation_input"`
	PendingCapture            bool                             `json:"pending_capture"`
	CandidateReload           bool                             `json:"candidate_reload"`
	UnpublishedGeometry       bool                             `json:"unpublished_geometry"`
	Selection                 string                           `json:"selection,omitempty"`
	Selections                []string                         `json:"selections"`
	CanBack                   bool                             `json:"can_back"`
	CanForward                bool                             `json:"can_forward"`
	FocusOrder                []string                         `json:"focus_order"`
	Viewport                  image.Point                      `json:"viewport"`
	Diagnostics               []document.Diagnostic            `json:"diagnostics"`
	Valid                     bool                             `json:"valid"`
	LastGoodAvailable         bool                             `json:"last_good_available"`
	Transient                 interaction.Transient            `json:"transient"`
	Router                    interaction.RouterSnapshot       `json:"router"`
	Editing                   interaction.EditingStoreSnapshot `json:"editing"`
	CurrentFieldID            string                           `json:"current_field_id,omitempty"`
	CurrentField              *interaction.FieldSnapshot       `json:"current_field,omitempty"`
	StateValues               map[string]map[string]any        `json:"state_values"`
	Scroll                    map[string]image.Point           `json:"scroll"`
	QueueSizes                interaction.RouterQueueSizes     `json:"queue_sizes"`
	Clock                     ViewClockSnapshot                `json:"clock"`
	ClockMode                 string                           `json:"clock_mode"`
	ClockTimeMS               int64                            `json:"clock_time_ms"`
	NextTimerMS               *int64                           `json:"next_timer_ms,omitempty"`
	ClipboardLength           int                              `json:"clipboard_length"`
	BlinkVisible              bool                             `json:"blink_visible"`
	EditingHistory            map[string][2]int                `json:"editing_history"`
	publicationStartFrame     uint64
}

type WaitForViewRequest struct {
	AfterFrameRevision    uint64
	AfterFrameSet         bool
	AfterRuntimeRevision  uint64
	AfterRuntimeSet       bool
	Condition             string
	StableFrames          int
	Timeout               time.Duration
	AllowAlreadySatisfied bool
}

var ErrRuntimeClosed = errors.New("runtime is closed")

type WaitTimeoutError struct {
	Snapshot AutomationSnapshot
}

func (e *WaitTimeoutError) Error() string { return "timed out waiting for view publication" }

type Runtime struct {
	mu                        sync.RWMutex
	reloadMu                  sync.Mutex
	root                      string
	entry                     string
	loaded                    *project.Loaded
	selected                  string
	viewport                  image.Point
	viewportExplicit          bool
	diagnostics               []document.Diagnostic
	invalid                   bool
	scroll                    map[string]image.Point
	state                     *interaction.Store
	editing                   *interaction.EditingStore
	navigation                *navigation.History
	navigationRevision        uint64
	runtimeRevision           uint64
	effectiveRoot             *project.Node
	effectiveSource           *project.Node
	effectiveScreen           string
	effectiveRevision         uint64
	effectiveEditingRevision  uint64
	effectiveTransient        interaction.Transient
	publishedTree             *semantic.Node
	publishedScroll           map[string]render.ScrollMetrics
	scrollMetricScale         float64
	trace                     *automation.TraceRecorder
	automationClipboard       string
	clockMode                 string
	clockTimeMS               int64
	nextTimer                 *viewTimer
	timerQueue                []viewTimer
	timerDispatchLog          []string
	timerOrder                uint64
	blinkVisible              bool
	router                    *interaction.Router
	routerSnapshot            interaction.RouterSnapshot
	routerSnapshotSet         bool
	frameRevision             uint64
	geometryRevision          uint64
	publishedRuntimeRevision  uint64
	publishedGeometryRevision uint64
	reloadRevision            uint64
	automationInputRevision   uint64
	publishedValid            bool
	publicationStreak         uint64
	publicationStartFrame     uint64
	syncCh                    chan struct{}
	doneCh                    chan struct{}
	closed                    bool
	candidateReload           bool
}

func newRuntime(root, entry string) *Runtime {
	runtime := &Runtime{
		root: root, entry: entry, scroll: make(map[string]image.Point),
		scrollMetricScale: 1,
		clockMode:         "real", clockTimeMS: time.Now().UnixMilli(), blinkVisible: true,
		trace: automation.NewTraceRecorder(),
		state: interaction.NewStore(), editing: interaction.NewEditingStore(),
		router: interaction.NewRouter(), syncCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	runtime.scheduleBlinkLocked()
	return runtime
}

func NewRuntime(root, entry string) (*Runtime, error) {
	runtime := newRuntime(root, entry)
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
	runtime := newRuntime(root, entry)
	runtime.Reload()
	return runtime
}

func (runtime *Runtime) ensureSyncLocked() {
	if runtime.syncCh == nil {
		runtime.syncCh = make(chan struct{})
	}
	if runtime.doneCh == nil {
		runtime.doneCh = make(chan struct{})
	}
	if runtime.router == nil {
		runtime.router = interaction.NewRouter()
	}
}

// signalLocked wakes all current waiters and replaces the notification
// channel, keeping waiter storage bounded regardless of publication count.
func (runtime *Runtime) signalLocked() {
	runtime.ensureSyncLocked()
	previous := runtime.syncCh
	runtime.syncCh = make(chan struct{})
	close(previous)
}

// Close wakes every automation waiter and releases the runtime's synchronization
// resources. It is idempotent so project/view/server teardown can compose.
func (runtime *Runtime) Close() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.ensureSyncLocked()
	if runtime.closed {
		return
	}
	runtime.closed = true
	runtime.automationClipboard = ""
	runtime.nextTimer = nil
	runtime.timerQueue = nil
	runtime.timerDispatchLog = nil
	if runtime.trace != nil {
		runtime.trace.Close()
	}
	close(runtime.doneCh)
	runtime.signalLocked()
}

// ConfigureEventTrace enables or disables the bounded per-view automation
// trace. Enabling starts a new generation; clearing preserves its identity.
func (runtime *Runtime) ConfigureEventTrace(enabled bool, capacity int) error {
	runtime.mu.Lock()
	if runtime.trace == nil {
		runtime.trace = automation.NewTraceRecorder()
	}
	trace := runtime.trace
	runtime.mu.Unlock()
	return trace.Configure(enabled, capacity)
}

func (runtime *Runtime) ClearEventTrace() {
	runtime.mu.RLock()
	trace := runtime.trace
	runtime.mu.RUnlock()
	if trace != nil {
		trace.Clear()
	}
}

func (runtime *Runtime) EventTrace() automation.TraceSnapshot {
	runtime.mu.RLock()
	trace := runtime.trace
	runtime.mu.RUnlock()
	if trace == nil {
		return automation.TraceSnapshot{Capacity: 512}
	}
	return trace.Snapshot()
}

func (runtime *Runtime) RecordEventTrace(entry automation.TraceEntry) {
	runtime.mu.RLock()
	trace := runtime.trace
	runtime.mu.RUnlock()
	if trace != nil {
		trace.Record(entry)
	}
}

func (runtime *Runtime) Reload() {
	runtime.reloadMu.Lock()
	defer runtime.reloadMu.Unlock()
	runtime.mu.Lock()
	runtime.ensureSyncLocked()
	if runtime.closed {
		runtime.mu.Unlock()
		return
	}
	runtime.candidateReload = true
	runtime.mu.Unlock()
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
	runtime.candidateReload = false
	runtime.reloadRevision++
	if loaded == nil || len(diagnostics) != 0 {
		runtime.diagnostics = append([]document.Diagnostic(nil), diagnostics...)
		runtime.invalid = true
		runtime.runtimeRevision++
		runtime.signalLocked()
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
	if runtime.editing == nil {
		runtime.editing = interaction.NewEditingStore()
	}
	runtime.reconcileEditingLocked()
	runtime.editing.SyncCommitted(runtime.state.AllValues())
	runtime.effectiveRoot = nil
	runtime.pruneScroll(loaded)
	runtime.runtimeRevision++
	runtime.signalLocked()
}

func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.snapshotLocked()
}

func (runtime *Runtime) snapshotLocked() Snapshot {
	runtime.ensureSyncLocked()
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
		FrameRevision: runtime.frameRevision, GeometryRevision: runtime.geometryRevision,
		PublishedRuntimeRevision:  runtime.publishedRuntimeRevision,
		PublishedGeometryRevision: runtime.publishedGeometryRevision,
		ReloadRevision:            runtime.reloadRevision,
		AutomationInputRevision:   runtime.automationInputRevision,
		PublishedValid:            runtime.publishedValid,
		Clock:                     runtime.clockSnapshotLocked(),
		ClipboardLength:           len([]rune(runtime.automationClipboard)),
		BlinkVisible:              runtime.blinkVisible,
		publicationStreak:         runtime.publicationStreak,
	}
	if runtime.editing != nil {
		snapshot.Editing = runtime.editing.States()
		snapshot.EditingRevision = runtime.editing.Revision()
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
	if snapshot.Root != nil && (runtime.effectiveRoot == nil || runtime.effectiveSource != snapshot.Root || runtime.effectiveScreen != runtime.selected || runtime.effectiveRevision != snapshot.Revision || runtime.effectiveEditingRevision != snapshot.EditingRevision || transientGeometryChanged) {
		runtime.effectiveSource = snapshot.Root
		runtime.effectiveScreen = runtime.selected
		runtime.effectiveRevision = snapshot.Revision
		runtime.effectiveEditingRevision = snapshot.EditingRevision
		runtime.effectiveTransient = snapshot.Transient
		if snapshot.Transient.OpenSelect != "" {
			runtime.effectiveRoot = interaction.ResolveTreeWithFields(snapshot.Root, snapshot.StateValues, snapshot.Transient, snapshot.Editing, snapshot.Screen)
		} else {
			runtime.effectiveRoot = interaction.ResolvePersistentTreeWithFields(snapshot.Root, snapshot.StateValues, snapshot.Editing, snapshot.Screen)
		}
		runtime.geometryRevision++
	}
	snapshot.Root = runtime.effectiveRoot
	snapshot.GeometryRevision = runtime.geometryRevision
	return snapshot
}

// AutomationSnapshot returns the latest immutable synchronization and
// transient-state view. It never consumes router or editing queues.
func (runtime *Runtime) AutomationSnapshot() AutomationSnapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.automationSnapshotLocked(runtime.snapshotLocked())
}

func (runtime *Runtime) automationSnapshotLocked(snapshot Snapshot) AutomationSnapshot {
	routerSnapshot := runtime.routerSnapshot
	if !runtime.routerSnapshotSet {
		routerSnapshot = runtime.router.Snapshot()
	}
	editingSnapshot := interaction.EditingStoreSnapshot{Fields: map[string]interaction.FieldSnapshot{}}
	if runtime.editing != nil {
		editingSnapshot = runtime.editing.Snapshot()
	}
	result := AutomationSnapshot{
		SchemaVersion:             1,
		RuntimeRevision:           snapshot.RuntimeRevision,
		GeometryRevision:          snapshot.GeometryRevision,
		FrameRevision:             snapshot.FrameRevision,
		PublishedRuntimeRevision:  snapshot.PublishedRuntimeRevision,
		PublishedGeometryRevision: snapshot.PublishedGeometryRevision,
		ReloadRevision:            snapshot.ReloadRevision,
		AutomationInputRevision:   snapshot.AutomationInputRevision,
		publicationStreak:         snapshot.publicationStreak,
		Selection:                 snapshot.Screen,
		Selections:                append([]string(nil), snapshot.Screens...),
		CanBack:                   snapshot.CanBack,
		CanForward:                snapshot.CanForward,
		FocusOrder:                focusOrderIDs(runtime.publishedTree),
		Viewport:                  snapshot.Viewport,
		Diagnostics:               append([]document.Diagnostic(nil), snapshot.Diagnostics...),
		Valid:                     !snapshot.Invalid,
		LastGoodAvailable:         snapshot.Root != nil,
		Transient: interaction.Transient{
			Focused: routerSnapshot.FocusedID, OpenSelect: routerSnapshot.OpenSelectID,
		},
		Router:                routerSnapshot,
		Editing:               editingSnapshot,
		StateValues:           cloneStateValues(snapshot.StateValues),
		Scroll:                cloneScroll(snapshot.Scroll),
		QueueSizes:            routerSnapshot.QueueSizes,
		Clock:                 runtime.clockSnapshotLocked(),
		ClockMode:             runtime.clockMode,
		ClockTimeMS:           runtime.clockTimeMS,
		ClipboardLength:       len([]rune(runtime.automationClipboard)),
		BlinkVisible:          runtime.blinkVisible,
		EditingHistory:        map[string][2]int{},
		IdleReasons:           []string{},
		publicationStartFrame: runtime.publicationStartFrame,
	}
	result.NextTimerMS = result.Clock.NextTimerMS
	for id := range editingSnapshot.Fields {
		if undo, redo, ok := runtime.editing.HistoryDepth(id); ok {
			result.EditingHistory[id] = [2]int{undo, redo}
		}
	}
	if len(routerSnapshot.HoveredIDs) != 0 {
		result.Transient.Hovered = routerSnapshot.HoveredIDs[0]
	}
	if len(routerSnapshot.PressedIDs) != 0 {
		result.Transient.Pressed = routerSnapshot.PressedIDs[0]
	}
	if len(routerSnapshot.ActiveIDs) != 0 {
		result.Transient.ActiveOption = routerSnapshot.ActiveIDs[0]
	}
	result.RuntimePublished = runtime.publishedTree != nil && runtime.publishedValid && !runtime.invalid && !runtime.candidateReload && runtime.publishedRuntimeRevision == runtime.runtimeRevision
	result.GeometryPublished = result.RuntimePublished && runtime.publishedGeometryRevision == runtime.geometryRevision
	result.Agreement = result.RuntimePublished && result.GeometryPublished
	if !result.Agreement {
		// A stale or invalid candidate cannot contribute to the next stable
		// publication streak, even though the last-good frame remains retained.
		result.publicationStreak = 0
		result.publicationStartFrame = 0
	}
	result.CandidateReload = runtime.candidateReload
	result.UnpublishedGeometry = !result.GeometryPublished
	result.PendingAutomationInput = routerSnapshot.QueueSizes.ValueChanges != 0 || routerSnapshot.QueueSizes.ScrollChanges != 0
	// Capture requests are completed synchronously by the existing runtime and
	// therefore have no pending queue in this phase. Keep the explicit field in
	// the envelope so a future asynchronous capture path can remain compatible.
	result.PendingCapture = false
	if routerSnapshot.FocusedID != "" {
		if field, ok := editingSnapshot.Fields[routerSnapshot.FocusedID]; ok {
			result.CurrentFieldID = routerSnapshot.FocusedID
			copy := field
			copy.Focused = true
			copy.Issues = append([]interaction.ValidationIssue(nil), field.Issues...)
			result.CurrentField = &copy
		}
	}
	if runtime.closed {
		result.IdleReasons = append(result.IdleReasons, "closed")
	}
	if result.CandidateReload {
		result.IdleReasons = append(result.IdleReasons, "candidate_reload")
	}
	if !result.Agreement {
		result.IdleReasons = append(result.IdleReasons, "unpublished_frame")
	}
	if snapshot.Invalid {
		result.IdleReasons = append(result.IdleReasons, "invalid_source")
	}
	if result.PendingAutomationInput {
		result.IdleReasons = append(result.IdleReasons, "pending_automation_input")
	}
	if result.PendingCapture {
		result.IdleReasons = append(result.IdleReasons, "pending_capture")
	}
	result.Idle = len(result.IdleReasons) == 0
	return result
}

// CurrentTransient returns the runtime-owned interaction state without
// consuming any router or editing queues. Automation drivers use it to
// reconcile navigation/select changes after applying an activation.
func (runtime *Runtime) CurrentTransient() interaction.Transient {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.state == nil {
		return interaction.Transient{}
	}
	return runtime.state.Transient()
}

func cloneStateValues(values map[string]map[string]any) map[string]map[string]any {
	if values == nil {
		return map[string]map[string]any{}
	}
	result := make(map[string]map[string]any, len(values))
	for scope, entries := range values {
		copy := make(map[string]any, len(entries))
		for name, value := range entries {
			copy[name] = value
		}
		result[scope] = copy
	}
	return result
}

func focusOrderIDs(root *semantic.Node) []string {
	if root == nil {
		return []string{}
	}
	type focusNode struct {
		id    string
		order int
		index int
	}
	items := make([]focusNode, 0)
	for index, node := range semantic.Flatten(root) {
		if node == nil || node.ID == "" || !node.Visible || !node.Enabled || node.Bounds == nil || node.FocusOrder < 0 {
			continue
		}
		items = append(items, focusNode{id: node.ID, order: node.FocusOrder, index: index})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].order != items[j].order {
			return items[i].order < items[j].order
		}
		return items[i].index < items[j].index
	})
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.id
	}
	return result
}

// WaitForView waits on publication notifications rather than polling. The
// caller may request an immediate result for explicitly satisfied revisions;
// omitted revisions establish a current-frame barrier.
func (runtime *Runtime) WaitForView(ctx context.Context, request WaitForViewRequest) (AutomationSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Condition == "" {
		request.Condition = "published"
	}
	if request.Condition != "published" && request.Condition != "idle" {
		return runtime.AutomationSnapshot(), fmt.Errorf("unknown wait condition %q", request.Condition)
	}
	if request.StableFrames < 0 {
		return runtime.AutomationSnapshot(), fmt.Errorf("stable_frames must be non-negative")
	}
	if request.StableFrames == 0 {
		request.StableFrames = 1
	}
	if request.Timeout <= 0 {
		request.Timeout = 5 * time.Second
	}
	if request.Timeout > 60*time.Second {
		request.Timeout = 60 * time.Second
	}
	initial := runtime.AutomationSnapshot()
	baselineFrame := initial.FrameRevision
	if request.AfterFrameSet {
		baselineFrame = request.AfterFrameRevision
	}
	baselineRuntime := initial.RuntimeRevision
	if request.AfterRuntimeSet {
		baselineRuntime = request.AfterRuntimeRevision
	}
	requireNewFrame := !request.AllowAlreadySatisfied && !request.AfterFrameSet && !request.AfterRuntimeSet
	if request.AfterFrameSet || request.AfterRuntimeSet {
		requireNewFrame = !request.AllowAlreadySatisfied
	}
	lastFrame := initial.FrameRevision
	baselineStreak := initial.publicationStreak
	stable := waitPublicationCount(initial, request, baselineFrame, baselineRuntime, baselineStreak, requireNewFrame)
	if stable >= request.StableFrames && (request.AllowAlreadySatisfied || request.AfterFrameSet || request.AfterRuntimeSet) {
		return initial, nil
	}
	deadline := time.NewTimer(request.Timeout)
	defer deadline.Stop()
	for {
		current := runtime.AutomationSnapshot()
		if current.FrameRevision != lastFrame {
			lastFrame = current.FrameRevision
			stable = waitPublicationCount(current, request, baselineFrame, baselineRuntime, baselineStreak, requireNewFrame)
		} else if stable == 0 && !requireNewFrame {
			stable = waitPublicationCount(current, request, baselineFrame, baselineRuntime, baselineStreak, false)
		}
		if stable >= request.StableFrames {
			return current, nil
		}
		runtime.mu.RLock()
		notification := runtime.syncCh
		done := runtime.doneCh
		closed := runtime.closed
		frameAtChannel := runtime.frameRevision
		runtime.mu.RUnlock()
		if closed {
			return current, ErrRuntimeClosed
		}
		// Close-over the channel and frame under the same lock. If a publication
		// won the race between the snapshot read and this lock, loop once before
		// blocking so no notification can be missed.
		if frameAtChannel != current.FrameRevision {
			continue
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-deadline.C:
			return current, &WaitTimeoutError{Snapshot: current}
		case <-done:
			return current, ErrRuntimeClosed
		case <-notification:
		}
	}
}

// waitPublicationCount derives the number of consecutive matching publication
// transitions available in the current bounded snapshot. publicationStreak is
// reset by PublishFrame whenever the runtime/geometry revision changes or a
// candidate is invalid, so a burst of frames remains countable even when a
// waiter is not scheduled between notifications.
func waitPublicationCount(snapshot AutomationSnapshot, request WaitForViewRequest, baselineFrame, baselineRuntime, baselineStreak uint64, requireNewFrame bool) int {
	if !automationWaitMatches(snapshot, request, baselineFrame, baselineRuntime, requireNewFrame) {
		return 0
	}
	if snapshot.FrameRevision <= baselineFrame {
		return 1
	}
	if snapshot.publicationStreak == 0 {
		return 0
	}
	if snapshot.publicationStartFrame != 0 {
		floor := baselineFrame
		if snapshot.publicationStartFrame > 0 && snapshot.publicationStartFrame-1 > floor {
			floor = snapshot.publicationStartFrame - 1
		}
		if snapshot.FrameRevision <= floor {
			return 1
		}
		count := snapshot.FrameRevision - floor
		if count > snapshot.publicationStreak {
			count = snapshot.publicationStreak
		}
		return int(count)
	}
	if baselineStreak > 0 && snapshot.publicationStreak > baselineStreak {
		return int(snapshot.publicationStreak - baselineStreak)
	}
	return int(snapshot.publicationStreak)
}

func automationWaitMatches(snapshot AutomationSnapshot, request WaitForViewRequest, baselineFrame, baselineRuntime uint64, requireNewFrame bool) bool {
	if requireNewFrame && snapshot.FrameRevision <= baselineFrame {
		return false
	}
	if request.AfterFrameSet && snapshot.FrameRevision < request.AfterFrameRevision {
		return false
	}
	if request.AfterRuntimeSet && snapshot.PublishedRuntimeRevision < request.AfterRuntimeRevision {
		return false
	}
	if !request.AfterRuntimeSet && request.AfterFrameSet && snapshot.PublishedRuntimeRevision < baselineRuntime {
		return false
	}
	if request.Condition == "idle" {
		return snapshot.Idle
	}
	return snapshot.Agreement
}

func (runtime *Runtime) reconcileEditingLocked() {
	if runtime.loaded == nil || runtime.editing == nil || runtime.state == nil {
		return
	}
	values := runtime.state.AllValues()
	var specs []interaction.FieldSpec
	collect := func(root *project.Node, selection string) {
		var walk func(*project.Node)
		walk = func(node *project.Node) {
			if node == nil {
				return
			}
			if node.Type == "text_field" || node.Type == "text_area" {
				declaration := document.StateDeclaration{}
				if node.BindingState != nil {
					declaration = *node.BindingState
				}
				spec := interaction.FieldSpec{
					ID: semantic.StableID(node, selection), Scope: node.Scope, Binding: node.Binding,
					Type: declaration.Type, Multiline: node.Type == "text_area", Value: values[node.Scope][node.Binding], Declaration: declaration,
					Disabled:  resolvedRuntimeBool(node.Props["disabled"], values),
					Required:  resolvedRuntimeBool(node.Props["required"], values),
					MinLength: resolvedRuntimeInt(node.Props["min_length"]), MaxLength: resolvedRuntimeInt(node.Props["max_length"]),
					MinLines: resolvedRuntimeInt(node.Props["min_lines"]), MaxLines: resolvedRuntimeInt(node.Props["max_lines"]),
				}
				spec.Pattern, spec.HasPattern = node.Props["pattern"].(string)
				specs = append(specs, spec)
			}
			for _, child := range node.Children {
				walk(child)
			}
		}
		walk(root)
	}
	if runtime.loaded.Document.Kind == document.KindApp {
		for name, root := range runtime.loaded.Screens {
			collect(root, name)
		}
	} else {
		collect(runtime.loaded.Root, runtime.loaded.Selected)
	}
	runtime.editing.Reconcile(specs)
}

func resolvedRuntimeBool(value any, values map[string]map[string]any) bool {
	if reference, ok := value.(project.StateReference); ok {
		resolved, _ := values[reference.Scope][reference.Name].(bool)
		return resolved
	}
	resolved, _ := value.(bool)
	return resolved
}

func resolvedRuntimeInt(value any) *int {
	var number float64
	switch typed := value.(type) {
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float64:
		number = typed
	default:
		return nil
	}
	if number < 0 || number != float64(int(number)) {
		return nil
	}
	result := int(number)
	return &result
}

func (runtime *Runtime) syncEditingFromStateLocked() {
	if runtime.editing == nil || runtime.state == nil {
		return
	}
	runtime.reconcileEditingLocked()
	runtime.editing.SyncCommitted(runtime.state.AllValues())
}

func (runtime *Runtime) publishValidFieldDraftLocked(id string) error {
	if runtime.editing == nil || runtime.state == nil {
		return nil
	}
	spec, value, ok := runtime.editing.PublishableValue(id)
	if !ok {
		return nil
	}
	if err := runtime.state.SetValues(spec.Scope, map[string]any{spec.Binding: value}); err != nil {
		return err
	}
	normalized := runtime.state.Values(spec.Scope)[spec.Binding]
	if err := runtime.editing.AcceptPublished(id, normalized); err != nil {
		return err
	}
	runtime.editing.SyncCommitted(runtime.state.AllValues())
	return nil
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
		runtime.ensureSyncLocked()
		runtime.router.SyncTransient(transient)
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
	if changed {
		runtime.syncEditingFromStateLocked()
	}
	if navigationAction != nil && runtime.applyNavigationLocked(navigationAction) {
		changed = true
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

func (runtime *Runtime) applyNavigationLocked(action *document.Action) bool {
	if action == nil || runtime.navigation == nil || runtime.loaded == nil || runtime.loaded.Document.Kind != document.KindApp {
		return false
	}
	var transition navigation.Transition
	currentScroll := cloneScroll(runtime.scroll)
	switch action.Action {
	case "navigate":
		transition = runtime.navigation.Navigate(action.To, currentScroll)
	case "replace":
		transition = runtime.navigation.Replace(action.To, currentScroll)
	case "back":
		transition = runtime.navigation.Back(currentScroll)
	case "forward":
		transition = runtime.navigation.Forward(currentScroll)
	}
	if !transition.Changed {
		return false
	}
	runtime.selected = transition.Screen
	runtime.scroll = cloneScroll(transition.Scroll)
	if runtime.scroll == nil {
		runtime.scroll = make(map[string]image.Point)
	}
	runtime.state.SetTransient(interaction.Transient{})
	runtime.navigationRevision++
	return true
}

func (runtime *Runtime) SetTransient(transient interaction.Transient) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	if runtime.state.Transient() != transient {
		previous := runtime.state.Transient()
		if previous.Focused != "" && previous.Focused != transient.Focused && runtime.editing != nil {
			if field := semanticNodeByHandle(runtime.publishedTree, previous.Focused); field != nil && field.Role == "textbox" {
				if runtime.editing.Touch(field.ID) {
					_ = runtime.publishValidFieldDraftLocked(field.ID)
				}
			}
		}
		runtime.state.SetTransient(transient)
		if previous.Focused != transient.Focused && transient.Focused != "" {
			runtime.revealFocusedLocked(transient.Focused)
		}
		if previous.OpenSelect != transient.OpenSelect || previous.ActiveOption != transient.ActiveOption {
			runtime.effectiveRoot = nil
		}
		runtime.runtimeRevision++
	}
}

func (runtime *Runtime) revealFocusedLocked(handle string) {
	var walk func(*semantic.Node) (*semantic.Rect, bool)
	walk = func(node *semantic.Node) (*semantic.Rect, bool) {
		if node == nil {
			return nil, false
		}
		if node.Handle == handle && node.Bounds != nil {
			if node.Role == "textbox" {
				for _, child := range node.Children {
					if child != nil && child.Type == "field_box" && child.Bounds != nil {
						caret := render.FieldCaretRect(child.Props, child.Bounds.ImageRectangle())
						if !caret.Empty() {
							return &semantic.Rect{X: caret.Min.X, Y: caret.Min.Y, Width: caret.Dx(), Height: caret.Dy()}, true
						}
					}
				}
			}
			return node.Bounds, true
		}
		for _, child := range node.Children {
			bounds, found := walk(child)
			if !found {
				continue
			}
			if node.Type == "scroll" && node.Bounds != nil {
				axis, _ := node.Props["axis"].(string)
				if axis == "" {
					axis = "vertical"
				}
				key := semanticScrollKey(node)
				offset := runtime.scroll[key]
				previousOffset := offset
				viewport := node.Bounds.ImageRectangle()
				enabledX := axis == "horizontal" || axis == "both"
				enabledY := axis == "vertical" || axis == "both"
				metrics, published := runtime.publishedScroll[node.Handle]
				if published {
					viewport = metrics.Viewport
					enabledX = metrics.EnabledX
					enabledY = metrics.EnabledY
				}
				if enabledX {
					if bounds.X < viewport.Min.X {
						offset.X = max(0, offset.X-(viewport.Min.X-bounds.X))
					} else if bounds.X+bounds.Width > viewport.Max.X {
						offset.X += bounds.X + bounds.Width - viewport.Max.X
					}
				}
				if enabledY {
					if bounds.Y < viewport.Min.Y {
						offset.Y = max(0, offset.Y-(viewport.Min.Y-bounds.Y))
					} else if bounds.Y+bounds.Height > viewport.Max.Y {
						offset.Y += bounds.Y + bounds.Height - viewport.Max.Y
					}
				}
				if published {
					offset = clampScrollPoint(offset, metrics)
				} else if len(node.Children) == 1 && node.Children[0] != nil && node.Children[0].Bounds != nil {
					if enabledX {
						offset.X = min(offset.X, max(0, node.Children[0].Bounds.Width-node.Bounds.Width))
					}
					if enabledY {
						offset.Y = min(offset.Y, max(0, node.Children[0].Bounds.Height-node.Bounds.Height))
					}
				}
				runtime.scroll[key] = offset
				adjusted := *bounds
				adjusted.X -= offset.X - previousOffset.X
				adjusted.Y -= offset.Y - previousOffset.Y
				bounds = &adjusted
			}
			return bounds, true
		}
		return nil, false
	}
	_, _ = walk(runtime.publishedTree)
}

func semanticScrollKey(node *semantic.Node) string {
	if node.Name == "" {
		return node.Handle
	}
	parts := make([]string, 0, len(node.Breadcrumb)+1)
	for _, segment := range node.Breadcrumb {
		parts = append(parts, url.PathEscape(segment))
	}
	parts = append(parts, url.PathEscape(node.Name))
	return strings.Join(parts, "/")
}

func (runtime *Runtime) ResetState() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state != nil {
		runtime.state.ResetContext(runtime.selected)
		runtime.syncEditingFromStateLocked()
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
		runtime.replaceEditingDraftsForSelectionLocked(screen)
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

func (runtime *Runtime) replaceEditingDraftsForSelectionLocked(selection string) {
	if runtime.loaded == nil || runtime.editing == nil || runtime.state == nil {
		return
	}
	root := runtime.loaded.Root
	if runtime.loaded.Document.Kind == document.KindApp {
		root = runtime.loaded.Screens[selection]
	}
	values := runtime.state.AllValues()
	var walk func(*project.Node)
	walk = func(node *project.Node) {
		if node == nil {
			return
		}
		if node.Type == "text_field" || node.Type == "text_area" {
			_ = runtime.editing.ReplaceCommitted(semantic.StableID(node, selection), values[node.Scope][node.Binding])
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
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
	if runtime.scroll == nil {
		runtime.scroll = make(map[string]image.Point)
	}
	offset := runtime.scroll[key]
	if axis == "horizontal" {
		offset.X = max(0, value)
	} else {
		offset.Y = max(0, value)
	}
	if offset == runtime.scroll[key] {
		return
	}
	runtime.scroll[key] = offset
	runtime.runtimeRevision++
}

func (runtime *Runtime) Capture(path string, scale int) (string, error) {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return "", fmt.Errorf("no valid frame is available")
	}
	if !snapshot.Invalid {
		if err := runtime.ensurePublishedFrame(); err != nil {
			return "", err
		}
		snapshot = runtime.Snapshot()
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
	if !snapshot.Invalid {
		if err := runtime.ensurePublishedFrame(); err != nil {
			return nil, "", err
		}
		snapshot = runtime.Snapshot()
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

// CapturePNGReadOnly captures the already-published overlay-free frame without
// rendering or publishing a stale candidate. The returned identity is copied
// under the same read lock as the immutable render snapshot, so later runtime
// mutations cannot make metadata describe a different set of pixels.
func (runtime *Runtime) CapturePNGReadOnly(scale int) ([]byte, string, automation.CaptureIdentity, error) {
	if scale <= 0 {
		return nil, "", automation.CaptureIdentity{}, fmt.Errorf("scale must be a positive integer")
	}
	runtime.mu.RLock()
	root := runtime.effectiveRoot
	if root == nil {
		runtime.mu.RUnlock()
		return nil, "", automation.CaptureIdentity{}, fmt.Errorf("no valid published frame is available")
	}
	stateValues := map[string]map[string]any{}
	if runtime.state != nil {
		stateValues = cloneStateValues(runtime.state.AllValues())
	}
	snapshot := Snapshot{
		Root: root, Viewport: runtime.viewport, Screen: runtime.selected,
		Scroll: cloneScroll(runtime.scroll), StateValues: stateValues,
		Transient: runtime.effectiveTransient, Invalid: runtime.invalid,
	}
	identity := automation.CaptureIdentity{Selection: runtime.selected, ViewportWidth: runtime.viewport.X, ViewportHeight: runtime.viewport.Y, RuntimeRevision: runtime.runtimeRevision, FrameRevision: runtime.frameRevision, GeometryRevision: runtime.geometryRevision, PublishedRuntimeRevision: runtime.publishedRuntimeRevision, PublishedGeometryRevision: runtime.publishedGeometryRevision, Width: runtime.viewport.X, Height: runtime.viewport.Y, Valid: runtime.publishedTree != nil && runtime.publishedValid}
	runtime.mu.RUnlock()
	data, err := render.CapturePNG(snapshot.Root, snapshot.Viewport, renderState(snapshot), scale)
	if err != nil {
		return nil, "", identity, err
	}
	if snapshot.Invalid {
		return data, "source is invalid; captured the last-good frame", identity, nil
	}
	return data, "", identity, nil
}

func (runtime *Runtime) ensurePublishedFrame() error {
	runtime.mu.RLock()
	current := runtime.publishedTree != nil && runtime.publishedValid && !runtime.invalid && runtime.publishedRuntimeRevision == runtime.runtimeRevision && runtime.publishedGeometryRevision == runtime.geometryRevision
	runtime.mu.RUnlock()
	if current {
		return nil
	}
	_, err := runtime.runtimeFrame()
	return err
}

// RuntimeTree builds the canonical headless semantic tree for the current view.
func (runtime *Runtime) RuntimeTree() (*semantic.Node, error) {
	result, err := runtime.runtimeFrame()
	if err != nil {
		return nil, err
	}
	return result.Tree, nil
}

// CurrentRuntimeTree returns the current published tree when it already
// agrees with runtime state, rendering and publishing one frame only when a
// mutation made the publication stale. Renderer-neutral automation uses this
// boundary to avoid creating a frame before every input event.
func (runtime *Runtime) CurrentRuntimeTree() (*semantic.Node, error) {
	return runtime.currentRuntimeTree()
}

func (runtime *Runtime) runtimeFrame() (render.Result, error) {
	snapshot := runtime.Snapshot()
	if snapshot.Root == nil {
		return render.Result{}, fmt.Errorf("no valid runtime tree is available")
	}
	if snapshot.Invalid {
		// An invalid candidate retains the last-good frame. Returning that
		// immutable publication keeps RuntimeTree/inspection callers read-only
		// while diagnostics continue to describe the current source; publishing
		// it again would fabricate a new frame for an invalid document.
		runtime.mu.RLock()
		published := runtime.publishedTree
		runtime.mu.RUnlock()
		if published == nil {
			return render.Result{}, fmt.Errorf("no valid runtime tree is available")
		}
		return render.Result{Tree: published}, nil
	}
	result := render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot))
	runtime.PublishFrame(result.Tree, result.Scroll)
	return result, nil
}

// currentRuntimeFrame returns the last published frame when it already agrees
// with current runtime/geometry revisions. Mutation validation can therefore
// inspect the current semantic tree without creating a pre-mutation frame;
// stale or absent frames are rendered and published once.
func (runtime *Runtime) currentRuntimeFrame() (render.Result, error) {
	runtime.mu.RLock()
	if runtime.publishedTree != nil && runtime.publishedValid && !runtime.invalid && runtime.publishedRuntimeRevision == runtime.runtimeRevision && runtime.publishedGeometryRevision == runtime.geometryRevision {
		result := render.Result{Tree: runtime.publishedTree}
		runtime.mu.RUnlock()
		return result, nil
	}
	runtime.mu.RUnlock()
	return runtime.runtimeFrame()
}

func (runtime *Runtime) currentRuntimeTree() (*semantic.Node, error) {
	result, err := runtime.currentRuntimeFrame()
	return result.Tree, err
}

// ActivateSemanticID performs the completed semantic activation represented by id.
func (runtime *Runtime) ActivateSemanticID(id string) error {
	tree, err := runtime.currentRuntimeTree()
	if err != nil {
		return err
	}
	for _, node := range semantic.Flatten(tree) {
		if node.ID != id {
			continue
		}
		if !node.Visible || !node.InViewport || !node.Enabled {
			return fmt.Errorf("semantic node %q is not activatable", id)
		}
		if node.Type == "select" && node.Visible && node.InViewport && node.Enabled {
			return runtime.Activate(interaction.Activation{OpenSelect: node.Handle, ActiveOption: initialSelectOption(node)})
		}
		if node.Type == "button" {
			if action, _ := node.Props["form_action"].(string); action != "" {
				form := semanticNodeByHandle(tree, node.FormHandle)
				if form == nil {
					return fmt.Errorf("form for semantic node %q is unavailable", id)
				}
				if action == "submit" {
					return runtime.SubmitForm(form.ID)
				}
				return runtime.ResetForm(form.ID)
			}
		}
		if len(node.Actions) == 0 {
			return fmt.Errorf("semantic node %q is not activatable", id)
		}
		return runtime.Activate(interaction.Activation{Scope: node.Scope, Actions: node.Actions})
	}
	return fmt.Errorf("unknown semantic node %q", id)
}

func semanticNodeByHandle(root *semantic.Node, handle string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.Handle == handle {
			return node
		}
	}
	return nil
}

// SetFieldDraft replaces one visible editable field's draft and publishes it
// when the resulting typed value is valid.
func (runtime *Runtime) SetFieldDraft(id, draft string) error {
	tree, err := runtime.currentRuntimeTree()
	if err != nil {
		return err
	}
	var field *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.ID == id {
			field = node
			break
		}
	}
	if field == nil || field.Role != "textbox" {
		return fmt.Errorf("unknown semantic field %q", id)
	}
	if !field.Visible || !field.InViewport || !field.Enabled || field.ReadOnly {
		return fmt.Errorf("semantic field %q is not editable", id)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	before := runtime.editing.Revision()
	if err := runtime.editing.SetDraft(id, draft); err != nil {
		return err
	}
	if runtime.editing.Revision() == before {
		return nil
	}
	if err := runtime.publishValidFieldDraftLocked(id); err != nil {
		return err
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return nil
}

func (runtime *Runtime) ApplyFieldEdit(id string, start, end int, text string) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	start, end, err := runeRangeToGraphemeLocked(runtime.editing, id, start, end)
	if err != nil {
		return err
	}
	return runtime.applyEditCommandLocked(interaction.EditCommand{Kind: interaction.EditReplace, FieldID: id, Start: start, End: end, Text: text})
}

func runeRangeToGraphemeLocked(store *interaction.EditingStore, id string, start, end int) (int, int, error) {
	draft, ok := store.Draft(id)
	if !ok {
		return 0, 0, fmt.Errorf("unknown field %q", id)
	}
	return interaction.GraphemeIndexAtRune(draft, start), interaction.GraphemeIndexAtRune(draft, end), nil
}

func (runtime *Runtime) SetFieldSelection(id string, start, end int) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	start, end, err := runeRangeToGraphemeLocked(runtime.editing, id, start, end)
	if err != nil {
		return err
	}
	return runtime.applyEditCommandLocked(interaction.EditCommand{Kind: interaction.EditSelection, FieldID: id, Start: start, End: end})
}

func (runtime *Runtime) SetFieldComposition(id string, start, end int) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	start, end, err := runeRangeToGraphemeLocked(runtime.editing, id, start, end)
	if err != nil {
		return err
	}
	kind := interaction.EditCompositionStart
	// Gio's native SetComposition treats an equal range on an idle field as
	// the end/no-op signal. Keep that behavior while routing through the shared
	// command path; explicit automation composition_start still supports an
	// empty caret range via its distinct command kind.
	if start == end {
		kind = interaction.EditCompositionCommit
	}
	return runtime.applyEditCommandLocked(interaction.EditCommand{Kind: kind, FieldID: id, Start: start, End: end})
}

func (runtime *Runtime) CancelFieldComposition(id string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return false
	}
	before := runtime.editing.Revision()
	if err := runtime.applyEditCommandLocked(interaction.EditCommand{Kind: interaction.EditCompositionCancel, FieldID: id}); err != nil {
		return false
	}
	return runtime.editing.Revision() != before
}

func (runtime *Runtime) MoveFieldSelection(id, movement string, extend bool) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.editing.MoveSelection(id, movement, extend) {
		return false
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) SetFieldVisualColumns(id string, columns int) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.editing.SetVisualColumns(id, columns) {
		return false
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) ScrollFieldInternal(id string, lines int) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil || !runtime.editing.ScrollInternal(id, lines) {
		return false
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) CanScrollFieldInternal(id string, lines int) bool {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.editing != nil && runtime.editing.CanScrollInternal(id, lines)
}

// commitScrollInput applies field-internal scrolling and document scroll
// offsets under one runtime lock/revision. Native and automation adapters use
// this to preserve diagonal ownership without exposing an intermediate frame.
func (runtime *Runtime) commitScrollInput(fieldID string, fieldLines, fieldColumns int, points map[string]image.Point) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	changed := false
	if runtime.editing != nil && fieldID != "" {
		if fieldColumns > 0 && runtime.editing.SetVisualColumns(fieldID, fieldColumns) {
			changed = true
		}
		if fieldLines != 0 && runtime.editing.ScrollInternal(fieldID, fieldLines) {
			changed = true
		}
	}
	if runtime.scroll == nil {
		runtime.scroll = make(map[string]image.Point)
	}
	for key, offset := range points {
		if key != "" && runtime.scroll[key] != offset {
			changed = true
		}
	}
	if changed {
		for key, offset := range points {
			if key != "" {
				runtime.scroll[key] = offset
			}
		}
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
	}
	return changed
}

func (runtime *Runtime) DeleteFieldSelection(id string, backward, word bool) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.editing.DeleteSelection(id, backward, word) {
		return false
	}
	if err := runtime.publishValidFieldDraftLocked(id); err != nil {
		return false
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) TouchField(id string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.editing.Touch(id) {
		return false
	}
	if err := runtime.publishValidFieldDraftLocked(id); err != nil {
		return false
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) FieldRuneSelection(id string) (int, int, bool) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.editing == nil {
		return 0, 0, false
	}
	return runtime.editing.RuneSelection(id)
}

func (runtime *Runtime) FieldSelectedText(id string) (string, bool) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.editing.SelectedText(id)
}

func (runtime *Runtime) FieldDraft(id string) (string, bool) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.editing.Draft(id)
}

func (runtime *Runtime) UndoField(id string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return false
	}
	before := runtime.editing.Revision()
	if err := runtime.applyEditCommandLocked(interaction.EditCommand{Kind: interaction.EditUndo, FieldID: id}); err != nil {
		return false
	}
	return runtime.editing.Revision() != before
}

func (runtime *Runtime) RedoField(id string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return false
	}
	before := runtime.editing.Revision()
	if err := runtime.applyEditCommandLocked(interaction.EditCommand{Kind: interaction.EditRedo, FieldID: id}); err != nil {
		return false
	}
	return runtime.editing.Revision() != before
}

// SubmitForm validates all enabled descendant fields, publishes their drafts,
// then performs the form's transactional authored submit effects.
func (runtime *Runtime) SubmitForm(id string) error {
	tree, err := runtime.currentRuntimeTree()
	if err != nil {
		return err
	}
	var form *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.ID == id && node.Role == "form" {
			form = node
			break
		}
	}
	if form == nil {
		return fmt.Errorf("unknown semantic form %q", id)
	}
	runtime.mu.Lock()
	changes := make(map[string]map[string]any)
	var firstInvalid *semantic.Node
	var firstValidationError error
	var accepted []struct {
		id    string
		value any
	}
	for _, field := range semantic.Flatten(form) {
		if field.Role != "textbox" || !field.Enabled {
			continue
		}
		value, prepareErr := runtime.editing.PrepareCommit(field.ID)
		if prepareErr != nil {
			if firstInvalid == nil {
				firstInvalid = field
				firstValidationError = prepareErr
			}
			continue
		}
		if changes[field.Scope] == nil {
			changes[field.Scope] = make(map[string]any)
		}
		changes[field.Scope][field.Binding] = value
		accepted = append(accepted, struct {
			id    string
			value any
		}{field.ID, value})
	}
	if firstInvalid != nil {
		transient := runtime.state.Transient()
		transient.Focused = firstInvalid.Handle
		runtime.state.SetTransient(transient)
		runtime.revealFocusedLocked(firstInvalid.Handle)
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
		runtime.mu.Unlock()
		return firstValidationError
	}
	navigationAction, err := runtime.state.ApplyForm(changes, form.Scope, form.Actions)
	if err != nil {
		runtime.mu.Unlock()
		return err
	}
	for _, field := range accepted {
		_ = runtime.editing.AcceptCommitted(field.id, field.value)
	}
	runtime.syncEditingFromStateLocked()
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	runtime.applyNavigationLocked(navigationAction)
	runtime.mu.Unlock()
	return nil
}

// ResetForm resets only states bound to fields beneath the selected form.
func (runtime *Runtime) ResetForm(id string) error {
	tree, err := runtime.currentRuntimeTree()
	if err != nil {
		return err
	}
	var form *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.ID == id && node.Role == "form" {
			form = node
			break
		}
	}
	if form == nil {
		return fmt.Errorf("unknown semantic form %q", id)
	}
	names := make(map[string][]string)
	var ids []string
	for _, field := range semantic.Flatten(form) {
		if field.Role == "textbox" {
			names[field.Scope] = append(names[field.Scope], field.Binding)
			ids = append(ids, field.ID)
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.state.ResetScopedValues(names); err != nil {
		return err
	}
	runtime.editing.Reset(ids, runtime.state.AllValues())
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return nil
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
	tree, err := runtime.currentRuntimeTree()
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
	if control.Type == "text_field" || control.Type == "text_area" {
		value, err = runtime.editing.ValidateCommitted(id, value)
		if err != nil {
			return nil, err
		}
	}
	before := runtime.state.Revision()
	editingBefore := runtime.editing.Revision()
	if err := runtime.state.SetValues(control.Scope, map[string]any{control.Binding: value}); err != nil {
		return nil, err
	}
	normalized := runtime.state.Values(control.Scope)[control.Binding]
	if control.Type == "text_field" || control.Type == "text_area" {
		_ = runtime.editing.ReplaceCommitted(id, normalized)
		runtime.editing.SyncCommitted(runtime.state.AllValues())
	} else {
		runtime.syncEditingFromStateLocked()
	}
	if runtime.state.Revision() != before || runtime.editing.Revision() != editingBefore {
		runtime.effectiveRoot = nil
		runtime.runtimeRevision++
	}
	return normalized, nil
}

func settableControlType(nodeType string) bool {
	switch nodeType {
	case "text_field", "text_area", "toggle", "checkbox", "radio_group", "tabs", "select", "slider", "stepper":
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
	tree, err := runtime.currentRuntimeTree()
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
	runtime.syncEditingFromStateLocked()
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
	tree, err := runtime.currentRuntimeTree()
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
	runtime.syncEditingFromStateLocked()
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	return nil
}

// ScrollSemanticID changes one visible scroll node and clamps it to content extents.
func (runtime *Runtime) ScrollSemanticID(id, mode string, x, y int) error {
	result, err := runtime.currentRuntimeFrame()
	if err != nil {
		return err
	}
	snapshot := runtime.Snapshot()
	var semanticNode *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.ID == id {
			semanticNode = node
			break
		}
	}
	derivedAxis := semanticNode != nil && semanticNode.Type == "scrollbar" && semanticNode.Role == "scrollbar"
	if semanticNode == nil || (!derivedAxis && semanticNode.Type != "scroll") || !semanticNode.Visible || semanticNode.Bounds == nil {
		return fmt.Errorf("semantic node %q is not a visible scroll node", id)
	}
	if derivedAxis && !semanticNode.Enabled {
		return fmt.Errorf("semantic scrollbar %q is disabled", id)
	}
	var source *project.Node
	var find func(*project.Node)
	find = func(node *project.Node) {
		if node == nil || source != nil {
			return
		}
		handle := semanticNode.Handle
		if derivedAxis {
			handle = semanticNode.Group
		}
		if node.Handle == handle {
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
	if derivedAxis {
		axis = semanticNode.Orientation
	}
	metrics, ok := result.Scroll[source.Handle]
	if !ok {
		metrics, ok = runtime.publishedScrollMetrics()[source.Handle]
	}
	if !ok {
		return fmt.Errorf("scroll metrics for %q are unavailable", id)
	}
	key := project.ScrollKey(source)
	current := snapshot.Scroll[key]
	// Validate the caller's axis-local operands before composing with the
	// current point. A derived vertical scrollbar must be able to add Y while
	// preserving a nonzero X owned by the same both-axis scrollport.
	if mode != "by" && mode != "to" {
		return fmt.Errorf("scroll mode must be by or to")
	}
	switch axis {
	case "horizontal":
		if y != 0 {
			return fmt.Errorf("horizontal scroll does not accept a vertical offset")
		}
	case "vertical":
		if x != 0 {
			return fmt.Errorf("vertical scroll does not accept a horizontal offset")
		}
	case "both":
	default:
		return fmt.Errorf("scroll axis %q is unsupported", axis)
	}
	if derivedAxis {
		if mode == "by" {
			if axis == "horizontal" {
				x += current.X
				y = current.Y
			} else {
				x = current.X
				y += current.Y
			}
		} else if axis == "horizontal" {
			y = current.Y
		} else {
			x = current.X
		}
	} else if mode == "by" {
		x += current.X
		y += current.Y
	}
	runtime.setScrollPoint(key, clampScrollPoint(image.Pt(x, y), metrics))
	return nil
}

func (runtime *Runtime) PublishTree(tree *semantic.Node) {
	runtime.PublishFrame(tree, nil)
}

func (runtime *Runtime) PublishFrame(tree *semantic.Node, scroll ...map[string]render.ScrollMetrics) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.ensureSyncLocked()
	nextFrame := runtime.frameRevision + 1
	validPublication := !runtime.invalid && tree != nil
	if validPublication && runtime.publishedValid && runtime.publishedRuntimeRevision == runtime.runtimeRevision && runtime.publishedGeometryRevision == runtime.geometryRevision {
		runtime.publicationStreak++
	} else if validPublication {
		runtime.publicationStreak = 1
		runtime.publicationStartFrame = nextFrame
	} else {
		runtime.publicationStreak = 0
		runtime.publicationStartFrame = 0
	}
	runtime.publishedTree = tree
	var metrics map[string]render.ScrollMetrics
	if len(scroll) > 0 {
		metrics = scroll[0]
	}
	runtime.publishedScroll = cloneScrollMetrics(metrics)
	runtime.frameRevision = nextFrame
	runtime.publishedRuntimeRevision = runtime.runtimeRevision
	runtime.publishedGeometryRevision = runtime.geometryRevision
	runtime.publishedValid = validPublication
	if runtime.state == nil {
		runtime.state = interaction.NewStore()
	}
	runtime.router.SyncTransient(runtime.state.Transient())
	runtime.router.Update(tree)
	runtime.routerSnapshot = cloneRouterSnapshot(runtime.router.Snapshot())
	runtime.routerSnapshotSet = true
	runtime.signalLocked()
}

func (runtime *Runtime) publishedScrollMetrics() map[string]render.ScrollMetrics {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return cloneScrollMetrics(runtime.publishedScroll)
}

func cloneScrollMetrics(in map[string]render.ScrollMetrics) map[string]render.ScrollMetrics {
	if in == nil {
		return nil
	}
	out := make(map[string]render.ScrollMetrics, len(in))
	for key, metrics := range in {
		out[key] = metrics
	}
	return out
}

func clampScrollPoint(offset image.Point, metrics render.ScrollMetrics) image.Point {
	if metrics.EnabledX {
		offset.X = min(max(0, offset.X), metrics.Maximum.X)
	} else {
		offset.X = 0
	}
	if metrics.EnabledY {
		offset.Y = min(max(0, offset.Y), metrics.Maximum.Y)
	} else {
		offset.Y = 0
	}
	return offset
}

// PublishRouterSnapshot stores an immutable copy of the host router's current
// transient state. Studio and app hosts use this after dispatching native
// events so automation inspection observes pointer/keyboard ownership without
// sharing the mutable UI router with the runtime reader. Headless publication
// continues to use the runtime-owned router through PublishFrame.
func (runtime *Runtime) PublishRouterSnapshot(snapshot interaction.RouterSnapshot) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.ensureSyncLocked()
	copy := cloneRouterSnapshot(snapshot)
	if reflect.DeepEqual(runtime.routerSnapshot, copy) && runtime.routerSnapshotSet {
		return
	}
	runtime.routerSnapshot = copy
	runtime.routerSnapshotSet = true
	runtime.automationInputRevision++
	runtime.signalLocked()
}

func cloneRouterSnapshot(snapshot interaction.RouterSnapshot) interaction.RouterSnapshot {
	copy := snapshot
	copy.HoveredIDs = append([]string(nil), snapshot.HoveredIDs...)
	copy.PressedIDs = append([]string(nil), snapshot.PressedIDs...)
	copy.ActiveIDs = append([]string(nil), snapshot.ActiveIDs...)
	copy.DisabledIDs = append([]string(nil), snapshot.DisabledIDs...)
	if snapshot.PointerCapture != nil {
		capture := *snapshot.PointerCapture
		copy.PointerCapture = &capture
	}
	if snapshot.KeyboardPress != nil {
		keyboard := *snapshot.KeyboardPress
		copy.KeyboardPress = &keyboard
	}
	return copy
}

func (runtime *Runtime) setScrollPoint(key string, offset image.Point) bool {
	if key == "" {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.scroll == nil {
		runtime.scroll = make(map[string]image.Point)
	}
	if runtime.scroll[key] == offset {
		return false
	}
	runtime.scroll[key] = offset
	runtime.runtimeRevision++
	return true
}

func (runtime *Runtime) setScrollPoints(points map[string]image.Point) bool {
	if len(points) == 0 {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.scroll == nil {
		runtime.scroll = make(map[string]image.Point)
	}
	changed := false
	for key, offset := range points {
		if key != "" && runtime.scroll[key] != offset {
			changed = true
		}
	}
	if !changed {
		return false
	}
	for key, offset := range points {
		if key != "" {
			runtime.scroll[key] = offset
		}
	}
	runtime.runtimeRevision++
	return true
}

// SetScrollMetricScale records the published native metric used to convert
// physical-pixel wheel deltas before ownership routing. Headless views retain
// the default scale of one.
func (runtime *Runtime) SetScrollMetricScale(scale float64) {
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	runtime.mu.Lock()
	runtime.scrollMetricScale = scale
	runtime.mu.Unlock()
}

// RouteScroll is the renderer-neutral scroll adapter used by automation and
// native hosts. It consumes independent axes through the published chain and
// commits all changed offsets atomically.
func (runtime *Runtime) RouteScroll(event scrollinput.Event) (scrollinput.Outcome, error) {
	if runtime == nil {
		return scrollinput.Outcome{}, fmt.Errorf("runtime is unavailable")
	}
	runtime.mu.RLock()
	if runtime.closed {
		runtime.mu.RUnlock()
		return scrollinput.Outcome{}, ErrRuntimeClosed
	}
	tree := runtime.publishedTree
	metrics := cloneScrollMetrics(runtime.publishedScroll)
	offsets := cloneScroll(runtime.scroll)
	scale := runtime.scrollMetricScale
	runtime.mu.RUnlock()
	if tree == nil {
		return scrollinput.Outcome{}, fmt.Errorf("no published runtime tree is available")
	}
	outcome, err := scrollinput.Normalize(event, scale)
	if err != nil {
		return scrollinput.Outcome{}, err
	}
	outcome.Candidates = scrollCandidateIDs(tree, event.Point)
	if hasCommandModifier(event.Modifiers) {
		outcome.NoFrameReason = "command_modified_headless_scroll_blocked"
		outcome.ResidualX = outcome.LogicalDeltaX
		outcome.ResidualY = outcome.LogicalDeltaY
		outcome.Axes = []scrollinput.AxisResult{{Axis: "x", Residual: outcome.ResidualX, ContainmentStop: true}, {Axis: "y", Residual: outcome.ResidualY, ContainmentStop: true}}
		return outcome, nil
	}
	if outcome.Phase == "cancel" {
		outcome.LogicalDeltaX = 0
		outcome.LogicalDeltaY = 0
		outcome.ResidualX = 0
		outcome.ResidualY = 0
		outcome.NoFrameReason = "phase_cancel"
		outcome.Axes = []scrollinput.AxisResult{{Axis: "x"}, {Axis: "y"}}
		return outcome, nil
	}
	delta := image.Pt(int(outcome.LogicalDeltaX), int(outcome.LogicalDeltaY))
	if delta == (image.Point{}) {
		outcome.NoFrameReason = "phase_only"
		outcome.Axes = []scrollinput.AxisResult{{Axis: "x", Residual: 0}, {Axis: "y", Residual: 0}}
		return outcome, nil
	}
	fieldID, fieldLines, fieldColumns := "", 0, 0
	fieldConsumedY := false
	if owner := topmostScrollPriority(tree, event.Point); owner != nil {
		if owner.Type == "slider" || owner.Role == "slider" {
			outcome.OwnerID = semanticIDOrHandle(owner)
			outcome.ResidualX, outcome.ResidualY = 0, 0
			outcome.Axes = []scrollinput.AxisResult{{Axis: "x", ContainmentStop: true}, {Axis: "y", ContainmentStop: true}}
			outcome.NoFrameReason = "slider_owns_scroll"
			return outcome, nil
		}
		if owner.Type == "text_area" {
			outcome.FieldOwnerID = semanticIDOrHandle(owner)
			outcome.OwnerID = outcome.FieldOwnerID
			fieldID = owner.ID
			if fieldID == "" {
				fieldID = owner.Handle
			}
			fieldLines = int(math.Round(float64(delta.Y) / 16))
			if fieldLines == 0 && delta.Y != 0 {
				fieldLines = 1
				if delta.Y < 0 {
					fieldLines = -1
				}
			}
			if columns, ok := fieldVisualColumns(owner); ok {
				fieldColumns = columns
			}
			fieldConsumedY = delta.Y != 0 && runtime.CanScrollFieldInternal(owner.ID, fieldLines)
			if fieldConsumedY {
				delta.Y = 0
			}
		}
		if fieldID == "" && (owner.Role == "scrollbar" || owner.Type == "scrollbar" || owner.Type == "scrollbar_track" || owner.Type == "scrollbar_thumb") {
			outcome.OwnerID = semanticIDOrHandle(owner)
			// A wheel over a scrollbar is an axis-local semantic scroll shortcut;
			// it never leaks into an unrelated document chain.
			axis := owner.Orientation
			if axis == "" {
				axis = "vertical"
			}
			if axis == "horizontal" {
				delta.Y = 0
			} else {
				delta.X = 0
			}
			if delta != (image.Point{}) {
				if err := runtime.ScrollSemanticID(owner.ID, "by", delta.X, delta.Y); err == nil {
					if axis == "horizontal" {
						outcome.ConsumedX = float64(delta.X)
					} else {
						outcome.ConsumedY = float64(delta.Y)
					}
					outcome.Changed = true
					outcome.ResidualX = float64(int(outcome.LogicalDeltaX) - int(outcome.ConsumedX))
					outcome.ResidualY = float64(int(outcome.LogicalDeltaY) - int(outcome.ConsumedY))
					if axis == "horizontal" {
						outcome.ResidualY = 0
						outcome.Axes = []scrollinput.AxisResult{{Axis: "x", Consumed: outcome.ConsumedX, Residual: 0, ContainmentStop: true, Consumers: []scrollinput.Consumer{{ID: outcome.OwnerID, Axis: "x", Consumed: outcome.ConsumedX}}}, {Axis: "y", Residual: 0, ContainmentStop: true}}
					} else {
						outcome.ResidualX = 0
						outcome.Axes = []scrollinput.AxisResult{{Axis: "x", Residual: 0, ContainmentStop: true}, {Axis: "y", Consumed: outcome.ConsumedY, Residual: 0, ContainmentStop: true, Consumers: []scrollinput.Consumer{{ID: outcome.OwnerID, Axis: "y", Consumed: outcome.ConsumedY}}}}
					}
					outcome.FinalOffsets = cloneScroll(runtime.Snapshot().Scroll)
					return outcome, nil
				}
			}
			if axis == "horizontal" {
				outcome.ResidualY = 0
			} else {
				outcome.ResidualX = 0
			}
			outcome.NoFrameReason = "scrollbar_owns_axis"
			return outcome, nil
		}
	}
	plan := scrollChainPlan{Updates: make(map[string]image.Point), Remaining: delta, Axes: map[string][]scrollinput.Consumer{"x": nil, "y": nil}, Containment: map[string]bool{"x": false, "y": false}}
	if delta != (image.Point{}) {
		plan = planScrollChain(tree, metrics, offsets, event.Point, delta)
	}
	for _, axis := range []string{"x", "y"} {
		consumers := append([]scrollinput.Consumer(nil), plan.Axes[axis]...)
		if axis == "y" && fieldConsumedY {
			consumers = append([]scrollinput.Consumer{{ID: outcome.FieldOwnerID, Axis: "y", Consumed: outcome.LogicalDeltaY}}, consumers...)
		}
		axisResult := scrollinput.AxisResult{Axis: axis, Consumers: consumers, ContainmentStop: plan.Containment[axis]}
		for _, consumer := range consumers {
			axisResult.Consumed += consumer.Consumed
		}
		if axis == "x" {
			axisResult.Residual = float64(plan.Remaining.X)
			outcome.ConsumedX = axisResult.Consumed
			outcome.ResidualX = axisResult.Residual
		} else {
			axisResult.Residual = float64(plan.Remaining.Y)
			outcome.ConsumedY = axisResult.Consumed
			outcome.ResidualY = axisResult.Residual
		}
		outcome.Axes = append(outcome.Axes, axisResult)
	}
	if len(plan.Axes["x"]) != 0 || len(plan.Axes["y"]) != 0 {
		if chain := scrollChainAt(tree, event.Point); len(chain) != 0 {
			outcome.OwnerID = semanticIDOrHandle(chain[len(chain)-1])
			outcome.CanvasOwnerID = outcome.OwnerID
		}
	}
	finalOffsets := cloneScroll(offsets)
	for key, offset := range plan.Updates {
		finalOffsets[key] = offset
	}
	outcome.FinalOffsets = finalOffsets
	fieldDeltaY := 0
	if fieldConsumedY {
		fieldDeltaY = fieldLines
	}
	outcome.Changed = runtime.commitScrollInput(fieldID, fieldDeltaY, fieldColumns, plan.Updates)
	if !outcome.Changed {
		outcome.NoFrameReason = "no_scroll_consumed"
	}
	return outcome, nil
}

func hasCommandModifier(modifiers []string) bool {
	for _, modifier := range modifiers {
		if modifier == "command" || modifier == "control" {
			return true
		}
	}
	return false
}

func scrollCandidateIDs(root *semantic.Node, point image.Point) []string {
	nodes := make([]*semantic.Node, 0)
	for _, node := range semantic.Flatten(root) {
		if node == nil || !node.Visible || !node.InViewport || node.Bounds == nil || node.Clip == nil || !point.In(node.Bounds.ImageRectangle().Intersect(node.Clip.ImageRectangle())) {
			continue
		}
		switch node.Type {
		case "scroll", "text_area", "scrollbar", "scrollbar_track", "scrollbar_thumb", "slider":
			nodes = append(nodes, node)
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].PaintOrder != nodes[j].PaintOrder {
			return nodes[i].PaintOrder > nodes[j].PaintOrder
		}
		return len(nodes[i].Breadcrumb) > len(nodes[j].Breadcrumb)
	})
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, semanticIDOrHandle(node))
	}
	return ids
}

func topmostScrollPriority(root *semantic.Node, point image.Point) *semantic.Node {
	ids := scrollCandidateIDs(root, point)
	if len(ids) == 0 {
		return nil
	}
	for _, node := range semantic.Flatten(root) {
		if semanticIDOrHandle(node) == ids[0] {
			return node
		}
	}
	return nil
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
