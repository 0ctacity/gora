package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"gora/internal/automation"
	"gora/internal/session"
	"gora/internal/studio"
)

func (backend *hostBackend) startObserver(notify func()) {
	if backend == nil {
		return
	}
	backend.mu.Lock()
	if backend.stop != nil {
		backend.mu.Unlock()
		return
	}
	backend.stop = make(chan struct{})
	backend.done = make(chan struct{})
	backend.notify = notify
	stop, done := backend.stop, backend.done
	backend.mu.Unlock()
	go func() {
		defer func() {
			backend.mu.Lock()
			if backend.done == done {
				backend.done = nil
				backend.stop = nil
			}
			backend.mu.Unlock()
			close(done)
		}()
		for {
			select {
			case <-stop:
				return
			default:
			}
			backend.mu.Lock()
			after := backend.last.FrameRevision
			connected := backend.connected
			backend.mu.Unlock()
			if !connected {
				// Disconnect is quiescent: reconnect is attempted only by a later
				// MCP operation, never by a background timer.
				return
			}
			payload, _ := json.Marshal(map[string]any{"after_frame_revision": after, "after_frame_set": true, "condition": "published", "stable_frames": 1, "timeout_ms": 500})
			response, err := session.Send(backend.socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionWait, Payload: payload}, 600*time.Millisecond)
			if len(response.Data) != 0 {
				var snapshot studio.AutomationSnapshot
				if json.Unmarshal(response.Data, &snapshot) == nil {
					backend.mu.Lock()
					backend.last = snapshot
					backend.mu.Unlock()
					if backend.notify != nil {
						backend.notify()
					}
				}
			}
			if err != nil && response.Error == "" {
				backend.mu.Lock()
				backend.connected = false
				backend.reason = err.Error()
				backend.mu.Unlock()
				if backend.notify != nil {
					backend.notify()
				}
			}
		}
	}()
}

func (backend *hostBackend) close() {
	if backend == nil {
		return
	}
	backend.stopOnce.Do(func() {
		backend.mu.Lock()
		stop, done := backend.stop, backend.done
		backend.mu.Unlock()
		if stop != nil {
			close(stop)
			<-done
		}
	})
}

// ViewBackend is the small ownership boundary between MCP handlers and the
// runtime owner. Headless views use an in-process backend; app and Studio
// views use the host session backend without creating a shadow Runtime.
type ViewBackend interface {
	Mode() session.HostMode
	Connected() bool
	Snapshot(context.Context) (studio.AutomationSnapshot, error)
	Wait(context.Context, studio.WaitForViewRequest) (studio.AutomationSnapshot, error)
	Inspect(context.Context) ([]byte, error)
	SetViewport(context.Context, int, int) error
	Select(context.Context, string) error
	Activate(context.Context, string) error
	Scroll(context.Context, string, string, int, int) error
	SetState(context.Context, string, map[string]any) error
	ResetState(context.Context, string) error
	SetControlValue(context.Context, string, any) (any, error)
	SetFieldDraft(context.Context, string, string) error
	SubmitForm(context.Context, string) error
	ResetForm(context.Context, string) error
	Dispatch(context.Context, []automation.Event) ([]automation.Result, error)
	SetClipboard(context.Context, string) error
	Clipboard(context.Context) (string, error)
	SetClock(context.Context, string) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error)
	AdvanceClock(context.Context, int64, bool) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error)
	Trace(context.Context) (automation.TraceSnapshot, error)
	ConfigureTrace(context.Context, bool, int) (automation.TraceSnapshot, error)
	Capture(context.Context, int) ([]byte, string, automation.CaptureIdentity, error)
	Close()
}

type headlessBackend struct {
	runtime *studio.Runtime
	driver  *automation.Driver
}

func (backend *headlessBackend) Mode() session.HostMode { return session.HostModeHeadless }
func (backend *headlessBackend) Connected() bool        { return backend != nil && backend.runtime != nil }
func (backend *headlessBackend) Snapshot(context.Context) (studio.AutomationSnapshot, error) {
	if backend == nil || backend.runtime == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("headless backend is unavailable")
	}
	return backend.runtime.AutomationSnapshot(), nil
}
func (backend *headlessBackend) Wait(ctx context.Context, request studio.WaitForViewRequest) (studio.AutomationSnapshot, error) {
	if backend == nil || backend.runtime == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("headless backend is unavailable")
	}
	return backend.runtime.WaitForView(ctx, request)
}
func (backend *headlessBackend) Inspect(context.Context) ([]byte, error) {
	if backend == nil || backend.runtime == nil {
		return nil, fmt.Errorf("headless backend is unavailable")
	}
	data, _, err := backend.runtime.Inspect(sessionModeString(session.HostModeHeadless))
	return data, err
}
func (backend *headlessBackend) SetViewport(_ context.Context, width, height int) error {
	backend.runtime.SetViewport(width, height)
	return nil
}
func (backend *headlessBackend) Select(_ context.Context, name string) error {
	if !backend.runtime.SelectScreen(name) {
		return fmt.Errorf("unknown selection %q", name)
	}
	return nil
}
func (backend *headlessBackend) Activate(_ context.Context, id string) error {
	return backend.runtime.ActivateSemanticID(id)
}
func (backend *headlessBackend) Scroll(_ context.Context, id, mode string, x, y int) error {
	return backend.runtime.ScrollSemanticID(id, mode, x, y)
}
func (backend *headlessBackend) SetState(_ context.Context, scope string, values map[string]any) error {
	return backend.runtime.SetStateValues(scope, values)
}
func (backend *headlessBackend) ResetState(_ context.Context, scope string) error {
	return backend.runtime.ResetStateScope(scope)
}
func (backend *headlessBackend) SetControlValue(_ context.Context, id string, value any) (any, error) {
	return backend.runtime.SetControlValue(id, value)
}
func (backend *headlessBackend) SetFieldDraft(_ context.Context, id, draft string) error {
	return backend.runtime.SetFieldDraft(id, draft)
}
func (backend *headlessBackend) SubmitForm(_ context.Context, id string) error {
	return backend.runtime.SubmitForm(id)
}
func (backend *headlessBackend) ResetForm(_ context.Context, id string) error {
	return backend.runtime.ResetForm(id)
}
func (backend *headlessBackend) Dispatch(_ context.Context, events []automation.Event) ([]automation.Result, error) {
	if backend == nil || backend.driver == nil {
		return nil, fmt.Errorf("headless input driver is unavailable")
	}
	return backend.driver.Dispatch(events)
}
func (backend *headlessBackend) SetClipboard(_ context.Context, text string) error {
	backend.runtime.SetAutomationClipboard(text)
	return nil
}
func (backend *headlessBackend) Clipboard(context.Context) (string, error) {
	return backend.runtime.AutomationClipboard(), nil
}
func (backend *headlessBackend) SetClock(_ context.Context, mode string) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error) {
	clock, err := backend.runtime.SetViewClock(mode)
	return clock, backend.runtime.AutomationSnapshot(), err
}
func (backend *headlessBackend) AdvanceClock(_ context.Context, delta int64, idle bool) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error) {
	clock, err := backend.runtime.AdvanceViewClock(delta, idle)
	return clock, backend.runtime.AutomationSnapshot(), err
}
func (backend *headlessBackend) Trace(context.Context) (automation.TraceSnapshot, error) {
	return backend.runtime.EventTrace(), nil
}
func (backend *headlessBackend) ConfigureTrace(_ context.Context, enabled bool, capacity int) (automation.TraceSnapshot, error) {
	err := backend.runtime.ConfigureEventTrace(enabled, capacity)
	return backend.runtime.EventTrace(), err
}
func (backend *headlessBackend) Capture(_ context.Context, scale int) ([]byte, string, automation.CaptureIdentity, error) {
	return backend.runtime.CapturePNGReadOnly(scale)
}
func (backend *headlessBackend) Close() {
	if backend != nil && backend.runtime != nil {
		backend.runtime.Close()
	}
}

func sessionModeString(mode session.HostMode) string { return string(mode) }

func (backend *hostBackend) Mode() session.HostMode {
	if backend == nil {
		return ""
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.identity.Mode
}
func (backend *hostBackend) Connected() bool {
	if backend == nil {
		return false
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.connected
}
func (backend *hostBackend) Snapshot(_ context.Context) (studio.AutomationSnapshot, error) {
	if backend == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("host backend is unavailable")
	}
	backend.mu.Lock()
	entry := backend.identity.Document
	backend.mu.Unlock()
	backend.refresh(entry)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !backend.connected {
		return backend.last, fmt.Errorf("attached host is disconnected: %s", backend.reason)
	}
	return backend.last, nil
}
func (backend *hostBackend) Wait(ctx context.Context, request studio.WaitForViewRequest) (studio.AutomationSnapshot, error) {
	if backend == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("host backend is unavailable")
	}
	return waitHostBackend(ctx, backend, request)
}
func (backend *hostBackend) Inspect(_ context.Context) ([]byte, error) {
	if backend == nil {
		return nil, fmt.Errorf("host backend is unavailable")
	}
	if err := backend.ensureConnected(); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	socketPath := backend.socketPath
	backend.mu.Unlock()
	response, err := session.Send(socketPath, session.Request{Action: "inspect"}, 500*time.Millisecond)
	if err != nil {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.connected = false
		backend.reason = err.Error()
		return nil, err
	}
	if !response.OK {
		err := fmt.Errorf("host inspection rejected: %s", response.Error)
		backend.mu.Lock()
		backend.connected = false
		backend.reason = err.Error()
		backend.mu.Unlock()
		return nil, err
	}
	return response.Data, nil
}
func (backend *hostBackend) ensureConnected() error {
	if backend == nil {
		return fmt.Errorf("host backend is unavailable")
	}
	backend.mu.Lock()
	connected := backend.connected
	entry := backend.identity.Document
	notify := backend.notify
	backend.mu.Unlock()
	if !connected {
		backend.refresh(entry)
		backend.mu.Lock()
		connected = backend.connected
		notify = backend.notify
		backend.mu.Unlock()
		if connected {
			backend.startObserver(notify)
		}
	}
	if !connected {
		backend.mu.Lock()
		reason := backend.reason
		backend.mu.Unlock()
		return fmt.Errorf("attached host is disconnected: %s", reason)
	}
	return nil
}
func (backend *hostBackend) command(ctx context.Context, kind string, payload any, target any) error {
	if err := backend.ensureConnected(); err != nil {
		return err
	}
	body := map[string]any{"kind": kind}
	if encoded, err := json.Marshal(payload); err == nil {
		var fields map[string]any
		if json.Unmarshal(encoded, &fields) == nil {
			for key, value := range fields {
				body[key] = value
			}
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 {
		timeout = time.Until(deadline)
	}
	backend.mu.Lock()
	socketPath := backend.socketPath
	backend.mu.Unlock()
	response, err := session.Send(socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionCommand, Payload: data}, timeout)
	if err != nil {
		backend.mu.Lock()
		wasConnected := backend.connected
		backend.connected = false
		backend.reason = err.Error()
		notify := backend.notify
		backend.mu.Unlock()
		if wasConnected && notify != nil {
			notify()
		}
		return err
	}
	if !response.OK {
		err := fmt.Errorf("host command rejected: %s", response.Error)
		backend.mu.Lock()
		backend.connected = false
		backend.reason = err.Error()
		backend.mu.Unlock()
		return err
	}
	if target != nil && len(response.Data) != 0 {
		return json.Unmarshal(response.Data, target)
	}
	return nil
}
func (backend *hostBackend) SetViewport(ctx context.Context, width, height int) error {
	return backend.command(ctx, "set_viewport", map[string]any{"width": width, "height": height}, nil)
}
func (backend *hostBackend) Select(ctx context.Context, name string) error {
	return backend.command(ctx, "select", map[string]any{"name": name}, nil)
}
func (backend *hostBackend) Activate(ctx context.Context, id string) error {
	return backend.command(ctx, "activate", map[string]any{"semantic_id": id}, nil)
}
func (backend *hostBackend) Scroll(ctx context.Context, id, mode string, x, y int) error {
	return backend.command(ctx, "scroll", map[string]any{"semantic_id": id, "mode": mode, "x": x, "y": y}, nil)
}
func (backend *hostBackend) SetState(ctx context.Context, scope string, values map[string]any) error {
	return backend.command(ctx, "set_state", map[string]any{"scope_id": scope, "values": values}, nil)
}
func (backend *hostBackend) ResetState(ctx context.Context, scope string) error {
	return backend.command(ctx, "reset_state", map[string]any{"scope_id": scope}, nil)
}
func (backend *hostBackend) SetControlValue(ctx context.Context, id string, value any) (any, error) {
	var result struct {
		Value any `json:"value"`
	}
	err := backend.command(ctx, "set_control_value", map[string]any{"semantic_id": id, "value": value}, &result)
	return result.Value, err
}
func (backend *hostBackend) SetFieldDraft(ctx context.Context, id, draft string) error {
	return backend.command(ctx, "set_field_draft", map[string]any{"semantic_id": id, "draft": draft}, nil)
}
func (backend *hostBackend) SubmitForm(ctx context.Context, id string) error {
	return backend.command(ctx, "submit_form", map[string]any{"semantic_id": id}, nil)
}
func (backend *hostBackend) ResetForm(ctx context.Context, id string) error {
	return backend.command(ctx, "reset_form", map[string]any{"semantic_id": id}, nil)
}
func (backend *hostBackend) Dispatch(ctx context.Context, events []automation.Event) ([]automation.Result, error) {
	var results []automation.Result
	err := backend.command(ctx, "dispatch", map[string]any{"events": events}, &results)
	return results, err
}
func (backend *hostBackend) SetClipboard(ctx context.Context, text string) error {
	return backend.command(ctx, "set_clipboard", map[string]any{"text": text}, nil)
}
func (backend *hostBackend) Clipboard(ctx context.Context) (string, error) {
	var result struct {
		Text string `json:"text"`
	}
	err := backend.command(ctx, "get_clipboard", nil, &result)
	return result.Text, err
}
func (backend *hostBackend) SetClock(ctx context.Context, mode string) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error) {
	var result struct {
		Clock    studio.ViewClockSnapshot  `json:"clock"`
		Snapshot studio.AutomationSnapshot `json:"snapshot"`
	}
	err := backend.command(ctx, "set_clock", map[string]any{"mode": mode}, &result)
	return result.Clock, result.Snapshot, err
}
func (backend *hostBackend) AdvanceClock(ctx context.Context, delta int64, idle bool) (studio.ViewClockSnapshot, studio.AutomationSnapshot, error) {
	var result struct {
		Clock    studio.ViewClockSnapshot  `json:"clock"`
		Snapshot studio.AutomationSnapshot `json:"snapshot"`
	}
	err := backend.command(ctx, "advance_clock", map[string]any{"delta_ms": delta, "run_until_idle": idle}, &result)
	return result.Clock, result.Snapshot, err
}
func (backend *hostBackend) Trace(ctx context.Context) (automation.TraceSnapshot, error) {
	var result automation.TraceSnapshot
	err := backend.command(ctx, "get_trace", nil, &result)
	return result, err
}
func (backend *hostBackend) ConfigureTrace(ctx context.Context, enabled bool, capacity int) (automation.TraceSnapshot, error) {
	var result automation.TraceSnapshot
	err := backend.command(ctx, "configure_trace", map[string]any{"enabled": enabled, "capacity": capacity}, &result)
	return result, err
}
func (backend *hostBackend) Capture(ctx context.Context, scale int) ([]byte, string, automation.CaptureIdentity, error) {
	var result struct {
		PNGBase64 string                     `json:"png_base64"`
		Warning   string                     `json:"warning"`
		Identity  automation.CaptureIdentity `json:"identity"`
	}
	err := backend.command(ctx, "capture", map[string]any{"scale": scale}, &result)
	if err != nil {
		return nil, "", automation.CaptureIdentity{}, err
	}
	png, err := base64.StdEncoding.DecodeString(result.PNGBase64)
	return png, result.Warning, result.Identity, err
}
func (backend *hostBackend) Close() { backend.close() }

func waitHostBackend(ctx context.Context, backend *hostBackend, request studio.WaitForViewRequest) (studio.AutomationSnapshot, error) {
	if err := backend.ensureConnected(); err != nil {
		return studio.AutomationSnapshot{}, err
	}
	payload := struct {
		AfterFrameRevision   uint64 `json:"after_frame_revision"`
		AfterFrameSet        bool   `json:"after_frame_set"`
		AfterRuntimeRevision uint64 `json:"after_runtime_revision"`
		AfterRuntimeSet      bool   `json:"after_runtime_set"`
		Condition            string `json:"condition"`
		StableFrames         int    `json:"stable_frames"`
		TimeoutMS            int    `json:"timeout_ms"`
	}{request.AfterFrameRevision, request.AfterFrameSet, request.AfterRuntimeRevision, request.AfterRuntimeSet, request.Condition, request.StableFrames, int(request.Timeout / 1000000)}
	data, err := json.Marshal(payload)
	if err != nil {
		return studio.AutomationSnapshot{}, err
	}
	backend.mu.Lock()
	socketPath := backend.socketPath
	backend.mu.Unlock()
	response, err := session.Send(socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionWait, Payload: data}, request.Timeout)
	if err != nil {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if response.Error != "" {
			var snapshot studio.AutomationSnapshot
			_ = json.Unmarshal(response.Data, &snapshot)
			backend.last = snapshot
			return snapshot, err
		}
		backend.connected = false
		backend.reason = err.Error()
		return backend.last, err
	}
	var snapshot studio.AutomationSnapshot
	_ = json.Unmarshal(response.Data, &snapshot)
	backend.mu.Lock()
	backend.last = snapshot
	backend.mu.Unlock()
	if !response.OK {
		return snapshot, fmt.Errorf("host wait rejected: %s", response.Error)
	}
	return snapshot, nil
}
