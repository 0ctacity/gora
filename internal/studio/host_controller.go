package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/op"
	xdraw "golang.org/x/image/draw"

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
	barrier    hostBarrier
	deadline   time.Time
	timer      *time.Timer
}

// hostBarrier records the ConfigEvent revision observed before a window
// transition was requested. The command cannot complete until a later config
// event and a subsequent stable frame are both published.
type hostBarrier struct {
	required       bool
	configRevision uint64
}

// HostPublication is the immutable publication barrier observed by waiters.
type HostPublication struct {
	RuntimeRevision  uint64
	GeometryRevision uint64
	FrameRevision    uint64
	InputRevision    uint64
	TraceRevision    uint64
	Host             HostSnapshot
	ClientFrame      *HostClientFrame
}

// HostClientFrame is the latest operation list actually submitted to a Gio
// host window. It is retained as one bounded frame for attached client-area
// capture; the pointer is immutable after publication and is never exposed in
// JSON resources.
type HostClientFrame struct {
	Ops  *op.Ops
	Size image.Point
}

// HostController serializes socket requests through the owning Gio event
// loop. Queue and publication storage are bounded.
type HostController struct {
	mu                sync.Mutex
	capacity          int
	queue             []queuedHostCommand
	pending           []queuedHostCommand
	latest            HostPublication
	host              HostSnapshot
	client            *HostClientFrame
	published         bool
	hostRevision      uint64
	barriers          map[string]hostBarrier
	transitionTimeout time.Duration
	wake              chan struct{}
	closed            chan struct{}
	once              sync.Once
	handler           HostCommandHandler
}

// HostCommandHandler lets a real host add finite window/Studio operations to
// the same event-loop command queue used by runtime operations.
type HostCommandHandler func(HostCommandPayload) (func() error, func() (json.RawMessage, error), error)

func NewHostController(capacity int) *HostController {
	if capacity <= 0 {
		capacity = 64
	}
	return &HostController{capacity: capacity, wake: make(chan struct{}, 1), closed: make(chan struct{}), barriers: make(map[string]hostBarrier), transitionTimeout: 5 * time.Second}
}

func (controller *HostController) Wake() <-chan struct{} { return controller.wake }
func (controller *HostController) Done() <-chan struct{} { return controller.closed }

func (controller *HostController) SetCommandHandler(handler HostCommandHandler) {
	controller.mu.Lock()
	controller.handler = handler
	controller.mu.Unlock()
}

func (controller *HostController) CommandHandler() HostCommandHandler {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.handler
}

// RequireConfigBarrier arms the next command with the supplied request ID.
// It is called by the event-loop command handler immediately before enqueueing
// an operation that changes native window configuration.
func (controller *HostController) RequireConfigBarrier(requestID string) {
	if requestID == "" {
		return
	}
	controller.mu.Lock()
	controller.barriers[requestID] = hostBarrier{required: true, configRevision: controller.host.ConfigRevision}
	controller.mu.Unlock()
}

func (controller *HostController) queuedCommandLocked(command HostCommand) queuedHostCommand {
	item := queuedHostCommand{command: command, result: make(chan commandCompletion, 1)}
	if barrier, ok := controller.barriers[command.RequestID]; ok {
		delete(controller.barriers, command.RequestID)
		item.barrier = barrier
		item.deadline = time.Now().Add(controller.transitionTimeout)
		item.timer = time.AfterFunc(controller.transitionTimeout, func() {
			controller.timeoutBarrier(command.RequestID)
		})
	}
	return item
}

func (controller *HostController) timeoutBarrier(requestID string) {
	controller.mu.Lock()
	var expired *queuedHostCommand
	for index := range controller.queue {
		if controller.queue[index].command.RequestID != requestID {
			continue
		}
		item := controller.queue[index]
		controller.queue = append(controller.queue[:index], controller.queue[index+1:]...)
		expired = &item
		break
	}
	if expired == nil {
		for index := range controller.pending {
			if controller.pending[index].command.RequestID != requestID {
				continue
			}
			item := controller.pending[index]
			controller.pending = append(controller.pending[:index], controller.pending[index+1:]...)
			expired = &item
			break
		}
	}
	controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
	if controller.host.PendingCommands == 0 {
		controller.host.CommandState = "idle"
	}
	controller.mu.Unlock()
	if expired != nil {
		select {
		case expired.result <- commandCompletion{err: errors.New("window transition timed out waiting for ConfigEvent")}:
		default:
		}
	}
}

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
	controller.queue = append(controller.queue, controller.queuedCommandLocked(command))
	controller.host.CommandState = "pending"
	controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
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
	item := controller.queuedCommandLocked(command)
	controller.queue = append(controller.queue, item)
	controller.host.CommandState = "pending"
	controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
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
	if len(commands) != 0 {
		controller.host.CommandState = "running"
	}
	controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
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
				if controller.pending[index].timer != nil {
					controller.pending[index].timer.Stop()
				}
				controller.pending[index].result <- controller.pending[index].completion
				controller.pending = append(controller.pending[:index], controller.pending[index+1:]...)
				controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
				if controller.host.PendingCommands == 0 {
					controller.host.CommandState = "idle"
				}
			}
			return
		}
	}
}

// CompleteNow acknowledges an event-loop command immediately. It is reserved
// for close, where Gio may destroy the window before another frame can be
// presented. Ordinary commands still complete through Publish.
func (controller *HostController) CompleteNow(requestID string, data json.RawMessage, err error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	for index := range controller.pending {
		if controller.pending[index].command.RequestID != requestID {
			continue
		}
		item := controller.pending[index]
		if item.timer != nil {
			item.timer.Stop()
		}
		controller.pending = append(controller.pending[:index], controller.pending[index+1:]...)
		controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
		if controller.host.PendingCommands == 0 {
			controller.host.CommandState = "idle"
		}
		select {
		case item.result <- commandCompletion{data: data, err: err}:
		default:
		}
		return
	}
}

// Publish releases successful commands after the host has produced its
// canonical tree/frame publication.
func (controller *HostController) Publish(publication HostPublication) {
	controller.mu.Lock()
	if publication.Host.HostInstanceID == "" {
		publication.Host = cloneHostSnapshot(controller.host)
	}
	publication.Host.RuntimeRevision = publication.RuntimeRevision
	publication.Host.GeometryRevision = publication.GeometryRevision
	publication.Host.FrameRevision = publication.FrameRevision
	publication.Host.InputRevision = publication.InputRevision
	publication.Host.TraceRevision = publication.TraceRevision
	controller.hostRevision++
	publication.Host.HostRevision = controller.hostRevision
	controller.host = cloneHostSnapshot(publication.Host)
	controller.latest = publication
	controller.client = publication.ClientFrame
	controller.published = true
	ready := make([]queuedHostCommand, 0, len(controller.pending))
	remaining := make([]queuedHostCommand, 0, len(controller.pending))
	now := time.Now()
	for _, item := range controller.pending {
		if !item.completed {
			ready = append(ready, item)
			continue
		}
		if item.completion.err != nil || !item.barrier.required || controller.host.ConfigRevision > item.barrier.configRevision {
			ready = append(ready, item)
			continue
		}
		if !item.deadline.IsZero() && !now.Before(item.deadline) {
			item.completion.err = errors.New("window transition timed out waiting for ConfigEvent")
			ready = append(ready, item)
			continue
		}
		remaining = append(remaining, item)
	}
	controller.pending = remaining
	controller.host.PendingCommands = len(controller.queue) + len(controller.pending)
	if controller.host.PendingCommands == 0 {
		controller.host.CommandState = "idle"
	} else if controller.host.CommandState == "idle" {
		controller.host.CommandState = "pending"
	}
	pending := ready
	controller.mu.Unlock()
	for _, item := range pending {
		if item.timer != nil {
			item.timer.Stop()
		}
		completion := item.completion
		if item.completed && completion.err == nil && len(completion.data) == 0 && item.command.Result != nil {
			completion.data, completion.err = item.command.Result()
		}
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

func (controller *HostController) SetHostIdentity(instanceID, mode string, pid int, capabilities []string) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.host.SchemaVersion = 1
	controller.host.HostProtocolVersion = session.ProtocolVersion
	controller.host.HostInstanceID = instanceID
	controller.host.Mode = normalizeWindowMode(mode)
	controller.host.ConnectionState = "connected"
	controller.host.ProcessID = pid
	controller.host.Capabilities = sortedCapabilities(capabilities)
	controller.host.Visible = true
	controller.host.WindowMode = "windowed"
}

func (controller *HostController) UpdateWindowState(logical, physical image.Point, pxPerDp, pxPerSp float32, mode string, focused, visible, closing bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	changed := controller.host.LogicalClientWidth != logical.X || controller.host.LogicalClientHeight != logical.Y || controller.host.PhysicalClientWidth != physical.X || controller.host.PhysicalClientHeight != physical.Y || controller.host.PxPerDp != pxPerDp || controller.host.PxPerSp != pxPerSp || (mode != "" && controller.host.WindowMode != normalizeWindowMode(mode)) || controller.host.Focused != focused || controller.host.Visible != visible || controller.host.Closing != closing
	controller.host.LogicalClientWidth, controller.host.LogicalClientHeight = logical.X, logical.Y
	controller.host.PhysicalClientWidth, controller.host.PhysicalClientHeight = physical.X, physical.Y
	controller.host.PxPerDp, controller.host.PxPerSp = pxPerDp, pxPerSp
	if mode != "" {
		controller.host.WindowMode = normalizeWindowMode(mode)
	}
	controller.host.Focused, controller.host.Visible, controller.host.Closing = focused, visible, closing
	if changed {
		controller.host.ConfigRevision++
	}
}

func (controller *HostController) UpdateMetrics(logical, physical image.Point, pxPerDp, pxPerSp float32) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	changed := controller.host.LogicalClientWidth != logical.X || controller.host.LogicalClientHeight != logical.Y || controller.host.PhysicalClientWidth != physical.X || controller.host.PhysicalClientHeight != physical.Y || controller.host.PxPerDp != pxPerDp || controller.host.PxPerSp != pxPerSp
	controller.host.LogicalClientWidth, controller.host.LogicalClientHeight = logical.X, logical.Y
	controller.host.PhysicalClientWidth, controller.host.PhysicalClientHeight = physical.X, physical.Y
	controller.host.PxPerDp, controller.host.PxPerSp = pxPerDp, pxPerSp
	if changed {
		controller.host.ConfigRevision++
	}
}

func (controller *HostController) UpdateStudioState(state HostStudioSnapshot, revision uint64) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.host.Studio = &state
	controller.host.StudioRevision = revision
}

func (controller *HostController) HostSnapshot() HostSnapshot {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	snapshot := controller.publishedHostLocked()
	// Queue state is intentionally observable before the next frame, while
	// window metrics and Studio fields remain tied to the last published frame.
	snapshot.CommandState = controller.host.CommandState
	snapshot.PendingCommands = controller.host.PendingCommands
	publication := controller.latest
	snapshot.HostRevision = publication.Host.HostRevision
	if snapshot.HostRevision == 0 {
		snapshot.HostRevision = publication.FrameRevision
	}
	snapshot.RuntimeRevision = publication.RuntimeRevision
	snapshot.GeometryRevision = publication.GeometryRevision
	snapshot.FrameRevision = publication.FrameRevision
	snapshot.InputRevision = publication.InputRevision
	snapshot.TraceRevision = publication.TraceRevision
	return snapshot
}

func (controller *HostController) publishedHostLocked() HostSnapshot {
	if controller.published {
		return cloneHostSnapshot(controller.latest.Host)
	}
	snapshot := cloneHostSnapshot(controller.host)
	snapshot.LogicalClientWidth, snapshot.LogicalClientHeight = 0, 0
	snapshot.PhysicalClientWidth, snapshot.PhysicalClientHeight = 0, 0
	snapshot.PxPerDp, snapshot.PxPerSp = 0, 0
	snapshot.Focused, snapshot.Closing = false, false
	snapshot.HostRevision, snapshot.RuntimeRevision = 0, 0
	snapshot.GeometryRevision, snapshot.FrameRevision = 0, 0
	snapshot.InputRevision, snapshot.StudioRevision, snapshot.TraceRevision = 0, 0, 0
	snapshot.Studio = nil
	return snapshot
}

// CaptureClientPNG renders the latest client operation list through Gio's
// headless backend. It does not invoke layout, consume input, or mutate any
// runtime/Studio revision. A scale other than one resamples the retained
// physical client frame deterministically.
func (controller *HostController) CaptureClientPNG(scale int) ([]byte, string, automation.CaptureIdentity, error) {
	if scale <= 0 {
		return nil, "", automation.CaptureIdentity{}, errors.New("scale must be a positive integer")
	}
	controller.mu.Lock()
	frame := controller.client
	host := controller.publishedHostLocked()
	publication := controller.latest
	host.HostRevision = publication.Host.HostRevision
	host.RuntimeRevision = publication.RuntimeRevision
	host.GeometryRevision = publication.GeometryRevision
	host.FrameRevision = publication.FrameRevision
	host.InputRevision = publication.InputRevision
	host.TraceRevision = publication.TraceRevision
	controller.mu.Unlock()
	if frame == nil || frame.Ops == nil || frame.Size.X <= 0 || frame.Size.Y <= 0 {
		return nil, "", automation.CaptureIdentity{}, errors.New("no published host client frame is available")
	}
	window, err := headless.NewWindow(frame.Size.X, frame.Size.Y)
	if err != nil {
		return nil, "", automation.CaptureIdentity{}, err
	}
	defer window.Release()
	if err := window.Frame(frame.Ops); err != nil {
		return nil, "", automation.CaptureIdentity{}, err
	}
	raw := image.NewRGBA(image.Rect(0, 0, frame.Size.X, frame.Size.Y))
	if err := window.Screenshot(raw); err != nil {
		return nil, "", automation.CaptureIdentity{}, err
	}
	if scale != 1 {
		scaled := image.NewRGBA(image.Rect(0, 0, raw.Bounds().Dx()*scale, raw.Bounds().Dy()*scale))
		xdraw.NearestNeighbor.Scale(scaled, scaled.Bounds(), raw, raw.Bounds(), xdraw.Src, nil)
		raw = scaled
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, raw); err != nil {
		return nil, "", automation.CaptureIdentity{}, err
	}
	identity := automation.CaptureIdentity{
		ViewportWidth: host.LogicalClientWidth, ViewportHeight: host.LogicalClientHeight,
		RuntimeRevision: host.RuntimeRevision, FrameRevision: host.FrameRevision,
		GeometryRevision: host.GeometryRevision, PublishedRuntimeRevision: host.RuntimeRevision,
		PublishedGeometryRevision: host.GeometryRevision, Width: raw.Bounds().Dx(), Height: raw.Bounds().Dy(),
		Valid: host.ConnectionState == "connected",
	}
	return buffer.Bytes(), "", identity, nil
}

func (controller *HostController) Close() {
	controller.once.Do(func() {
		close(controller.closed)
		controller.mu.Lock()
		pending := append(controller.queue, controller.pending...)
		controller.queue = nil
		controller.pending = nil
		controller.barriers = make(map[string]hostBarrier)
		controller.mu.Unlock()
		for _, item := range pending {
			if item.timer != nil {
				item.timer.Stop()
			}
			select {
			case item.result <- commandCompletion{err: ErrHostControllerClosed}:
			default:
			}
		}
	})
}

func normalizeWindowMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "windowed"
	}
	return mode
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
		case session.ActionHostSnapshot:
			if controller == nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "host snapshot unavailable"}
			}
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: mustJSON(controller.HostSnapshot())}
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
			var command HostCommandPayload
			if err := decodePayload(request.Payload, &command); err != nil {
				return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: err.Error()}
			}
			command.RequestID = request.RequestID
			var dispatchResults []automation.Result
			apply := runtime.commandApply(command)
			var result func() (json.RawMessage, error)
			if handler := controller.CommandHandler(); handler != nil && (command.Kind == "set_window" || command.Kind == "window_action" || command.Kind == "set_studio_state") {
				var handlerErr error
				apply, result, handlerErr = handler(command)
				if handlerErr != nil {
					return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: handlerErr.Error()}
				}
			}
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
			if result == nil {
				result = func() (json.RawMessage, error) {
					if command.Kind == "dispatch" {
						return json.Marshal(dispatchResults)
					}
					if command.Kind == "assert" {
						return runtime.commandResult(command, controller.HostSnapshot())
					}
					return runtime.commandResult(command)
				}
			}
			data, err := controller.SubmitResult(ctx, HostCommand{RequestID: request.RequestID, Apply: apply, Result: result})
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

type HostCommandPayload struct {
	RequestID    string                         `json:"-"`
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
	Action       string                         `json:"action"`
	Selection    string                         `json:"selection"`
	Zoom         float32                        `json:"zoom"`
	Inspect      *bool                          `json:"inspect"`
	SelectedID   string                         `json:"selected_semantic_id"`
	PanX         *int                           `json:"pan_x"`
	PanY         *int                           `json:"pan_y"`
	Output       string                         `json:"output"`
	OutputSet    bool                           `json:"output_set"`
	ResetState   bool                           `json:"reset_state"`
}

type hostCommandPayload = HostCommandPayload

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

func (runtime *Runtime) commandResult(command hostCommandPayload, hosts ...HostSnapshot) (json.RawMessage, error) {
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
	case "capture", "capture_host_client":
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
		var hostValues map[string]any
		if len(hosts) != 0 {
			encoded, _ := json.Marshal(hosts[0])
			_ = json.Unmarshal(encoded, &hostValues)
		}
		report, err := automation.EvaluateAssertions(automation.AssertionSnapshot{Tree: tree, View: view, Router: snapshot.Router, Editing: snapshot.Editing, StateValues: snapshot.StateValues, ScrollOffsets: snapshot.Scroll, HostValues: hostValues}, command.Assertions)
		if err != nil {
			return nil, err
		}
		value = report
	}
	return json.Marshal(value)
}
