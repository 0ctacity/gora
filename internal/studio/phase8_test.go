package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"gora/internal/semantic"
	"gora/internal/session"
)

func TestHostControllerPublishesImmutableHostSnapshot(t *testing.T) {
	controller := NewHostController(4)
	controller.SetHostIdentity("host-1", "app", 42, []string{"capture", "snapshot"})
	controller.UpdateWindowState(image.Pt(640, 480), image.Pt(1280, 960), 2, 2, "windowed", true, true, false)
	if beforePublish := controller.HostSnapshot(); beforePublish.LogicalClientWidth != 0 {
		t.Fatalf("unpublished config leaked into host snapshot: %+v", beforePublish)
	}
	controller.Publish(HostPublication{RuntimeRevision: 3, GeometryRevision: 4, FrameRevision: 5, InputRevision: 6, TraceRevision: 7})

	snapshot := controller.HostSnapshot()
	if snapshot.HostInstanceID != "host-1" || snapshot.Mode != "app" {
		t.Fatalf("unexpected identity: %+v", snapshot)
	}
	if snapshot.LogicalClientWidth != 640 || snapshot.PhysicalClientWidth != 1280 || snapshot.PxPerDp != 2 {
		t.Fatalf("unexpected metrics: %+v", snapshot)
	}
	if snapshot.RuntimeRevision != 3 || snapshot.FrameRevision != 5 || snapshot.InputRevision != 6 || snapshot.TraceRevision != 7 {
		t.Fatalf("unexpected revisions: %+v", snapshot)
	}
	if snapshot.HostRevision == 0 || snapshot.HostRevision == snapshot.FrameRevision {
		// Host and frame revisions are independent publication clocks. They may
		// coincide by chance on the first frame, but a second publication must
		// prove they do not alias.
		controller.Publish(HostPublication{FrameRevision: 5})
		if next := controller.HostSnapshot(); next.HostRevision <= snapshot.HostRevision || next.HostRevision == next.FrameRevision {
			t.Fatalf("host revision did not remain independent: first=%+v next=%+v", snapshot, next)
		}
	}
	snapshot.Capabilities[0] = "mutated"
	if controller.HostSnapshot().Capabilities[0] == "mutated" {
		t.Fatal("host snapshot leaked mutable capabilities")
	}
	if _, err := json.Marshal(controller.HostSnapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestStudioControllerAppliesAtomicStateAndClampsPan(t *testing.T) {
	controller := NewStudioController()
	controller.SetCanvas(image.Pt(100, 80), image.Pt(300, 200))
	if err := controller.Apply(StudioStateChange{Zoom: float32Pointer(2), PanX: intPointer(500), PanY: intPointer(-10)}); err != nil {
		t.Fatal(err)
	}
	state := controller.Snapshot()
	if state.Zoom != 2 || state.CanvasPanX != 200 || state.CanvasPanY != 0 {
		t.Fatalf("pan was not clamped: %+v", state)
	}
	revision := state.Revision
	if err := controller.Apply(StudioStateChange{Zoom: float32Pointer(99), PanX: intPointer(-1)}); err == nil {
		t.Fatal("expected zoom validation failure")
	}
	if after := controller.Snapshot(); after.Revision != revision || after.Zoom != 2 || after.CanvasPanX != 200 {
		t.Fatalf("failed atomic update changed state: before=%d after=%+v", revision, after)
	}
}

func TestStudioControllerToolbarAndMCPTransitionsShareState(t *testing.T) {
	controller := NewStudioController()
	selection := "reports"
	inspect := true
	zoom := float32(1.5)
	if err := controller.Apply(StudioStateChange{Selection: &selection, Inspect: &inspect, Zoom: &zoom, Status: stringPointer("ready")}); err != nil {
		t.Fatal(err)
	}
	mcpState := controller.Snapshot()
	controller.SyncFromUI(StudioState{Selection: selection, Inspect: inspect, Zoom: zoom, Status: "ready"})
	toolbarState := controller.Snapshot()
	if mcpState.Selection != toolbarState.Selection || mcpState.Inspect != toolbarState.Inspect || mcpState.Zoom != toolbarState.Zoom || toolbarState.Status != "ready" {
		t.Fatalf("controller paths diverged: mcp=%+v toolbar=%+v", mcpState, toolbarState)
	}
}

func TestStudioControllerPrunesReloadedInspectedNode(t *testing.T) {
	controller := NewStudioController()
	selected := "screen/main/button"
	if err := controller.Apply(StudioStateChange{SelectedSemanticID: &selected}); err != nil {
		t.Fatal(err)
	}
	revision := controller.Snapshot().Revision
	controller.PruneSelection(map[string]bool{"screen/main/other": true})
	state := controller.Snapshot()
	if state.SelectedSemanticID != "" || state.Revision <= revision {
		t.Fatalf("selection was not pruned: %+v", state)
	}
}

func TestStudioSelectionRequiresCanonicalSemanticID(t *testing.T) {
	root := &semantic.Node{ID: "screen/main", Visible: true, Children: []*semantic.Node{{ID: "screen/main/save", Handle: "internal-handle", Visible: true, InViewport: true, Bounds: &semantic.Rect{Width: 20, Height: 10}}}}
	selected := selectableSemanticNode(root, "screen/main/save")
	if selected == nil || selected.Handle != "internal-handle" {
		t.Fatalf("canonical selection did not resolve: %+v", selected)
	}
	if selectableSemanticNode(root, "internal-handle") != nil {
		t.Fatal("renderer handle was accepted as semantic selection ID")
	}
}

func TestStudioTransitionValidationRollsBackAllOwners(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n  secondary: { type: spacer }\n")
	if err := os.WriteFile(entry, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	state := newStudioUIState(runtime)
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	beforeController := state.controller.Snapshot()
	beforeRuntime := runtime.AutomationSnapshot()
	beforeOutput := state.output.Text()
	inspect := true
	selected := "missing"
	zoom := float32(2)
	if err := applyStudioUITransition(runtime, state, StudioStateChange{Inspect: &inspect, SelectedSemanticID: &selected, Zoom: &zoom}); err == nil {
		t.Fatal("invalid semantic selection was accepted")
	}
	if after := state.controller.Snapshot(); after != beforeController {
		t.Fatalf("controller changed after rejected transition: before=%+v after=%+v", beforeController, after)
	}
	if after := runtime.AutomationSnapshot(); after.RuntimeRevision != beforeRuntime.RuntimeRevision || after.FrameRevision != beforeRuntime.FrameRevision || after.Transient != beforeRuntime.Transient {
		t.Fatalf("runtime changed after rejected transition: before=%+v after=%+v", beforeRuntime, after)
	}
	if state.inspecting || state.selectedSemanticID != "" || state.output.Text() != beforeOutput {
		t.Fatalf("UI changed after rejected transition: %+v", state)
	}
	secondary := "secondary"
	badZoom := float32(9)
	if err := applyStudioUITransition(runtime, state, StudioStateChange{Selection: &secondary, Zoom: &badZoom}); err == nil {
		t.Fatal("invalid zoom was accepted")
	}
	if after := runtime.Snapshot(); after.Screen != "main" {
		t.Fatalf("runtime selection changed after invalid zoom: %+v", after)
	}
}

func TestStudioTransitionSelectionThenSemanticIDUsesTargetScreen(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main:\n    type: text\n    name: main-copy\n    props: { text: Main }\n  secondary:\n    type: text\n    name: target-copy\n    props: { text: Secondary }\n")
	if err := os.WriteFile(entry, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.SelectScreen("secondary") {
		t.Fatal("secondary screen was unavailable")
	}
	targetTree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	var targetID string
	for _, node := range semantic.Flatten(targetTree) {
		if node.Name == "target-copy" {
			targetID = node.ID
			break
		}
	}
	if targetID == "" {
		t.Fatal("target semantic ID was not published")
	}
	if !runtime.SelectScreen("main") {
		t.Fatal("main screen was unavailable")
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	state := newStudioUIState(runtime)
	selection := "secondary"
	inspect := true
	if err := applyStudioUITransition(runtime, state, StudioStateChange{Selection: &selection, Inspect: &inspect, SelectedSemanticID: &targetID}); err != nil {
		t.Fatalf("ordered target-screen selection failed: %v", err)
	}
	if snapshot := runtime.Snapshot(); snapshot.Screen != "secondary" {
		t.Fatalf("runtime screen=%q", snapshot.Screen)
	}
	if snapshot := state.controller.Snapshot(); snapshot.SelectedSemanticID != targetID || !snapshot.Inspect {
		t.Fatalf("studio state=%+v", snapshot)
	}
}

func TestHostControllerEvaluatesResultAfterPublishedRevision(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	done := make(chan json.RawMessage, 1)
	go func() {
		data, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "revision", Result: func() (json.RawMessage, error) {
			return mustJSON(controller.HostSnapshot().FrameRevision), nil
		}})
		if err != nil {
			t.Errorf("submit: %v", err)
			return
		}
		done <- data
	}()
	var commands []HostCommand
	deadline := time.Now().Add(time.Second)
	for len(commands) == 0 && time.Now().Before(deadline) {
		commands = controller.Drain()
		if len(commands) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("commands=%d", len(commands))
	}
	controller.Complete(commands[0].RequestID, nil)
	controller.Publish(HostPublication{FrameRevision: 9})
	select {
	case data := <-done:
		if string(data) != "9" {
			t.Fatalf("result=%s", data)
		}
	case <-time.After(time.Second):
		t.Fatal("result did not complete")
	}
}

func TestHostControllerCapturesLatestClientFrameWithoutPublishing(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	ops := &op.Ops{}
	paint.FillShape(ops, colorNavy, clip.Rect{Max: image.Pt(10, 8)}.Op())
	controller.SetHostIdentity("capture-host", "app", 7, []string{"capture"})
	controller.Publish(HostPublication{RuntimeRevision: 3, GeometryRevision: 4, FrameRevision: 5, ClientFrame: &HostClientFrame{Ops: ops, Size: image.Pt(10, 8)}})
	before := controller.HostSnapshot()
	data, _, identity, err := controller.CaptureClientPNG(2)
	if err != nil {
		t.Skipf("client capture unavailable in this environment: %v", err)
	}
	decoded, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Width != 20 || decoded.Height != 16 || identity.Width != 20 || identity.Height != 16 {
		t.Fatalf("capture dimensions=%dx%d identity=%+v", decoded.Width, decoded.Height, identity)
	}
	after := controller.HostSnapshot()
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(afterJSON) != string(beforeJSON) {
		t.Fatalf("capture changed host publication: before=%+v after=%+v", before, after)
	}
}

func TestHostSessionRoutesWindowCommandThroughHostHandler(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	runtime := NewRuntimeAllowInvalid(root, entry)
	controller := NewHostController(2)
	defer controller.Close()
	controller.SetCommandHandler(func(command HostCommandPayload) (func() error, func() (json.RawMessage, error), error) {
		if command.Kind != "set_window" {
			t.Fatalf("kind=%q", command.Kind)
		}
		if command.RequestID != "window" {
			t.Fatalf("request id=%q", command.RequestID)
		}
		return func() error { return nil }, func() (json.RawMessage, error) { return mustJSON(map[string]any{"applied": true}), nil }, nil
	})
	identity := session.HostIdentity{InstanceID: "phase8-handler", Root: root, Document: entry, Mode: session.HostModeApp, PID: 7, Automation: true, Capabilities: []string{"window"}}
	handler := runtime.SessionHandlerWithController("app", identity, controller, nil)
	payload, _ := json.Marshal(map[string]any{"kind": "set_window", "width": 640, "height": 480})
	done := make(chan session.Response, 1)
	go func() {
		done <- handler(context.Background(), session.Request{Version: session.ProtocolVersion, RequestID: "window", Action: session.ActionCommand, Payload: payload})
	}()
	var commands []HostCommand
	deadline := time.Now().Add(time.Second)
	for len(commands) == 0 && time.Now().Before(deadline) {
		commands = controller.Drain()
		if len(commands) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("commands=%d", len(commands))
	}
	controller.Complete(commands[0].RequestID, commands[0].Apply())
	controller.Publish(HostPublication{FrameRevision: 2})
	select {
	case response := <-done:
		if !response.OK || string(response.Data) != `{"applied":true}` {
			t.Fatalf("response=%+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("window command did not complete")
	}
}

func TestHostControllerCompleteNowAcknowledgesCloseWithoutFrame(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "close"})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	var commands []HostCommand
	for len(commands) == 0 && time.Now().Before(deadline) {
		commands = controller.Drain()
		if len(commands) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("commands=%d", len(commands))
	}
	controller.CompleteNow("close", mustJSON(map[string]any{"acknowledged": true}), nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close acknowledgement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close acknowledgement did not wake waiter")
	}
}

func TestHostControllerCloseWakesPendingWaiterAndRetainsBoundedState(t *testing.T) {
	controller := NewHostController(2)
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "disconnect"})
		done <- err
	}()
	_ = waitHostCommands(t, controller, 1)
	controller.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHostControllerClosed) {
			t.Fatalf("close error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake waiter")
	}
	controller.mu.Lock()
	queue, pending := len(controller.queue), len(controller.pending)
	controller.mu.Unlock()
	if queue != 0 || pending != 0 {
		t.Fatalf("commands retained after close: queue=%d pending=%d", queue, pending)
	}
}

func TestHostControllerWindowTransitionWaitsForConfigAndStableFrame(t *testing.T) {
	controller := NewHostController(4)
	defer controller.Close()
	controller.transitionTimeout = 20 * time.Millisecond
	controller.SetHostIdentity("barrier-host", "app", 1, nil)
	controller.UpdateWindowState(image.Pt(100, 80), image.Pt(100, 80), 1, 1, "windowed", true, true, false)
	controller.Publish(HostPublication{FrameRevision: 1})
	controller.RequireConfigBarrier("transition")
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "transition", Result: func() (json.RawMessage, error) {
			return mustJSON(controller.HostSnapshot()), nil
		}})
		done <- err
	}()
	commands := waitHostCommands(t, controller, 1)
	controller.Complete(commands[0].RequestID, nil, mustJSON(map[string]any{"ok": true}))
	controller.Publish(HostPublication{FrameRevision: 2})
	select {
	case err := <-done:
		t.Fatalf("transition completed before ConfigEvent: %v", err)
	case <-time.After(5 * time.Millisecond):
	}
	controller.UpdateWindowState(image.Pt(120, 80), image.Pt(120, 80), 1, 1, "windowed", true, true, false)
	controller.Publish(HostPublication{FrameRevision: 3})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("transition after ConfigEvent: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transition did not complete after stable frame")
	}
}

func TestHostControllerWindowTransitionAcceptsChangedFrameMetrics(t *testing.T) {
	controller := NewHostController(4)
	defer controller.Close()
	controller.transitionTimeout = 50 * time.Millisecond
	controller.SetHostIdentity("frame-metrics-host", "app", 1, nil)
	controller.UpdateMetrics(image.Pt(100, 80), image.Pt(200, 160), 2, 2)
	controller.Publish(HostPublication{FrameRevision: 1})
	controller.RequireConfigBarrier("transition")
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "transition"})
		done <- err
	}()
	commands := waitHostCommands(t, controller, 1)
	controller.Complete(commands[0].RequestID, nil)

	// Gio on macOS can deliver the resized client through FrameEvent without a
	// separate ConfigEvent. The observed client metrics are still a native
	// configuration change and must satisfy the barrier.
	controller.UpdateMetrics(image.Pt(120, 90), image.Pt(240, 180), 2, 2)
	controller.Publish(HostPublication{FrameRevision: 2})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("transition after changed frame metrics: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("changed frame metrics did not complete the transition")
	}
}

func TestAppWindowNoOpDoesNotRequireConfigurationBarrier(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	controller.SetHostIdentity("no-op-host", "app", 1, nil)
	controller.UpdateWindowState(image.Pt(920, 730), image.Pt(1840, 1460), 2, 2, "windowed", true, true, false)
	controller.Publish(HostPublication{FrameRevision: 1})

	if _, _, err := appWindowCommandHandler(new(app.Window), controller)(HostCommandPayload{RequestID: "same", Kind: "set_window", Width: 920, Height: 730, Mode: "windowed"}); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	_, waiting := controller.barriers["same"]
	controller.mu.Unlock()
	if waiting {
		t.Fatal("already-observed window state installed a configuration barrier")
	}
}

func TestHostControllerWindowTransitionTimesOutTruthfully(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	controller.transitionTimeout = 30 * time.Millisecond
	controller.SetHostIdentity("timeout-host", "app", 1, nil)
	controller.RequireConfigBarrier("timeout")
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "timeout"})
		done <- err
	}()
	commands := waitHostCommands(t, controller, 1)
	controller.Complete(commands[0].RequestID, nil)
	controller.Publish(HostPublication{FrameRevision: 1})
	select {
	case err := <-done:
		t.Fatalf("transition timed out before its deadline: %v", err)
	case <-time.After(5 * time.Millisecond):
	}
	time.Sleep(35 * time.Millisecond)
	controller.Publish(HostPublication{FrameRevision: 2})
	select {
	case err := <-done:
		if err == nil || err.Error() != "window transition timed out waiting for ConfigEvent" {
			t.Fatalf("unexpected timeout error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transition timeout did not wake waiter")
	}
}

func TestHostControllerWindowTransitionTimesOutWithoutAnotherFrame(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	controller.transitionTimeout = 5 * time.Millisecond
	controller.SetHostIdentity("stalled-host", "app", 1, nil)
	controller.RequireConfigBarrier("stalled")
	done := make(chan error, 1)
	go func() {
		_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "stalled"})
		done <- err
	}()
	commands := waitHostCommands(t, controller, 1)
	controller.Complete(commands[0].RequestID, nil)
	controller.Publish(HostPublication{FrameRevision: 1})
	select {
	case err := <-done:
		if err == nil || err.Error() != "window transition timed out waiting for ConfigEvent" {
			t.Fatalf("unexpected timeout error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled transition did not time out without another frame")
	}
}

func waitHostCommands(t *testing.T, controller *HostController, expected int) []HostCommand {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		commands := controller.Drain()
		if len(commands) == expected {
			return commands
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("host command was not drained")
	return nil
}

func TestHostControllerHundredCommandCyclesRemainBounded(t *testing.T) {
	controller := NewHostController(4)
	defer controller.Close()
	for cycle := 0; cycle < 100; cycle++ {
		done := make(chan error, 1)
		go func(cycle int) {
			_, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: fmt.Sprintf("cycle-%d", cycle)})
			done <- err
		}(cycle)
		commands := waitHostCommands(t, controller, 1)
		controller.Complete(commands[0].RequestID, nil)
		controller.Publish(HostPublication{FrameRevision: uint64(cycle + 1)})
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("cycle %d: %v", cycle, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("cycle %d did not complete", cycle)
		}
	}
	controller.mu.Lock()
	queue, pending := len(controller.queue), len(controller.pending)
	controller.mu.Unlock()
	if queue != 0 || pending != 0 {
		t.Fatalf("retained host commands: queue=%d pending=%d", queue, pending)
	}
}

func float32Pointer(value float32) *float32 { return &value }
func intPointer(value int) *int             { return &value }
func stringPointer(value string) *string    { return &value }

var colorNavy = color.NRGBA{R: 20, G: 30, B: 50, A: 255}
