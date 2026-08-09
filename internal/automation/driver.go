// Package automation adapts the canonical interaction router to deterministic
// MCP-owned input batches. It deliberately contains no hit testing, layout,
// state reduction, or rendering of its own; those remain owned by the runtime
// and interaction packages.
package automation

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
	"sync"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/scrollinput"
	"gora/internal/semantic"
)

// Runtime is the small renderer-neutral mutation surface used by one Driver.
// studio.Runtime satisfies it without importing this package.
type Runtime interface {
	CurrentRuntimeTree() (*semantic.Node, error)
	Activate(interaction.Activation) error
	SetControlValue(string, any) (any, error)
	ScrollSemanticID(string, string, int, int) error
	SetTransient(interaction.Transient)
	PublishRouterSnapshot(interaction.RouterSnapshot)
}

// ScrollRuntime is implemented by hosts that share the renderer-neutral
// scroll adapter. It is optional so focused router tests can use a minimal
// fake runtime.
type ScrollRuntime interface {
	RouteScroll(scrollinput.Event) (scrollinput.Outcome, error)
}

type TraceRuntime interface {
	RecordEventTrace(TraceEntry)
}

type TraceSnapshotRuntime interface {
	EventTrace() TraceSnapshot
}

// RevisionSnapshot is the bounded revision subset returned with each event
// outcome. The MCP adapter supplies it from studio.Runtime without coupling
// this package to Studio's JSON envelope.
type RevisionSnapshot struct {
	RuntimeRevision           uint64
	FrameRevision             uint64
	GeometryRevision          uint64
	PublishedRuntimeRevision  uint64
	PublishedGeometryRevision uint64
	AutomationInputRevision   uint64
}

type SnapshotFunc func() RevisionSnapshot

// Event is the flat JSON union accepted by gora_dispatch_input. Fields that do
// not apply to the selected type/kind are ignored only after validation.
type Event struct {
	Type      string   `json:"type"`
	Kind      string   `json:"kind,omitempty"`
	PointerID int      `json:"pointer_id,omitempty"`
	Source    string   `json:"source,omitempty"`
	X         float64  `json:"x,omitempty"`
	Y         float64  `json:"y,omitempty"`
	Button    string   `json:"button,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
	TimeMS    float64  `json:"time_ms,omitempty"`
	Name      string   `json:"name,omitempty"`
	Repeat    bool     `json:"repeat,omitempty"`
	DeltaX    float64  `json:"delta_x,omitempty"`
	DeltaY    float64  `json:"delta_y,omitempty"`
	Units     string   `json:"units,omitempty"`
	Phase     string   `json:"phase,omitempty"`
	Momentum  string   `json:"momentum,omitempty"`
}

// Result is one deterministic per-event outcome. Revision fields are filled
// by the MCP adapter after each event's publication.
type Result struct {
	Index                     int                                 `json:"index"`
	Type                      string                              `json:"type"`
	Kind                      string                              `json:"kind"`
	TargetID                  string                              `json:"target_id,omitempty"`
	FocusBefore               string                              `json:"focus_before,omitempty"`
	FocusAfter                string                              `json:"focus_after,omitempty"`
	CaptureBefore             *interaction.PointerCaptureSnapshot `json:"capture_before,omitempty"`
	CaptureAfter              *interaction.PointerCaptureSnapshot `json:"capture_after,omitempty"`
	Consumed                  bool                                `json:"consumed"`
	Activation                *ActivationEffect                   `json:"activation,omitempty"`
	ValueChange               *ValueEffect                        `json:"value_change,omitempty"`
	ScrollChange              *ScrollEffect                       `json:"scroll_change,omitempty"`
	ScrollRouting             *scrollinput.Outcome                `json:"scroll,omitempty"`
	RuntimeRevision           uint64                              `json:"runtime_revision"`
	FrameRevision             uint64                              `json:"frame_revision"`
	GeometryRevision          uint64                              `json:"geometry_revision"`
	PublishedRuntimeRevision  uint64                              `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64                              `json:"published_geometry_revision"`
	AutomationInputRevision   uint64                              `json:"automation_input_revision"`
}

type ActivationEffect struct {
	Scope        string         `json:"scope,omitempty"`
	ActionCount  int            `json:"action_count,omitempty"`
	Actions      []ActionEffect `json:"actions,omitempty"`
	OpenSelect   bool           `json:"open_select,omitempty"`
	CloseSelect  bool           `json:"close_select,omitempty"`
	ActiveOption string         `json:"active_option,omitempty"`
}

type ActionEffect struct {
	Action string `json:"action"`
	State  string `json:"state,omitempty"`
	To     string `json:"to,omitempty"`
	Value  any    `json:"value,omitempty"`
	By     any    `json:"by,omitempty"`
}

type ValueEffect struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

type ScrollEffect struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
	X    int    `json:"x,omitempty"`
	Y    int    `json:"y,omitempty"`
}

// Driver owns exactly one Router and its bounded pointer timeline for one
// MCP view. Calls are serialized so a batch cannot interleave with another
// client operating the same view.
type Driver struct {
	mu             sync.Mutex
	runtime        Runtime
	router         *interaction.Router
	tree           *semantic.Node
	pointers       map[int]pointerState
	closed         bool
	snapshot       SnapshotFunc
	scrollPhase    string
	scrollMomentum string
}

// pointerState is the small amount of pointer timeline state needed to
// validate completion events before they reach the router. Keeping the
// source/button pair prevents a mismatched release from silently abandoning
// a semantic capture.
type pointerState struct {
	source string
	button string
}

func NewDriver(runtime Runtime) *Driver {
	return NewDriverWithSnapshot(runtime, nil)
}

func NewDriverWithSnapshot(runtime Runtime, snapshot SnapshotFunc) *Driver {
	driver := &Driver{runtime: runtime, router: interaction.NewRouter(), pointers: make(map[int]pointerState), snapshot: snapshot}
	return driver
}

// Router exposes a read-only-by-convention router handle for host snapshots
// and focused renderer-neutral tests. Mutation remains serialized by Driver.
func (d *Driver) Router() *interaction.Router {
	if d == nil {
		return nil
	}
	return d.router
}

// Update installs a newly published tree and clears capture/focus that no
// longer exists. Router.Update owns the stale-region reconciliation.
func (d *Driver) Update(tree *semantic.Node) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.tree = tree
	d.pointers = make(map[int]pointerState)
	d.scrollPhase, d.scrollMomentum = "", ""
	d.router.Update(tree)
}

func (d *Driver) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.pointers = make(map[int]pointerState)
		d.scrollPhase, d.scrollMomentum = "", ""
		d.router.SetInspecting(true)
	}
	d.mu.Unlock()
}

// Dispatch validates the complete batch before routing its first event.
func (d *Driver) Dispatch(events []Event) ([]Result, error) {
	if d == nil {
		return nil, fmt.Errorf("automation driver is unavailable")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("automation driver is closed")
	}
	if err := validateBatchWithPointers(events, d.pointers); err != nil {
		return nil, err
	}
	phase, momentum, err := validateScrollTimeline(events, d.scrollPhase, d.scrollMomentum)
	if err != nil {
		return nil, err
	}
	if d.tree == nil && d.runtime != nil {
		tree, err := d.runtime.CurrentRuntimeTree()
		if err != nil {
			return nil, err
		}
		d.tree = tree
		d.router.Update(tree)
	}
	results := make([]Result, 0, len(events))
	for index, event := range events {
		result, err := d.dispatchOne(index, event)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	d.scrollPhase, d.scrollMomentum = phase, momentum
	return results, nil
}

func (d *Driver) dispatchOne(index int, event Event) (Result, error) {
	before := d.router.Snapshot()
	beforeTransient := d.router.Transient()
	beforeRevisions := d.currentRevisions()
	beforeTrace := d.currentTraceRevision()
	result := Result{Index: index, Type: event.Type, Kind: event.Kind, FocusBefore: before.FocusedID, CaptureBefore: cloneCapture(before.PointerCapture)}
	d.recordTrace(TraceEntry{Stage: "accepted", EventIndex: index, Type: event.Type, RuntimeBefore: beforeRevisions.RuntimeRevision, GeometryBefore: beforeRevisions.GeometryRevision, FrameBefore: beforeRevisions.FrameRevision, TraceBefore: beforeTrace})
	var activation interaction.Activation
	var activated bool
	var changed bool
	point := image.Pt(roundLogical(event.X), roundLogical(event.Y))

	switch event.Type {
	case "pointer":
		d.router.SetPointerMetadata(event.Source, pointerButtons(event.Button), point)
		result.TargetID = d.router.HitID(point)
		if before.PointerCapture != nil && before.PointerCapture.PointerID == event.PointerID {
			result.TargetID = before.PointerCapture.OwnerID
		} else if before.PointerCapture != nil {
			// While a pointer owns a control, other pointers are ignored rather
			// than being reported as if they targeted the captured control.
			result.TargetID = ""
		}
		isTouch := event.Source == "touch"
		switch event.Kind {
		case "enter", "move":
			d.router.MovePointer(event.PointerID, point, isTouch)
			result.Consumed = result.TargetID != "" && !isTouch
			if before.PointerCapture != nil && before.PointerCapture.PointerID == event.PointerID {
				result.Consumed = true
			}
		case "leave":
			// A leave must not synthesize a drag to (-1,-1). The router treats
			// this sentinel as an ownership-preserving hover clear.
			d.router.MovePointer(event.PointerID, image.Pt(-1, -1), isTouch)
			result.Consumed = len(before.HoveredIDs) != 0
			if before.PointerCapture != nil && before.PointerCapture.PointerID == event.PointerID {
				result.Consumed = true
			}
		case "press":
			d.pointers[event.PointerID] = pointerState{source: event.Source, button: normalizedButton(event.Button)}
			if event.Button == "primary" {
				result.Consumed = d.router.Press(event.PointerID, point)
			}
		case "release":
			state, ok := d.pointers[event.PointerID]
			if !ok {
				return Result{}, fmt.Errorf("pointer %d was not pressed", event.PointerID)
			}
			if state.source != event.Source || state.button != normalizedButton(event.Button) {
				return Result{}, fmt.Errorf("pointer %d release does not match its press", event.PointerID)
			}
			delete(d.pointers, event.PointerID)
			if event.Button == "primary" {
				activation, activated = d.router.Release(event.PointerID, point)
				result.Consumed = activated
			}
		case "cancel":
			if _, ok := d.pointers[event.PointerID]; !ok {
				return Result{}, fmt.Errorf("pointer %d was not pressed", event.PointerID)
			}
			delete(d.pointers, event.PointerID)
			d.router.Cancel(event.PointerID)
			result.Consumed = true
		}
	case "scroll":
		if runtime, ok := d.runtime.(ScrollRuntime); ok {
			scrollEvent := scrollinput.Event{Source: event.Source, Point: point, DeltaX: event.DeltaX, DeltaY: event.DeltaY, Units: event.Units, Phase: event.Phase, Momentum: event.Momentum, Modifiers: append([]string(nil), event.Modifiers...)}
			routing, err := runtime.RouteScroll(scrollEvent)
			if err != nil {
				return Result{}, err
			}
			result.ScrollRouting = &routing
			result.TargetID = routing.OwnerID
			result.Consumed = routing.ConsumedX != 0 || routing.ConsumedY != 0
			changed = routing.Changed
			d.recordTrace(TraceEntry{Stage: "conversion", EventIndex: index, Type: event.Type, DeltaX: routing.LogicalDeltaX, DeltaY: routing.LogicalDeltaY, ConsumedX: routing.ConsumedX, ConsumedY: routing.ConsumedY, ResidualX: routing.ResidualX, ResidualY: routing.ResidualY})
			d.recordTrace(TraceEntry{Stage: "candidates", EventIndex: index, Type: event.Type, TargetID: routing.OwnerID, IDs: routing.Candidates})
			d.recordTrace(TraceEntry{Stage: "owner_selection", EventIndex: index, Type: event.Type, TargetID: routing.OwnerID, SemanticID: routing.FieldOwnerID})
			d.recordTrace(TraceEntry{Stage: "capture_decision", EventIndex: index, Type: event.Type, TargetID: routing.OwnerID, Outcome: "none"})
			for _, axis := range routing.Axes {
				d.recordTrace(TraceEntry{Stage: "axis_routing", EventIndex: index, Type: event.Type, Axis: axis.Axis, Consumed: axis.Consumed, Residual: axis.Residual, Outcome: fmt.Sprintf("containment=%t", axis.ContainmentStop)})
			}
		} else {
			return Result{}, fmt.Errorf("automation runtime does not support scroll input")
		}
	case "key":
		result.TargetID = before.FocusedID
		focusedField := semanticNodeByID(d.tree, before.FocusedID)
		fieldTextKey := focusedField != nil && focusedField.Role == "textbox" && (isTextKey(event.Name) || event.Name == "Space")
		if fieldTextKey {
			// Text insertion/editing is deliberately deferred to the later
			// automation phase; these keys remain observable but unconsumed.
			result.Consumed = false
		} else if event.Name == "Tab" {
			if event.Kind == "down" {
				result.TargetID = semanticIDForHandle(d.tree, d.router.FocusNext(hasModifier(event.Modifiers, "shift")))
				result.Consumed = result.TargetID != ""
			} else {
				result.Consumed = true
			}
		} else if !isTextKey(event.Name) {
			if event.Kind == "down" {
				activation, activated = d.router.KeyDown(event.Name)
			} else {
				activation, activated = d.router.KeyUp(event.Name)
			}
			result.Consumed = activated
		}
	}

	if activated {
		result.Activation = &ActivationEffect{Scope: activation.Scope, ActionCount: len(activation.Actions), Actions: actionEffects(activation.Actions), OpenSelect: activation.OpenSelect != "", CloseSelect: activation.CloseSelect, ActiveOption: semanticIDForHandle(d.tree, activation.ActiveOption)}
		if d.runtime == nil {
			return Result{}, fmt.Errorf("automation runtime is unavailable")
		}
		if err := d.runtime.Activate(activation); err != nil {
			return Result{}, err
		}
		changed = true
		// Runtime activation owns select close/open and screen navigation
		// transient cleanup. Ordinary control activations (notably radio/tab
		// roving) must retain the router's newly focused item and publish it
		// below instead of overwriting it with the runtime's pre-event focus.
		if activation.OpenSelect != "" || activation.CloseSelect || hasNavigationAction(activation.Actions) {
			if reader, ok := d.runtime.(interface{ CurrentTransient() interaction.Transient }); ok {
				d.router.SyncTransient(reader.CurrentTransient())
			}
		}
	}
	for {
		value, ok := d.router.TakeValueChange()
		if !ok {
			break
		}
		if d.runtime == nil {
			return Result{}, fmt.Errorf("automation runtime is unavailable")
		}
		normalized, err := d.runtime.SetControlValue(value.ID, value.Value)
		if err != nil {
			return Result{}, err
		}
		result.ValueChange = &ValueEffect{ID: value.ID, Value: normalized}
		changed = true
		result.Consumed = true
	}
	for {
		scroll, ok := d.router.TakeScrollChange()
		if !ok {
			break
		}
		if d.runtime == nil {
			return Result{}, fmt.Errorf("automation runtime is unavailable")
		}
		if err := d.runtime.ScrollSemanticID(scroll.ID, scroll.Mode, scroll.X, scroll.Y); err != nil {
			return Result{}, err
		}
		copy := scroll
		result.ScrollChange = &ScrollEffect{ID: copy.ID, Mode: copy.Mode, X: copy.X, Y: copy.Y}
		changed = true
		result.Consumed = true
	}
	afterTransient := d.router.Transient()
	if afterTransient != beforeTransient {
		if event.Type == "key" {
			result.Consumed = true
		}
		if d.runtime == nil {
			return Result{}, fmt.Errorf("automation runtime is unavailable")
		}
		d.runtime.SetTransient(afterTransient)
		changed = true
	}
	if changed && d.runtime != nil {
		tree, err := d.runtime.CurrentRuntimeTree()
		if err != nil {
			return Result{}, err
		}
		d.tree = tree
		d.router.Update(tree)
		if reader, ok := d.runtime.(interface{ CurrentTransient() interaction.Transient }); ok {
			d.router.SyncTransient(reader.CurrentTransient())
		}
	}
	if d.runtime != nil {
		d.runtime.PublishRouterSnapshot(d.router.Snapshot())
	}
	after := d.router.Snapshot()
	result.FocusAfter = after.FocusedID
	result.CaptureAfter = cloneCapture(after.PointerCapture)
	if event.Type != "scroll" {
		candidateIDs := traceCandidateIDs(d.tree, point)
		if event.Type == "key" && result.FocusAfter != "" {
			candidateIDs = []string{result.FocusAfter}
		}
		d.recordTrace(TraceEntry{Stage: "candidates", EventIndex: index, Type: event.Type, TargetID: result.TargetID, IDs: candidateIDs})
		owner := result.TargetID
		if event.Type == "key" {
			owner = result.FocusAfter
		}
		d.recordTrace(TraceEntry{Stage: "owner_selection", EventIndex: index, Type: event.Type, TargetID: owner, SemanticID: owner})
		captureOutcome := "none"
		if event.Type == "key" && before.KeyboardPress != nil {
			captureOutcome = "keyboard_retained"
		} else if before.PointerCapture != nil {
			captureOutcome = "retained"
		}
		if event.Type == "key" && after.KeyboardPress != nil {
			if captureOutcome == "keyboard_retained" {
				captureOutcome = "keyboard_retained"
			} else {
				captureOutcome = "keyboard_acquired"
			}
		} else if result.CaptureAfter != nil {
			if captureOutcome == "retained" {
				captureOutcome = "retained"
			} else {
				captureOutcome = "acquired"
			}
		} else if before.PointerCapture != nil {
			captureOutcome = "released"
		}
		d.recordTrace(TraceEntry{Stage: "capture_decision", EventIndex: index, Type: event.Type, TargetID: result.TargetID, Outcome: captureOutcome})
	}
	if d.snapshot != nil {
		revisions := d.snapshot()
		result.RuntimeRevision = revisions.RuntimeRevision
		result.FrameRevision = revisions.FrameRevision
		result.GeometryRevision = revisions.GeometryRevision
		result.PublishedRuntimeRevision = revisions.PublishedRuntimeRevision
		result.PublishedGeometryRevision = revisions.PublishedGeometryRevision
		result.AutomationInputRevision = revisions.AutomationInputRevision
	}
	afterRevisions := d.currentRevisions()
	afterTrace := d.currentTraceRevision()
	mutationOutcome := "none"
	if changed {
		mutationOutcome = "queued"
	}
	d.recordTrace(TraceEntry{Stage: "mutation", EventIndex: index, Type: event.Type, TargetID: result.TargetID, Outcome: mutationOutcome, RuntimeBefore: beforeRevisions.RuntimeRevision, RuntimeAfter: afterRevisions.RuntimeRevision, GeometryBefore: beforeRevisions.GeometryRevision, GeometryAfter: afterRevisions.GeometryRevision, FrameBefore: beforeRevisions.FrameRevision, FrameAfter: afterRevisions.FrameRevision, TraceBefore: beforeTrace, TraceAfter: afterTrace})
	invalidationOutcome := "none"
	if changed {
		invalidationOutcome = "runtime"
	}
	d.recordTrace(TraceEntry{Stage: "invalidation", EventIndex: index, Type: event.Type, Outcome: invalidationOutcome, RuntimeBefore: beforeRevisions.RuntimeRevision, RuntimeAfter: afterRevisions.RuntimeRevision, GeometryBefore: beforeRevisions.GeometryRevision, GeometryAfter: afterRevisions.GeometryRevision, FrameBefore: beforeRevisions.FrameRevision, FrameAfter: afterRevisions.FrameRevision, TraceBefore: beforeTrace, TraceAfter: afterTrace})
	if afterRevisions.FrameRevision > beforeRevisions.FrameRevision {
		d.recordTrace(TraceEntry{Stage: "publication", EventIndex: index, Type: event.Type, Outcome: fmt.Sprintf("frame=%d", afterRevisions.FrameRevision), RuntimeBefore: beforeRevisions.RuntimeRevision, RuntimeAfter: afterRevisions.RuntimeRevision, GeometryBefore: beforeRevisions.GeometryRevision, GeometryAfter: afterRevisions.GeometryRevision, FrameBefore: beforeRevisions.FrameRevision, FrameAfter: afterRevisions.FrameRevision, TraceBefore: beforeTrace, TraceAfter: afterTrace})
	} else {
		reason := "no_frame"
		if result.ScrollRouting != nil && result.ScrollRouting.NoFrameReason != "" {
			reason = result.ScrollRouting.NoFrameReason
		} else if !changed && (before.FocusedID != after.FocusedID || captureChanged(before.PointerCapture, after.PointerCapture)) {
			reason = "router_only"
		} else if !result.Consumed {
			reason = "unconsumed"
		} else if changed {
			reason = "no_frame_revision"
		}
		d.recordTrace(TraceEntry{Stage: "publication", EventIndex: index, Type: event.Type, Outcome: reason, RuntimeBefore: beforeRevisions.RuntimeRevision, RuntimeAfter: afterRevisions.RuntimeRevision, GeometryBefore: beforeRevisions.GeometryRevision, GeometryAfter: afterRevisions.GeometryRevision, FrameBefore: beforeRevisions.FrameRevision, FrameAfter: afterRevisions.FrameRevision, TraceBefore: beforeTrace, TraceAfter: afterTrace})
	}
	return result, nil
}

func captureChanged(before, after *interaction.PointerCaptureSnapshot) bool {
	if before == nil || after == nil {
		return before != after
	}
	return before.PointerID != after.PointerID || before.OwnerID != after.OwnerID || before.Source != after.Source || before.Buttons != after.Buttons || before.Point != after.Point
}

func traceCandidateIDs(root *semantic.Node, point image.Point) []string {
	if root == nil {
		return nil
	}
	nodes := make([]*semantic.Node, 0)
	for _, node := range semantic.Flatten(root) {
		if node == nil || node.ID == "" || !node.Visible || !node.InViewport || node.Bounds == nil || node.Clip == nil {
			continue
		}
		bounds := node.Bounds.ImageRectangle().Intersect(node.Clip.ImageRectangle())
		if bounds.Empty() || !point.In(bounds) {
			continue
		}
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].PaintOrder != nodes[j].PaintOrder {
			return nodes[i].PaintOrder > nodes[j].PaintOrder
		}
		return len(nodes[i].Breadcrumb) > len(nodes[j].Breadcrumb)
	})
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func (d *Driver) currentRevisions() RevisionSnapshot {
	if d == nil || d.snapshot == nil {
		return RevisionSnapshot{}
	}
	return d.snapshot()
}

func (d *Driver) currentTraceRevision() uint64 {
	if d == nil || d.runtime == nil {
		return 0
	}
	if trace, ok := d.runtime.(TraceSnapshotRuntime); ok {
		return trace.EventTrace().Revision
	}
	return 0
}

func (d *Driver) recordTrace(entry TraceEntry) {
	if d == nil || d.runtime == nil {
		return
	}
	if recorder, ok := d.runtime.(TraceRuntime); ok {
		recorder.RecordEventTrace(entry)
	}
}

func semanticNodeByID(root *semantic.Node, id string) *semantic.Node {
	if root == nil || id == "" {
		return nil
	}
	for _, node := range semantic.Flatten(root) {
		if node != nil && node.ID == id {
			return node
		}
	}
	return nil
}

func actionEffects(actions []document.Action) []ActionEffect {
	if len(actions) == 0 {
		return nil
	}
	result := make([]ActionEffect, len(actions))
	for index, action := range actions {
		result[index] = ActionEffect{Action: action.Action, State: action.State, To: action.To, Value: action.Value, By: action.By}
	}
	return result
}

func validateBatch(events []Event) error {
	return validateBatchWithPointers(events, nil)
}

func validateScrollTimeline(events []Event, initialPhase, initialMomentum string) (string, string, error) {
	phase, momentum := initialPhase, initialMomentum
	for index, event := range events {
		if event.Type != "scroll" {
			continue
		}
		switch event.Phase {
		case "begin":
			if phase == "begin" || phase == "update" {
				return phase, momentum, fmt.Errorf("event %d scroll begin repeats an active gesture", index)
			}
			phase = "begin"
		case "update":
			if phase == "" {
				phase = "update"
			}
		case "end", "cancel":
			phase = ""
		}
		canceled := event.Phase == "cancel"
		switch event.Momentum {
		case "begin":
			if momentum == "begin" || momentum == "update" {
				return phase, momentum, fmt.Errorf("event %d scroll momentum begin repeats an active sequence", index)
			}
			momentum = "begin"
		case "update":
			if canceled {
				momentum = ""
				continue
			}
			if momentum == "" {
				return phase, momentum, fmt.Errorf("event %d scroll momentum update requires begin", index)
			}
			momentum = "update"
		case "end":
			if momentum == "" && !canceled {
				return phase, momentum, fmt.Errorf("event %d scroll momentum end requires begin", index)
			}
			momentum = ""
		case "none":
			if event.Phase == "cancel" {
				momentum = ""
			}
		}
	}
	return phase, momentum, nil
}

func validateBatchWithPointers(events []Event, initial map[int]pointerState) error {
	if len(events) == 0 {
		return fmt.Errorf("events must not be empty")
	}
	pointers := make(map[int]pointerState, len(initial)+len(events))
	for pointerID, state := range initial {
		pointers[pointerID] = state
	}
	lastTime := float64(0)
	for index, event := range events {
		if event.Type != "pointer" && event.Type != "key" && event.Type != "scroll" {
			return fmt.Errorf("event %d has unsupported type %q", index, event.Type)
		}
		if !finiteNonNegative(event.TimeMS) || (index > 0 && event.TimeMS < lastTime) {
			return fmt.Errorf("event %d time_ms must be finite, non-negative, and monotonic", index)
		}
		lastTime = event.TimeMS
		if err := validateModifiers(event.Modifiers); err != nil {
			return fmt.Errorf("event %d: %w", index, err)
		}
		if event.Type == "pointer" {
			if event.PointerID <= 0 {
				return fmt.Errorf("event %d pointer_id must be positive", index)
			}
			if event.Source != "mouse" && event.Source != "touch" {
				return fmt.Errorf("event %d source must be mouse or touch", index)
			}
			if !finite(event.X) || !finite(event.Y) {
				return fmt.Errorf("event %d pointer coordinates must be finite", index)
			}
			button := event.Button
			if button == "" {
				button = "none"
			}
			if button != "primary" && button != "secondary" && button != "middle" && button != "none" {
				return fmt.Errorf("event %d button %q is unsupported", index, event.Button)
			}
			switch event.Kind {
			case "enter", "move", "leave":
				if state, ok := pointers[event.PointerID]; ok && state.source != event.Source {
					return fmt.Errorf("event %d source does not match pointer %d press", index, event.PointerID)
				}
			case "press":
				if _, ok := pointers[event.PointerID]; ok {
					return fmt.Errorf("event %d presses pointer %d twice", index, event.PointerID)
				}
				pointers[event.PointerID] = pointerState{source: event.Source, button: normalizedButton(event.Button)}
			case "release", "cancel":
				state, ok := pointers[event.PointerID]
				if !ok {
					return fmt.Errorf("event %d %s uses unknown pointer %d", index, event.Kind, event.PointerID)
				}
				if event.Kind == "release" && (state.source != event.Source || state.button != normalizedButton(event.Button)) {
					return fmt.Errorf("event %d release does not match pointer %d press", index, event.PointerID)
				}
				delete(pointers, event.PointerID)
			default:
				return fmt.Errorf("event %d has unsupported pointer kind %q", index, event.Kind)
			}
		} else if event.Type == "scroll" {
			if event.PointerID != 0 {
				return fmt.Errorf("event %d scroll pointer_id must be omitted", index)
			}
			if !finite(event.X) || !finite(event.Y) {
				return fmt.Errorf("event %d scroll coordinates must be finite", index)
			}
			if event.Source != "wheel" && event.Source != "trackpad" {
				return fmt.Errorf("event %d scroll source must be wheel or trackpad", index)
			}
			if event.Units != "logical" && event.Units != "physical_pixels" {
				return fmt.Errorf("event %d scroll units must be logical or physical_pixels", index)
			}
			if event.Phase != "begin" && event.Phase != "update" && event.Phase != "end" && event.Phase != "cancel" {
				return fmt.Errorf("event %d scroll phase must be begin, update, end, or cancel", index)
			}
			if event.Momentum != "" && event.Momentum != "none" && event.Momentum != "begin" && event.Momentum != "update" && event.Momentum != "end" {
				return fmt.Errorf("event %d scroll momentum must be none, begin, update, or end", index)
			}
			if !finite(event.DeltaX) || !finite(event.DeltaY) {
				return fmt.Errorf("event %d scroll deltas must be finite", index)
			}
		} else {
			if event.Kind != "down" && event.Kind != "up" {
				return fmt.Errorf("event %d has unsupported key kind %q", index, event.Kind)
			}
			if !validKeyName(event.Name) {
				return fmt.Errorf("event %d has unsupported key %q", index, event.Name)
			}
		}
	}
	return nil
}

func validateModifiers(modifiers []string) error {
	seen := make(map[string]bool, len(modifiers))
	for _, modifier := range modifiers {
		if modifier != "shift" && modifier != "control" && modifier != "command" && modifier != "option" {
			return fmt.Errorf("unsupported modifier %q", modifier)
		}
		if seen[modifier] {
			return fmt.Errorf("duplicate modifier %q", modifier)
		}
		seen[modifier] = true
	}
	return nil
}

func validKeyName(name string) bool {
	switch name {
	case "Tab", "Enter", "Space", "Escape", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown", "Backspace", "Delete":
		return true
	}
	return len(name) == 1 && strings.Contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", name)
}

func isTextKey(name string) bool {
	return name == "Backspace" || name == "Delete" || (len(name) == 1 && strings.Contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", name))
}

func hasModifier(modifiers []string, wanted string) bool {
	for _, modifier := range modifiers {
		if modifier == wanted {
			return true
		}
	}
	return false
}

func hasNavigationAction(actions []document.Action) bool {
	for _, action := range actions {
		switch action.Action {
		case "navigate", "replace", "back", "forward":
			return true
		}
	}
	return false
}

func pointerButtons(button string) int {
	switch button {
	case "primary":
		return 1
	case "secondary":
		return 2
	case "middle":
		return 4
	default:
		return 0
	}
}

func normalizedButton(button string) string {
	if button == "" {
		return "none"
	}
	return button
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func finiteNonNegative(value float64) bool { return finite(value) && value >= 0 }

func roundLogical(value float64) int {
	if value <= float64(-int(^uint(0)>>1)) {
		return -int(^uint(0)>>1) - 1
	}
	if value >= float64(int(^uint(0)>>1)) {
		return int(^uint(0) >> 1)
	}
	return int(math.Round(value))
}

func cloneCapture(capture *interaction.PointerCaptureSnapshot) *interaction.PointerCaptureSnapshot {
	if capture == nil {
		return nil
	}
	copy := *capture
	return &copy
}

func semanticIDForHandle(root *semantic.Node, handle string) string {
	if handle == "" || root == nil {
		return ""
	}
	for _, node := range semantic.Flatten(root) {
		if node != nil && node.Handle == handle {
			return node.ID
		}
	}
	return handle
}
