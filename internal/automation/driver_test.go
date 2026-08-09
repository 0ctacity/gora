package automation

import (
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/scrollinput"
	"gora/internal/semantic"
)

func TestDriverRejectsAnInvalidBatchBeforeDeliveringEarlierEvents(t *testing.T) {
	driver := NewDriver(&fakeRuntime{tree: &semantic.Node{}})
	_, err := driver.Dispatch([]Event{
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 1},
		{Type: "key", Kind: "down", Name: "NotAKey", TimeMS: 2},
	})
	if err == nil {
		t.Fatal("Dispatch accepted an invalid batch")
	}
	if got := driver.Router().Transient().Focused; got != "" {
		t.Fatalf("invalid batch delivered event 1, focus=%q", got)
	}
}

func TestDriverScrollNormalizesPhysicalDiagonalAndRoutesBothAxes(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree()}
	runtime.scrollScale = 2
	driver := NewDriver(runtime)
	results, err := driver.Dispatch([]Event{{Type: "scroll", Source: "trackpad", X: 20, Y: 30, DeltaX: 18.5, DeltaY: -42, Units: "physical_pixels", Phase: "update", Momentum: "none", TimeMS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ScrollRouting == nil {
		t.Fatalf("results=%+v", results)
	}
	got := results[0].ScrollRouting
	if got.LogicalDeltaX != 9 || got.LogicalDeltaY != -21 {
		t.Fatalf("normalized scroll=%+v", got)
	}
}

func TestDriverScrollAcceptsPhaseOnlyAndRejectsInvalidScrollBatch(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree()}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "wheel", Units: "logical", Phase: "begin", Momentum: "begin", TimeMS: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.scrollInputs) != 1 || runtime.scrollInputs[0].DeltaX != 0 || runtime.scrollInputs[0].DeltaY != 0 {
		t.Fatalf("phase-only input=%+v", runtime.scrollInputs)
	}
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "wheel", Units: "logical", DeltaX: 1, Phase: "bogus", TimeMS: 2}}); err == nil {
		t.Fatal("invalid scroll phase accepted")
	}
}

func TestDriverScrollMomentumSequenceAndCancel(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree()}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "trackpad", Units: "logical", Phase: "begin", Momentum: "begin", TimeMS: 1}, {Type: "scroll", Source: "trackpad", Units: "logical", Phase: "update", Momentum: "update", TimeMS: 2}, {Type: "scroll", Source: "trackpad", Units: "logical", Phase: "cancel", Momentum: "end", TimeMS: 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "trackpad", Units: "logical", Phase: "update", Momentum: "update", TimeMS: 4}}); err == nil {
		t.Fatal("momentum update after cancel accepted")
	}
}

func TestDriverScrollTraceUsesRoutingStages(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree()}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "wheel", X: 5, Y: 6, DeltaY: 4, Units: "logical", Phase: "update", Momentum: "none", TimeMS: 1}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "conversion", "candidates", "owner_selection", "capture_decision", "mutation", "invalidation", "publication"}
	if len(runtime.traces) < len(want) {
		t.Fatalf("traces=%+v", runtime.traces)
	}
	for i, stage := range want {
		if runtime.traces[i].Stage != stage {
			t.Fatalf("trace[%d]=%+v, want stage %q", i, runtime.traces[i], stage)
		}
	}
}

func TestDriverTracePublicationIsEventLocalForPhaseOnlyAndBlockedScroll(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree()}
	driver := NewDriverWithSnapshot(runtime, func() RevisionSnapshot {
		return RevisionSnapshot{RuntimeRevision: 4, FrameRevision: 9, GeometryRevision: 7, PublishedRuntimeRevision: 4, PublishedGeometryRevision: 7}
	})
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "wheel", Units: "logical", Phase: "begin", Momentum: "none", TimeMS: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "scroll", Source: "wheel", Units: "logical", DeltaX: 10, DeltaY: 5, Modifiers: []string{"command"}, Phase: "update", Momentum: "none", TimeMS: 2}}); err != nil {
		t.Fatal(err)
	}
	publications := make([]TraceEntry, 0, 2)
	for _, trace := range runtime.traces {
		if trace.Stage == "publication" {
			publications = append(publications, trace)
		}
	}
	if len(publications) != 2 || publications[0].FrameBefore != publications[0].FrameAfter || publications[1].FrameBefore != publications[1].FrameAfter {
		t.Fatalf("event-local publications=%+v", publications)
	}
	if publications[0].Outcome != "phase_only" || publications[1].Outcome != "command_modified_headless_scroll_blocked" {
		t.Fatalf("publication reasons=%+v", publications)
	}
}

func TestDriverPointerAndKeyboardTraceOwnershipStages(t *testing.T) {
	button := interactiveNode("button", "button", "button", image.Rect(0, 0, 80, 30), image.Rect(0, 0, 100, 100), 0, 2)
	runtime := &fakeRuntime{tree: runtimeTree(button)}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1}, {Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 2}, {Type: "key", Kind: "down", Name: "Tab", TimeMS: 3}, {Type: "key", Kind: "down", Name: "Space", TimeMS: 4}}); err != nil {
		t.Fatal(err)
	}
	var captures, owners int
	for _, trace := range runtime.traces {
		if trace.Stage == "capture_decision" {
			captures++
		}
		if trace.Stage == "owner_selection" && trace.TargetID != "" {
			owners++
		}
	}
	if captures < 4 || owners < 4 {
		t.Fatalf("trace ownership stages captures=%d owners=%d traces=%+v", captures, owners, runtime.traces)
	}
}

func TestTraceRecorderRetainsBoundedEntriesAcrossThousandEvents(t *testing.T) {
	recorder := NewTraceRecorder()
	if err := recorder.Configure(true, 8); err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := recorder.Subscribe()
	defer unsubscribe()
	for i := 0; i < 1000; i++ {
		recorder.Record(TraceEntry{Stage: "accepted", EventIndex: i})
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Entries) != 8 || snapshot.Entries[len(snapshot.Entries)-1].EventIndex != 999 {
		t.Fatalf("bounded snapshot=%+v", snapshot)
	}
	select {
	case <-updates:
	default:
		t.Fatal("subscriber did not receive coalesced update")
	}
}

func TestDriverDispatchesPrimaryPressAndReleaseThroughRouter(t *testing.T) {
	node := &semantic.Node{ID: "button", Handle: "button", Role: "button", Type: "button", Visible: true, InViewport: true, Enabled: true, Bounds: rect(0, 0, 100, 40), Clip: rect(0, 0, 100, 40), FocusOrder: 0}
	driver := NewDriver(&fakeRuntime{tree: node})
	results, err := driver.Dispatch([]Event{
		{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1},
		{Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].TargetID != "button" || results[1].TargetID != "button" || !results[1].Consumed {
		t.Fatalf("results=%+v", results)
	}
}

func TestDriverDoesNotDeliverAnyEventFromAnInvalidTimeline(t *testing.T) {
	node := &semantic.Node{ID: "button", Handle: "button", Role: "button", Type: "button", Visible: true, InViewport: true, Enabled: true, Bounds: rect(0, 0, 100, 40), Clip: rect(0, 0, 100, 40), FocusOrder: 0}
	runtime := &fakeRuntime{tree: node}
	driver := NewDriver(runtime)
	_, err := driver.Dispatch([]Event{
		{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1},
		{Type: "pointer", Kind: "release", PointerID: 99, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 2},
	})
	if err == nil {
		t.Fatal("invalid pointer timeline was accepted")
	}
	if got := driver.Router().Snapshot(); got.PointerCapture != nil || len(got.PressedIDs) != 0 || len(runtime.activations) != 0 {
		t.Fatalf("invalid timeline mutated state: snapshot=%+v activations=%d", got, len(runtime.activations))
	}
}

func TestDriverReleaseOutsideDoesNotActivateAndDragBackActivatesOnce(t *testing.T) {
	node := &semantic.Node{ID: "button", Handle: "button", Role: "button", Type: "button", Visible: true, InViewport: true, Enabled: true, Bounds: rect(0, 0, 100, 40), Clip: rect(0, 0, 100, 40), FocusOrder: 0}
	runtime := &fakeRuntime{tree: node}
	driver := NewDriver(runtime)
	results, err := driver.Dispatch([]Event{
		{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1},
		{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 140, Y: 10, TimeMS: 2},
		{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 10, Y: 10, TimeMS: 3},
		{Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 4},
	})
	if err != nil || len(results) != 4 {
		t.Fatalf("dispatch error=%v results=%+v", err, results)
	}
	if len(runtime.activations) != 1 {
		t.Fatalf("activations=%d, want 1", len(runtime.activations))
	}
}

func TestDriverPublishesPointerMetadataAndClearsItOnRelease(t *testing.T) {
	node := &semantic.Node{ID: "button", Handle: "button", Role: "button", Type: "button", Visible: true, InViewport: true, Enabled: true, Bounds: rect(0, 0, 100, 40), Clip: rect(0, 0, 100, 40), FocusOrder: 0}
	runtime := &fakeRuntime{tree: node}
	driver := NewDriver(runtime)
	results, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 7, Source: "mouse", X: 12.5, Y: 18.5, Button: "primary", TimeMS: 1}})
	if err != nil || len(results) != 1 {
		t.Fatalf("press error=%v results=%+v", err, results)
	}
	capture := driver.Router().Snapshot().PointerCapture
	if capture == nil || capture.PointerID != 7 || capture.Source != "mouse" || capture.Buttons != 1 || capture.Point.X != 13 || capture.Point.Y != 19 {
		t.Fatalf("capture metadata=%+v", capture)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "release", PointerID: 7, Source: "mouse", X: 12.5, Y: 18.5, Button: "primary", TimeMS: 2}}); err != nil {
		t.Fatal(err)
	}
	if capture := driver.Router().Snapshot().PointerCapture; capture != nil {
		t.Fatalf("capture survived release: %+v", capture)
	}
}

func TestDriverTopmostClipAwareHoverPressAndOffTargetConsumption(t *testing.T) {
	back := interactiveNode("back", "back-handle", "button", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), 0, 1)
	clipped := interactiveNode("clipped", "clipped-handle", "button", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 10, 10), 1, 5)
	front := interactiveNode("front", "front-handle", "button", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), 2, 6)
	driver := NewDriver(&fakeRuntime{tree: runtimeTree(back, clipped)})
	driver.Update(runtimeTree(back, clipped))
	result, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 20, Y: 20, TimeMS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result[0].TargetID != back.ID || !result[0].Consumed || driver.Router().Snapshot().HoveredIDs[0] != back.ID {
		t.Fatalf("clipped hit result=%+v snapshot=%+v", result[0], driver.Router().Snapshot())
	}
	driver.Update(runtimeTree(back, clipped, front))
	result, err = driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 20, Y: 20, TimeMS: 2}})
	if err != nil || result[0].TargetID != front.ID || !result[0].Consumed {
		t.Fatalf("topmost result=%+v err=%v", result, err)
	}
	result, err = driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 140, Y: 20, TimeMS: 3}})
	if err != nil || result[0].TargetID != "" || result[0].Consumed {
		t.Fatalf("off-target result=%+v err=%v", result, err)
	}
	if len(driver.Router().Snapshot().HoveredIDs) != 0 {
		t.Fatalf("off-target hover survived: %+v", driver.Router().Snapshot())
	}
	result, err = driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 2, Source: "touch", X: 20, Y: 20, TimeMS: 4}})
	if err != nil || result[0].Consumed || len(driver.Router().Snapshot().HoveredIDs) != 0 {
		t.Fatalf("touch move established hover/consumption: result=%+v snapshot=%+v err=%v", result, driver.Router().Snapshot(), err)
	}
}

func TestDriverRejectsMismatchedReleaseAndSecondPointerCannotSteerCapture(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree(interactiveNode("button", "button-handle", "button", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), 0, 1))}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "secondary", TimeMS: 2}}); err == nil {
		t.Fatal("mismatched release was accepted")
	}
	if capture := driver.Router().Snapshot().PointerCapture; capture == nil || capture.PointerID != 1 {
		t.Fatalf("mismatched release lost capture: %+v", capture)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "touch", X: 10, Y: 10, TimeMS: 2.5}}); err == nil {
		t.Fatal("mismatched pointer source was accepted")
	}
	results, err := driver.Dispatch([]Event{
		{Type: "pointer", Kind: "press", PointerID: 2, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 3},
		{Type: "pointer", Kind: "move", PointerID: 2, Source: "mouse", X: 160, Y: 10, TimeMS: 4},
		{Type: "pointer", Kind: "release", PointerID: 2, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, result := range results {
		if result.Consumed || result.TargetID != "" {
			t.Fatalf("second pointer event %d steered capture: %+v", index, result)
		}
	}
	if len(runtime.activations) != 0 {
		t.Fatalf("second pointer activated control: %d", len(runtime.activations))
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "cancel", PointerID: 1, Source: "mouse", Button: "none", TimeMS: 6}}); err != nil {
		t.Fatal(err)
	}
	if driver.Router().Snapshot().PointerCapture != nil {
		t.Fatal("cancel did not clear primary capture")
	}
	secondaryResults, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 3, Source: "mouse", X: 10, Y: 10, Button: "secondary", TimeMS: 7}, {Type: "pointer", Kind: "release", PointerID: 3, Source: "mouse", X: 10, Y: 10, Button: "secondary", TimeMS: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.activations) != 0 || driver.Router().Snapshot().PointerCapture != nil || secondaryResults[0].Consumed || secondaryResults[1].Consumed {
		t.Fatalf("secondary sequence established semantic capture: results=%+v activations=%d snapshot=%+v", secondaryResults, len(runtime.activations), driver.Router().Snapshot())
	}
}

func TestDriverUpdateAndCloseClearPointerOwnership(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree(interactiveNode("button", "button-handle", "button", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), 0, 1))}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 1}}); err != nil {
		t.Fatal(err)
	}
	driver.Update(nil)
	if snapshot := driver.Router().Snapshot(); snapshot.PointerCapture != nil || len(snapshot.PressedIDs) != 0 || snapshot.FocusedID != "" {
		t.Fatalf("update retained ownership: %+v", snapshot)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 2}}); err == nil {
		t.Fatal("release after update was accepted")
	}
	driver.Update(runtime.tree)
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 2, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: 3}}); err != nil {
		t.Fatal(err)
	}
	driver.Close()
	if snapshot := driver.Router().Snapshot(); snapshot.PointerCapture != nil || !snapshot.Inspecting {
		t.Fatalf("close retained ownership: %+v", snapshot)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 2, Source: "mouse", X: 10, Y: 10, TimeMS: 4}}); err == nil {
		t.Fatal("closed driver accepted input")
	}
}

func TestDriverSliderAndBothAxisScrollbarValues(t *testing.T) {
	minimum, maximum := 0.0, 100.0
	slider := interactiveNode("slider", "slider-handle", "slider", image.Rect(0, 0, 100, 20), image.Rect(0, 0, 220, 220), 0, 1)
	slider.Binding, slider.Orientation, slider.Min, slider.Max = "volume", "horizontal", &minimum, &maximum
	vertical := scrollbarNode("vertical-bar", "vertical-handle", "vertical", image.Rect(190, 0, 198, 100), image.Rect(0, 0, 220, 220), 2, 200, 100, image.Rect(190, 0, 198, 34))
	horizontal := scrollbarNode("horizontal-bar", "horizontal-handle", "horizontal", image.Rect(0, 190, 100, 198), image.Rect(0, 0, 220, 220), 3, 100, 100, image.Rect(0, 190, 34, 198))
	runtime := &fakeRuntime{tree: runtimeTree(slider, vertical, horizontal)}
	driver := NewDriver(runtime)
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 25, Y: 10, Button: "primary", TimeMS: 1}, {Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 75, Y: 10, TimeMS: 2}, {Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 75, Y: 10, Button: "primary", TimeMS: 3}}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.values) < 2 || runtime.values[len(runtime.values)-1].Value != 75.0 {
		t.Fatalf("slider values=%+v", runtime.values)
	}
	verticalSlider := interactiveNode("vertical-slider", "vertical-slider-handle", "slider", image.Rect(120, 0, 140, 100), image.Rect(0, 0, 220, 220), 1, 7)
	verticalSlider.Binding, verticalSlider.Orientation, verticalSlider.Min, verticalSlider.Max = "vertical-volume", "vertical", &minimum, &maximum
	verticalRuntime := &fakeRuntime{tree: runtimeTree(verticalSlider)}
	verticalDriver := NewDriver(verticalRuntime)
	if _, err := verticalDriver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 8, Source: "mouse", X: 130, Y: 75, Button: "primary", TimeMS: 1}, {Type: "pointer", Kind: "move", PointerID: 8, Source: "mouse", X: 130, Y: 25, TimeMS: 2}, {Type: "pointer", Kind: "release", PointerID: 8, Source: "mouse", X: 130, Y: 25, Button: "primary", TimeMS: 3}}); err != nil {
		t.Fatal(err)
	}
	if len(verticalRuntime.values) < 2 || verticalRuntime.values[0].Value != 25.0 || verticalRuntime.values[len(verticalRuntime.values)-1].Value != 75.0 {
		t.Fatalf("vertical slider values=%+v", verticalRuntime.values)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 5, Source: "mouse", X: 40, Y: 10, Button: "primary", TimeMS: 3.1}}); err != nil {
		t.Fatal(err)
	}
	valueCount := len(runtime.values)
	results, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 6, Source: "mouse", X: 90, Y: 10, TimeMS: 3.2}, {Type: "pointer", Kind: "leave", PointerID: 5, Source: "mouse", X: 40, Y: 10, TimeMS: 3.3}, {Type: "pointer", Kind: "release", PointerID: 5, Source: "mouse", X: 140, Y: 10, Button: "primary", TimeMS: 3.4}})
	if err != nil || len(runtime.values) != valueCount || results[0].Consumed || len(driver.Router().Snapshot().PressedIDs) != 0 {
		t.Fatalf("slider mismatched/leave changed value: results=%+v values=%+v snapshot=%+v err=%v", results, runtime.values, driver.Router().Snapshot(), err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 2, Source: "mouse", X: 194, Y: 10, Button: "primary", TimeMS: 4}, {Type: "pointer", Kind: "move", PointerID: 2, Source: "mouse", X: 194, Y: 50, TimeMS: 5}, {Type: "pointer", Kind: "release", PointerID: 2, Source: "mouse", X: 194, Y: 50, Button: "primary", TimeMS: 6}}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.scrolls) == 0 || runtime.scrolls[len(runtime.scrolls)-1].ID != vertical.ID || runtime.scrolls[len(runtime.scrolls)-1].Y <= 0 || runtime.scrolls[len(runtime.scrolls)-1].X != 0 {
		t.Fatalf("vertical scroll values=%+v", runtime.scrolls)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 3, Source: "mouse", X: 20, Y: 194, Button: "primary", TimeMS: 7}, {Type: "pointer", Kind: "move", PointerID: 3, Source: "mouse", X: 50, Y: 194, TimeMS: 8}, {Type: "pointer", Kind: "release", PointerID: 3, Source: "mouse", X: 50, Y: 194, Button: "primary", TimeMS: 9}}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.scrolls) < 2 || runtime.scrolls[len(runtime.scrolls)-1].ID != horizontal.ID || runtime.scrolls[len(runtime.scrolls)-1].X <= 0 || runtime.scrolls[len(runtime.scrolls)-1].Y != 0 {
		t.Fatalf("horizontal scroll values=%+v", runtime.scrolls)
	}
	if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 4, Source: "mouse", X: 194, Y: 90, Button: "primary", TimeMS: 10}, {Type: "pointer", Kind: "release", PointerID: 4, Source: "mouse", X: 194, Y: 90, Button: "primary", TimeMS: 11}}); err != nil {
		t.Fatal(err)
	}
	if runtime.scrolls[len(runtime.scrolls)-1].ID != vertical.ID || runtime.scrolls[len(runtime.scrolls)-1].Y <= 0 {
		t.Fatalf("vertical track page missing: %+v", runtime.scrolls)
	}
}

func TestDriverFocusTraversalRovingAndActivationKeys(t *testing.T) {
	button := interactiveNode("button", "button-handle", "button", image.Rect(0, 0, 40, 30), image.Rect(0, 0, 300, 300), 0, 1)
	checkbox := interactiveNode("checkbox", "checkbox-handle", "checkbox", image.Rect(50, 0, 90, 30), image.Rect(0, 0, 300, 300), 1, 2)
	radioA := interactiveNode("radio-a", "radio-a-handle", "radio", image.Rect(0, 40, 40, 70), image.Rect(0, 0, 300, 300), 2, 3)
	radioA.Group = "radio-group"
	radioB := interactiveNode("radio-b", "radio-b-handle", "radio", image.Rect(50, 40, 90, 70), image.Rect(0, 0, 300, 300), -1, 4)
	radioB.Group = "radio-group"
	tabA := interactiveNode("tab-a", "tab-a-handle", "tab", image.Rect(0, 80, 40, 110), image.Rect(0, 0, 300, 300), 3, 5)
	tabA.Group = "tabs"
	tabB := interactiveNode("tab-b", "tab-b-handle", "tab", image.Rect(50, 80, 90, 110), image.Rect(0, 0, 300, 300), -1, 6)
	tabB.Group = "tabs"
	hidden := interactiveNode("hidden", "hidden-handle", "button", image.Rect(0, 120, 40, 150), image.Rect(0, 0, 300, 300), 4, 7)
	hidden.Visible = false
	disabled := interactiveNode("disabled", "disabled-handle", "button", image.Rect(50, 120, 90, 150), image.Rect(0, 0, 300, 300), 5, 8)
	disabled.Enabled = false
	runtime := &fakeRuntime{tree: runtimeTree(button, checkbox, radioA, radioB, tabA, tabB, hidden, disabled)}
	driver := NewDriver(runtime)
	result, err := driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Tab", TimeMS: 1}, {Type: "key", Kind: "down", Name: "Tab", TimeMS: 2}, {Type: "key", Kind: "down", Name: "Tab", TimeMS: 3}})
	if err != nil || len(result) != 3 || result[0].TargetID != button.ID || result[1].TargetID != checkbox.ID || result[2].TargetID != radioA.ID {
		t.Fatalf("tab traversal=%+v err=%v", result, err)
	}
	filtered := NewDriver(runtime)
	filteredResults, err := filtered.Dispatch([]Event{
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 1},
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 2},
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 3},
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 4},
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 5},
	})
	if err != nil || filteredResults[0].TargetID != button.ID || filteredResults[1].TargetID != checkbox.ID || filteredResults[2].TargetID != radioA.ID || filteredResults[3].TargetID != tabA.ID || filteredResults[4].TargetID != button.ID {
		t.Fatalf("hidden/disabled tab filtering=%+v err=%v", filteredResults, err)
	}
	result, err = driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "ArrowRight", TimeMS: 4}, {Type: "key", Kind: "down", Name: "Home", TimeMS: 5}, {Type: "key", Kind: "down", Name: "End", TimeMS: 6}})
	if err != nil || driver.Router().Snapshot().FocusedID != radioB.ID || len(runtime.activations) < 3 {
		t.Fatalf("radio roving=%+v snapshot=%+v activations=%d err=%v", result, driver.Router().Snapshot(), len(runtime.activations), err)
	}
	result, err = driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Tab", Modifiers: []string{"shift"}, TimeMS: 7}})
	if err != nil || result[0].TargetID != checkbox.ID {
		t.Fatalf("shift-tab target=%+v err=%v", result, err)
	}
	result, err = driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Tab", TimeMS: 7.1}, {Type: "key", Kind: "down", Name: "Tab", TimeMS: 7.2}})
	if err != nil || result[1].TargetID != tabA.ID {
		t.Fatalf("tab-list focus target=%+v err=%v", result, err)
	}
	result, err = driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "ArrowRight", TimeMS: 7.3}, {Type: "key", Kind: "down", Name: "Home", TimeMS: 7.4}})
	if err != nil || driver.Router().Snapshot().FocusedID != tabA.ID || len(runtime.activations) < 5 {
		t.Fatalf("tab roving=%+v snapshot=%+v activations=%d err=%v", result, driver.Router().Snapshot(), len(runtime.activations), err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Tab", Modifiers: []string{"shift"}, TimeMS: 7.5}, {Type: "key", Kind: "down", Name: "Tab", Modifiers: []string{"shift"}, TimeMS: 7.6}}); err != nil {
		t.Fatal(err)
	}
	result, err = driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Enter", TimeMS: 8}})
	if err != nil || result[0].Consumed || len(runtime.activations) < 3 {
		t.Fatalf("checkbox enter activation=%+v err=%v", result, err)
	}
	if _, err := driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Space", TimeMS: 9}, {Type: "key", Kind: "up", Name: "Space", TimeMS: 10}}); err != nil {
		t.Fatal(err)
	}
	activationsAfterSpace := len(runtime.activations)
	if activationsAfterSpace == 0 {
		t.Fatal("space did not activate checkbox")
	}
	if _, err := driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Space", TimeMS: 11}, {Type: "key", Kind: "down", Name: "Escape", TimeMS: 12}, {Type: "key", Kind: "up", Name: "Space", TimeMS: 13}}); err != nil {
		t.Fatal(err)
	}
	if snapshot := driver.Router().Snapshot(); snapshot.KeyboardPress != nil || len(snapshot.PressedIDs) != 0 {
		t.Fatalf("escape did not cancel keyboard press: %+v", snapshot)
	}
	if len(runtime.activations) != activationsAfterSpace {
		t.Fatalf("escape-cancelled space activated unexpectedly: %d -> %d", activationsAfterSpace, len(runtime.activations))
	}
}

func TestDriverTransientEventsAndRepeatedCyclesStayBounded(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree(interactiveNode("button", "button-handle", "button", image.Rect(0, 0, 80, 30), image.Rect(0, 0, 100, 100), 0, 1))}
	driver := NewDriver(runtime)
	for index := 0; index < 100; index++ {
		if _, err := driver.Dispatch([]Event{{Type: "key", Kind: "down", Name: "Tab", TimeMS: float64(index)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 10, Y: 10, TimeMS: float64(index)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "press", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: float64(index) + .1}, {Type: "pointer", Kind: "release", PointerID: 1, Source: "mouse", X: 10, Y: 10, Button: "primary", TimeMS: float64(index) + .2}}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := driver.Router().Snapshot()
	if snapshot.PointerCapture != nil || snapshot.QueueSizes.ValueChanges != 0 || snapshot.QueueSizes.ScrollChanges != 0 || len(driver.pointers) != 0 {
		t.Fatalf("bounded cycle state=%+v pointers=%d", snapshot, len(driver.pointers))
	}
	if len(runtime.published) != 400 {
		t.Fatalf("expected one bounded publication per event, got %d", len(runtime.published))
	}
}

func TestDriverTransientPublicationKeepsGeometryRevisionStable(t *testing.T) {
	runtime := &fakeRuntime{tree: runtimeTree(interactiveNode("button", "button-handle", "button", image.Rect(0, 0, 80, 30), image.Rect(0, 0, 100, 100), 0, 1))}
	revisions := RevisionSnapshot{RuntimeRevision: 3, FrameRevision: 8, GeometryRevision: 11, PublishedRuntimeRevision: 3, PublishedGeometryRevision: 11, AutomationInputRevision: 4}
	driver := NewDriverWithSnapshot(runtime, func() RevisionSnapshot { return revisions })
	driver.Update(runtime.tree)
	results, err := driver.Dispatch([]Event{{Type: "pointer", Kind: "move", PointerID: 1, Source: "mouse", X: 10, Y: 10, TimeMS: 1}})
	if err != nil || len(results) != 1 {
		t.Fatalf("transient dispatch error=%v results=%+v", err, results)
	}
	if results[0].GeometryRevision != 11 || results[0].PublishedGeometryRevision != 11 || len(runtime.transients) != 1 || len(runtime.published) != 1 {
		t.Fatalf("transient publication changed geometry or was not published: result=%+v transients=%d publications=%d", results[0], len(runtime.transients), len(runtime.published))
	}
}

func TestDriverSliderKeyboardUsesPortableStepPageAndBoundaryKeys(t *testing.T) {
	minimum, maximum, step := 0.0, 100.0, 5.0
	slider := interactiveNode("slider", "slider-handle", "slider", image.Rect(0, 0, 100, 20), image.Rect(0, 0, 120, 120), 0, 1)
	slider.Binding, slider.Orientation, slider.Min, slider.Max, slider.Step = "volume", "horizontal", &minimum, &maximum, &step
	runtime := &fakeRuntime{tree: runtimeTree(slider)}
	driver := NewDriver(runtime)
	results, err := driver.Dispatch([]Event{
		{Type: "key", Kind: "down", Name: "Tab", TimeMS: 1},
		{Type: "key", Kind: "down", Name: "ArrowRight", TimeMS: 2},
		{Type: "key", Kind: "down", Name: "PageUp", TimeMS: 3},
		{Type: "key", Kind: "down", Name: "End", TimeMS: 4},
		{Type: "key", Kind: "down", Name: "Home", TimeMS: 5},
		{Type: "key", Kind: "down", Name: "ArrowDown", TimeMS: 6},
	})
	if err != nil || len(results) != 6 {
		t.Fatalf("slider keyboard error=%v results=%+v", err, results)
	}
	for index := 0; index < 6; index++ {
		if !results[index].Consumed {
			t.Fatalf("slider key %d was not consumed: %+v", index, results[index])
		}
	}
	if len(runtime.activations) != 5 || runtime.activations[0].Actions[0].Action != "increment" || runtime.activations[1].Actions[0].By != 50.0 || runtime.activations[2].Actions[0].Value != 100.0 || runtime.activations[3].Actions[0].Value != 0.0 || runtime.activations[4].Actions[0].Action != "decrement" {
		t.Fatalf("slider keyboard actions=%+v", runtime.activations)
	}
}

func rect(x, y, w, h int) *semantic.Rect { return &semantic.Rect{X: x, Y: y, Width: w, Height: h} }

func runtimeTree(children ...*semantic.Node) *semantic.Node {
	return &semantic.Node{Type: "_viewport", Visible: true, InViewport: true, Enabled: true, Children: children}
}

type fakeRuntime struct {
	tree         *semantic.Node
	activations  []interaction.Activation
	values       []interaction.ControlValueChange
	scrolls      []interaction.ScrollChange
	transients   []interaction.Transient
	published    []interaction.RouterSnapshot
	treeCalls    int
	transient    interaction.Transient
	scrollScale  float64
	scrollInputs []scrollinput.Event
	traces       []TraceEntry
}

func (f *fakeRuntime) RouteScroll(event scrollinput.Event) (scrollinput.Outcome, error) {
	f.scrollInputs = append(f.scrollInputs, event)
	if f.scrollScale <= 0 {
		f.scrollScale = 1
	}
	outcome, err := scrollinput.Normalize(event, f.scrollScale)
	if err != nil {
		return scrollinput.Outcome{}, err
	}
	for _, modifier := range event.Modifiers {
		if modifier == "command" || modifier == "control" {
			outcome.NoFrameReason = "command_modified_headless_scroll_blocked"
			outcome.ResidualX = outcome.LogicalDeltaX
			outcome.ResidualY = outcome.LogicalDeltaY
			outcome.Axes = []scrollinput.AxisResult{{Axis: "x", Residual: outcome.ResidualX}, {Axis: "y", Residual: outcome.ResidualY}}
			return outcome, nil
		}
	}
	outcome.OwnerID = "canvas"
	outcome.ConsumedX = outcome.LogicalDeltaX
	outcome.ConsumedY = outcome.LogicalDeltaY
	outcome.ResidualX, outcome.ResidualY = 0, 0
	outcome.Changed = outcome.LogicalDeltaX != 0 || outcome.LogicalDeltaY != 0
	if !outcome.Changed {
		outcome.NoFrameReason = "phase_only"
		outcome.Axes = []scrollinput.AxisResult{{Axis: "x"}, {Axis: "y"}}
	}
	return outcome, nil
}
func (f *fakeRuntime) RecordEventTrace(entry TraceEntry) { f.traces = append(f.traces, entry) }

func (f *fakeRuntime) CurrentRuntimeTree() (*semantic.Node, error) { f.treeCalls++; return f.tree, nil }
func (f *fakeRuntime) RuntimeTree() (*semantic.Node, error)        { return f.tree, nil }
func (f *fakeRuntime) Activate(value interaction.Activation) error {
	f.activations = append(f.activations, value)
	if value.OpenSelect != "" {
		f.transient.OpenSelect = value.OpenSelect
		f.transient.ActiveOption = value.ActiveOption
	}
	if value.CloseSelect {
		f.transient.OpenSelect = ""
		f.transient.ActiveOption = ""
	}
	return nil
}
func (f *fakeRuntime) SetControlValue(id string, value any) (any, error) {
	f.values = append(f.values, interaction.ControlValueChange{ID: id, Value: value})
	return value, nil
}
func (f *fakeRuntime) ScrollSemanticID(id, mode string, x, y int) error {
	f.scrolls = append(f.scrolls, interaction.ScrollChange{ID: id, Mode: mode, X: x, Y: y})
	return nil
}
func (f *fakeRuntime) SetTransient(value interaction.Transient) {
	f.transient = value
	f.transients = append(f.transients, value)
}
func (f *fakeRuntime) PublishRouterSnapshot(value interaction.RouterSnapshot) {
	f.published = append(f.published, value)
}
func (f *fakeRuntime) AutomationSnapshot() RevisionSnapshot    { return RevisionSnapshot{} }
func (f *fakeRuntime) CurrentTransient() interaction.Transient { return f.transient }

var _ = image.Point{}

func interactiveNode(id, handle, role string, bounds, clip image.Rectangle, focus, paint int) *semantic.Node {
	return &semantic.Node{ID: id, Handle: handle, Type: role, Role: role, Visible: true, InViewport: true, Enabled: true, Bounds: rect(bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy()), Clip: rect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()), FocusOrder: focus, PaintOrder: paint, Actions: []document.Action{{Action: "set", State: "value", Value: id}}}
}

func scrollbarNode(id, handle, axisName string, bounds, clip image.Rectangle, focus, maximum, viewport int, thumb image.Rectangle) *semantic.Node {
	content := viewport + maximum
	axis := &semantic.Node{ID: id, Handle: handle, Type: "scrollbar", Role: "scrollbar", Orientation: axisName, Visible: true, InViewport: true, Enabled: true, Bounds: rect(bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy()), Clip: rect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()), FocusOrder: focus, PaintOrder: focus, Max: floatPointer(float64(maximum)), ViewportSize: rect(viewport, viewport, viewport, viewport), ContentSize: rect(content, content, content, content)}
	axis.Children = []*semantic.Node{{ID: id + "/track", Handle: handle + "/track", Type: "scrollbar_track", Group: handle, Visible: true, InViewport: true, Enabled: true, Bounds: rect(bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy()), Clip: rect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()), PaintOrder: focus + 1}, {ID: id + "/thumb", Handle: handle + "/thumb", Type: "scrollbar_thumb", Group: handle, Visible: true, InViewport: true, Enabled: true, Bounds: rect(thumb.Min.X, thumb.Min.Y, thumb.Dx(), thumb.Dy()), Clip: rect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()), PaintOrder: focus + 2}}
	return axis
}

func floatPointer(value float64) *float64 { return &value }
