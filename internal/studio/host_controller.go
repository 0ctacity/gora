package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"gora/internal/automation"
	"gora/internal/project"
	"gora/internal/semantic"
	"gora/internal/session"
)

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func NewHostAutomationDriver(runtime *Runtime) *automation.Driver {
	return automation.NewDriverWithSnapshot(runtime, func() automation.RevisionSnapshot {
		snapshot := runtime.AutomationSnapshot()
		return automation.RevisionSnapshot{RuntimeRevision: snapshot.RuntimeRevision, FrameRevision: snapshot.FrameRevision, GeometryRevision: snapshot.GeometryRevision, PublishedRuntimeRevision: snapshot.PublishedRuntimeRevision, PublishedGeometryRevision: snapshot.PublishedGeometryRevision, AutomationInputRevision: snapshot.AutomationInputRevision}
	})
}

func decodePayload(data json.RawMessage, target any) error {
	if len(data) == 0 {
		return errors.New("host request payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("host request payload must contain one JSON value")
	} else if err != io.EOF {
		return err
	}
	return nil
}

var ErrHostCommandQueueFull = errors.New("host command queue is full")
var ErrHostControllerClosed = errors.New("host controller is closed")

// HostCommand is an event-loop-owned mutation. Apply is always invoked by
// the Gio host loop, never by a session socket goroutine.
type HostCommand struct {
	RequestID string
	Apply     func() error
	Result    func() (json.RawMessage, error)
}

type commandCompletion struct {
	data json.RawMessage
	err  error
}

type queuedHostCommand struct {
	command    HostCommand
	result     chan commandCompletion
	completion commandCompletion
	completed  bool
}

// HostPublication is the immutable publication barrier observed by waiters.
type HostPublication struct {
	RuntimeRevision  uint64
	GeometryRevision uint64
	FrameRevision    uint64
}

// HostController serializes socket requests through the owning Gio event
// loop. Queue and publication storage are bounded.
type HostController struct {
	mu       sync.Mutex
	capacity int
	queue    []queuedHostCommand
	pending  []queuedHostCommand
	latest   HostPublication
	wake     chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func NewHostController(capacity int) *HostController {
	if capacity <= 0 {
		capacity = 64
	}
	return &HostController{capacity: capacity, wake: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (controller *HostController) Wake() <-chan struct{} { return controller.wake }
func (controller *HostController) Done() <-chan struct{} { return controller.closed }

func (controller *HostController) TrySubmit(command HostCommand) error {
	if command.Apply == nil {
		command.Apply = func() error { return nil }
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	select {
	case <-controller.closed:
		return ErrHostControllerClosed
	default:
	}
	if len(controller.queue)+len(controller.pending) >= controller.capacity {
		return ErrHostCommandQueueFull
	}
	controller.queue = append(controller.queue, queuedHostCommand{command: command, result: make(chan commandCompletion, 1)})
	select {
	case controller.wake <- struct{}{}:
	default:
	}
	return nil
}

func (controller *HostController) Submit(ctx context.Context, command HostCommand) error {
	_, err := controller.SubmitResult(ctx, command)
	return err
}

func (controller *HostController) SubmitResult(ctx context.Context, command HostCommand) (json.RawMessage, error) {
	if command.Apply == nil {
		command.Apply = func() error { return nil }
	}
	controller.mu.Lock()
	select {
	case <-controller.closed:
		controller.mu.Unlock()
		return nil, ErrHostControllerClosed
	default:
	}
	if len(controller.queue)+len(controller.pending) >= controller.capacity {
		controller.mu.Unlock()
		return nil, ErrHostCommandQueueFull
	}
	item := queuedHostCommand{command: command, result: make(chan commandCompletion, 1)}
	controller.queue = append(controller.queue, item)
	select {
	case controller.wake <- struct{}{}:
	default:
	}
	controller.mu.Unlock()
	select {
	case completion := <-item.result:
		return completion.data, completion.err
	case <-controller.closed:
		return nil, ErrHostControllerClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Drain transfers queued commands to the loop owner. The returned commands
// must be applied by that owner before calling Publish.
func (controller *HostController) Drain() []HostCommand {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	commands := make([]HostCommand, len(controller.queue))
	for index, item := range controller.queue {
		commands[index] = item.command
		controller.pending = append(controller.pending, item)
	}
	controller.queue = controller.queue[:0]
	return commands
}

// Complete records command errors after the loop owner has applied them. A
// subsequent Publish is still required before successful command responses.
func (controller *HostController) Complete(requestID string, err error, data ...json.RawMessage) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	for index := range controller.pending {
		if controller.pending[index].command.RequestID == requestID {
			var result json.RawMessage
			if len(data) != 0 {
				result = data[0]
			}
			controller.pending[index].completion = commandCompletion{data: result, err: err}
			controller.pending[index].completed = true
			if err != nil {
				controller.pending[index].result <- controller.pending[index].completion
				controller.pending = append(controller.pending[:index], controller.pending[index+1:]...)
			}
			return
		}
	}
}

// Publish releases successful commands after the host has produced its
// canonical tree/frame publication.
func (controller *HostController) Publish(publication HostPublication) {
	controller.mu.Lock()
	controller.latest = publication
	pending := controller.pending
	controller.pending = nil
	controller.mu.Unlock()
	for _, item := range pending {
		completion := item.completion
		if !item.completed {
			completion = commandCompletion{err: ErrHostControllerClosed}
		}
		select {
		case item.result <- completion:
		default:
		}
	}
}

func (controller *HostController) Publication() HostPublication {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.latest
}

func (controller *HostController) Close() {
	controller.once.Do(func() {
		close(controller.closed)
		controller.mu.Lock()
		pending := append(controller.queue, controller.pending...)
		controller.queue = nil
		controller.pending = nil
		controller.mu.Unlock()
		for _, item := range pending {
			select {
			case item.result <- commandCompletion{err: ErrHostControllerClosed}:
			default:
			}
		}
	})
}

// SessionHandler returns the protocol bridge for a live host. Legacy actions
// are delegated to Runtime.SessionHandler; versioned command actions are
// queued and completed only after HostController.Publish.
func (runtime *Runtime) SessionHandlerWithController(hostMode string, identity session.HostIdentity, controller *HostController, focus func(), drivers ...*automation.Driver) session.Handler {
	legacy := runtime.SessionHandler(hostMode, focus)
	var driver *automation.Driver
	if len(drivers) != 0 {
		driver = drivers[0]
	}
	return func(ctx context.Context, request session.Request) session.Response {
		if request.Version == 0 {
			return legacy(ctx, request)
		}
		if request.Version != session.ProtocolVersion {
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "unsupported session protocol version"}
		}
		if request.RequestID == "" {
			return session.Response{Version: session.ProtocolVersion, Error: "request_id is required for versioned session requests"}
		}
		switch request.Action {
		case session.ActionHandshake:
			var payload session.HandshakePayload
			if err := decodePayload(request.Payload, &payload); err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			if payload.Protocol != session.ProtocolVersion || payload.Mode != session.HostMode(hostMode) || !sameCanonicalSessionPath(payload.Root, identity.Root) || !sameCanonicalSessionPath(payload.Document, identity.Document) {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "host handshake identity mismatch"}
			}
			if !identity.Automation {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "host automation is not enabled"}
			}
			if identity.InstanceID == "" || identity.PID <= 0 {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "host identity is invalid"}
			}
			if err := session.ValidateCapabilities(identity.Capabilities); err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			result, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: result}
		case session.ActionSnapshot:
			data, err := json.Marshal(runtime.AutomationSnapshot())
			if err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
		case session.ActionWait:
			var payload struct {
				AfterFrameRevision   uint64 `json:"after_frame_revision"`
				AfterFrameSet        bool   `json:"after_frame_set"`
				AfterRuntimeRevision uint64 `json:"after_runtime_revision"`
				AfterRuntimeSet      bool   `json:"after_runtime_set"`
				Condition            string `json:"condition"`
				StableFrames         int    `json:"stable_frames"`
				TimeoutMS            int    `json:"timeout_ms"`
			}
			if err := decodePayload(request.Payload, &payload); err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			wait := WaitForViewRequest{AfterFrameRevision: payload.AfterFrameRevision, AfterFrameSet: payload.AfterFrameSet, AfterRuntimeRevision: payload.AfterRuntimeRevision, AfterRuntimeSet: payload.AfterRuntimeSet, Condition: payload.Condition, StableFrames: payload.StableFrames, Timeout: 5 * time.Second, AllowAlreadySatisfied: true}
			if payload.TimeoutMS > 0 {
				wait.Timeout = time.Duration(payload.TimeoutMS) * time.Millisecond
			}
			snapshot, err := runtime.WaitForView(ctx, wait)
			if err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error(), Data: mustJSON(snapshot)}
			}
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: mustJSON(snapshot)}
		case session.ActionCommand:
			if controller == nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "host command controller unavailable"}
			}
			var command hostCommandPayload
			if err := decodePayload(request.Payload, &command); err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			var dispatchResults []automation.Result
			apply := runtime.commandApply(command)
			if command.Kind == "dispatch" {
				if driver == nil {
					return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "unsupported capability: host input driver unavailable"}
				}
				apply = func() error {
					var dispatchErr error
					dispatchResults, dispatchErr = driver.Dispatch(command.Events)
					return dispatchErr
				}
			}
			data, err := controller.SubmitResult(ctx, HostCommand{RequestID: request.RequestID, Apply: apply, Result: func() (json.RawMessage, error) {
				if command.Kind == "dispatch" {
					return json.Marshal(dispatchResults)
				}
				return runtime.commandResult(command)
			}})
			if err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
		default:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "unknown host session action"}
		}
	}
}

func sameCanonicalSessionPath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	if leftErr != nil {
		leftResolved = left
	}
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if rightErr != nil {
		rightResolved = right
	}
	leftResolved, _ = filepath.Abs(leftResolved)
	rightResolved, _ = filepath.Abs(rightResolved)
	return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
}

type hostCommandPayload struct {
	Kind         string                         `json:"kind"`
	Width        int                            `json:"width"`
	Height       int                            `json:"height"`
	Name         string                         `json:"name"`
	SemanticID   string                         `json:"semantic_id"`
	Mode         string                         `json:"mode"`
	X            int                            `json:"x"`
	Y            int                            `json:"y"`
	ScopeID      string                         `json:"scope_id"`
	Values       map[string]any                 `json:"values"`
	Value        any                            `json:"value"`
	Draft        string                         `json:"draft"`
	Text         string                         `json:"text"`
	DeltaMS      int64                          `json:"delta_ms"`
	RunUntilIdle bool                           `json:"run_until_idle"`
	Enabled      bool                           `json:"enabled"`
	Capacity     int                            `json:"capacity"`
	Scale        int                            `json:"scale"`
	Events       []automation.Event             `json:"events"`
	Assertions   []automation.Assertion         `json:"assertions"`
	Overlay      map[string]project.OverlayFile `json:"overlay"`
	Faults       map[string]int                 `json:"faults"`
}

func (runtime *Runtime) commandApply(command hostCommandPayload) func() error {
	return func() error {
		switch command.Kind {
		case "invalidate", "snapshot", "noop", "":
			return nil
		case "set_viewport":
			if command.Width <= 0 || command.Height <= 0 {
				return errors.New("viewport dimensions must be positive")
			}
			runtime.SetViewport(command.Width, command.Height)
			return nil
		case "select":
			if !runtime.SelectScreen(command.Name) {
				return errors.New("unknown selection " + command.Name)
			}
			return nil
		case "activate":
			return runtime.ActivateSemanticID(command.SemanticID)
		case "scroll":
			return runtime.ScrollSemanticID(command.SemanticID, command.Mode, command.X, command.Y)
		case "set_state":
			return runtime.SetStateValues(command.ScopeID, command.Values)
		case "reset_state":
			runtime.ResetState()
			return nil
		case "set_control_value":
			_, err := runtime.SetControlValue(command.SemanticID, command.Value)
			return err
		case "set_field_draft":
			return runtime.SetFieldDraft(command.SemanticID, command.Draft)
		case "submit_form":
			return runtime.SubmitForm(command.SemanticID)
		case "reset_form":
			return runtime.ResetForm(command.SemanticID)
		case "set_clock":
			_, err := runtime.SetViewClock(command.Mode)
			return err
		case "advance_clock":
			_, err := runtime.AdvanceViewClock(command.DeltaMS, command.RunUntilIdle)
			return err
		case "run_until_idle":
			_, err := runtime.RunUntilIdle()
			return err
		case "set_clipboard":
			runtime.SetAutomationClipboard(command.Text)
			return nil
		case "configure_faults":
			runtime.ConfigureAutomationFaults(command.Faults)
			return nil
		case "clear_faults":
			runtime.ClearAutomationFaults()
			return nil
		case "get_clipboard", "get_trace":
			return nil
		case "configure_trace":
			return runtime.ConfigureEventTrace(command.Enabled, command.Capacity)
		case "clear_trace":
			runtime.ClearEventTrace()
			return nil
		case "capture":
			return nil
		case "assert":
			return nil
		case "reload_overlay":
			runtime.ReloadOverlay(command.Overlay)
			return nil
		default:
			return errors.New("unsupported host command " + command.Kind)
		}
	}
}

func (runtime *Runtime) commandResult(command hostCommandPayload) (json.RawMessage, error) {
	var value any = runtime.AutomationSnapshot()
	switch command.Kind {
	case "set_control_value":
		normalized := command.Value
		if tree, err := runtime.RuntimeTree(); err == nil {
			for _, node := range semantic.Flatten(tree) {
				if node.ID == command.SemanticID {
					normalized = node.CommittedValue
					if normalized == nil {
						normalized = node.Value
					}
					break
				}
			}
		}
		value = map[string]any{"value": normalized, "snapshot": runtime.AutomationSnapshot()}
	case "set_field_draft":
		field := map[string]any{"draft": command.Draft, "value": nil, "valid": false}
		if tree, err := runtime.RuntimeTree(); err == nil {
			for _, node := range semantic.Flatten(tree) {
				if node.ID == command.SemanticID {
					field["draft"], field["value"] = node.Value, node.CommittedValue
					field["valid"] = node.Valid != nil && *node.Valid
					break
				}
			}
		}
		value = field
	case "set_clock", "advance_clock", "run_until_idle":
		value = map[string]any{"clock": runtime.Snapshot().Clock, "snapshot": runtime.AutomationSnapshot()}
	case "get_clipboard":
		value = map[string]any{"text": runtime.AutomationClipboard()}
	case "get_trace":
		value = runtime.EventTrace()
	case "configure_trace", "clear_trace":
		value = runtime.EventTrace()
	case "capture":
		if runtime.ConsumeAutomationFault("capture_failure") {
			return nil, errors.New("injected capture failure")
		}
		png, warning, identity, err := runtime.CapturePNGReadOnly(command.Scale)
		if err != nil {
			return nil, err
		}
		value = map[string]any{"png_base64": base64.StdEncoding.EncodeToString(png), "warning": warning, "identity": identity}
	case "assert":
		var tree *semantic.Node
		if current, err := runtime.CurrentRuntimeTree(); err == nil {
			tree = current
		}
		snapshot := runtime.AutomationSnapshot()
		view := automation.ViewSnapshot{Valid: snapshot.Valid, LastGoodAvailable: snapshot.LastGoodAvailable, Agreement: snapshot.Agreement, RuntimePublished: snapshot.RuntimePublished, GeometryPublished: snapshot.GeometryPublished, Idle: snapshot.Idle, IdleReasons: snapshot.IdleReasons, Selection: snapshot.Selection, Selections: snapshot.Selections, Viewport: snapshot.Viewport, CanBack: snapshot.CanBack, CanForward: snapshot.CanForward, RuntimeRevision: snapshot.RuntimeRevision, FrameRevision: snapshot.FrameRevision, GeometryRevision: snapshot.GeometryRevision, PublishedRuntimeRevision: snapshot.PublishedRuntimeRevision, PublishedGeometryRevision: snapshot.PublishedGeometryRevision, ReloadRevision: snapshot.ReloadRevision, AutomationInputRevision: snapshot.AutomationInputRevision, Diagnostics: snapshot.Diagnostics, Transient: snapshot.Transient, Router: snapshot.Router, Editing: snapshot.Editing, StateValues: snapshot.StateValues, Tree: tree}
		report, err := automation.EvaluateAssertions(automation.AssertionSnapshot{Tree: tree, View: view, Router: snapshot.Router, Editing: snapshot.Editing, StateValues: snapshot.StateValues, ScrollOffsets: snapshot.Scroll}, command.Assertions)
		if err != nil {
			return nil, err
		}
		value = report
	}
	return json.Marshal(value)
}
