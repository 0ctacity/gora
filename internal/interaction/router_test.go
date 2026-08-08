package interaction

import (
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/semantic"
)

func TestRouterCapturesPointerAndActivatesOnlyOnReleaseInside(t *testing.T) {
	router := NewRouter()
	router.Update(runtimeTree(
		semanticButton("button", "screen:main", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 80, 40), false,
			document.Action{Action: "toggle", State: "on"},
		),
	))
	if !router.Press(7, image.Pt(20, 20)) || router.Transient().Pressed != "button" || router.Transient().Focused != "button" {
		t.Fatalf("press state = %+v", router.Transient())
	}
	if activation, ok := router.Release(7, image.Pt(90, 20)); ok || activation.Scope != "" {
		t.Fatalf("outside release activated: %+v", activation)
	}
	router.Press(8, image.Pt(20, 20))
	activation, ok := router.Release(8, image.Pt(30, 20))
	if !ok || activation.Scope != "screen:main" || len(activation.Actions) != 1 {
		t.Fatalf("inside release = %+v, %v", activation, ok)
	}
}

func TestRouterUsesTopmostRegionAndKeyboardTraversal(t *testing.T) {
	router := NewRouter()
	router.Update(runtimeTree(
		semanticButton("first", "", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), false),
		semanticButton("disabled", "", image.Rect(0, 50, 100, 90), image.Rect(0, 50, 100, 90), true),
		semanticButton("top", "", image.Rect(0, 0, 100, 40), image.Rect(0, 0, 100, 40), false),
	))
	router.Move(image.Pt(20, 20), false)
	if router.Transient().Hovered != "top" {
		t.Fatalf("hovered = %q", router.Transient().Hovered)
	}
	if got := router.FocusNext(false); got != "first" {
		t.Fatalf("first focus = %q", got)
	}
	if got := router.FocusNext(false); got != "top" {
		t.Fatalf("second focus = %q", got)
	}
	if got := router.FocusNext(true); got != "first" {
		t.Fatalf("reverse focus = %q", got)
	}
}

func TestRouterCompositeArrowTraversalRemainsSourceOrderedWhenPaintRanksDiffer(t *testing.T) {
	radioA := &semantic.Node{Handle: "radio-a", Role: "radio", Group: "group", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 0, 30, 30)), Clip: semanticRect(image.Rect(0, 0, 100, 40)), FocusOrder: 0, PaintOrder: 2}
	radioB := &semantic.Node{Handle: "radio-b", Role: "radio", Group: "group", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(30, 0, 60, 30)), Clip: semanticRect(image.Rect(0, 0, 100, 40)), FocusOrder: -1, PaintOrder: 1}
	radioC := &semantic.Node{Handle: "radio-c", Role: "radio", Group: "group", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(60, 0, 90, 30)), Clip: semanticRect(image.Rect(0, 0, 100, 40)), FocusOrder: -1, PaintOrder: 3}
	router := NewRouter()
	router.Update(runtimeTree(radioA, radioB, radioC))
	router.SyncTransient(Transient{Focused: radioA.Handle})
	if _, ok := router.KeyDown("ArrowRight"); !ok || router.Transient().Focused != radioB.Handle {
		t.Fatalf("composite arrow followed paint order: transient=%+v activated=%v", router.Transient(), ok)
	}
}

func TestRouterSpacePressEscapeAndInspectOwnership(t *testing.T) {
	router := NewRouter()
	router.Update(runtimeTree(semanticButton("button", "scope", image.Rect(0, 0, 10, 10), image.Rect(0, 0, 10, 10), false)))
	router.FocusNext(false)
	if _, ok := router.KeyDown("Space"); ok || router.Transient().Pressed != "button" {
		t.Fatalf("space down = %+v", router.Transient())
	}
	router.KeyDown("Escape")
	if router.Transient().Pressed != "" {
		t.Fatal("escape did not cancel keyboard press")
	}
	router.SetInspecting(true)
	if router.Transient() != (Transient{}) || router.Press(1, image.Pt(5, 5)) {
		t.Fatalf("inspect retained document interaction: %+v", router.Transient())
	}
}

func TestRouterSnapshotClearsOwnershipOnInspectCancelAndTreeInvalidation(t *testing.T) {
	button := semanticButton("button-handle", "scope", image.Rect(0, 0, 40, 30), image.Rect(0, 0, 200, 200), false)
	button.ID = "button-id"
	bar := &semantic.Node{Type: "scrollbar", Role: "scrollbar", Handle: "bar-handle", ID: "bar-id", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(50, 0, 58, 100)), Clip: semanticRect(image.Rect(0, 0, 200, 200))}
	slider := &semantic.Node{Type: "slider", Role: "slider", Handle: "slider-handle", ID: "slider-id", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 40, 100, 70)), Clip: semanticRect(image.Rect(0, 0, 200, 200))}
	selectNode := &semantic.Node{Type: "select", Role: "combobox", Handle: "select-handle", ID: "select-id", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 80, 100, 110)), Clip: semanticRect(image.Rect(0, 0, 200, 200))}
	option := &semantic.Node{Type: "option", Role: "option", Handle: "option-handle", ID: "option-id", Group: selectNode.Handle, Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 115, 100, 145)), Clip: semanticRect(image.Rect(0, 0, 200, 200))}
	tree := runtimeTree(button, bar, slider, selectNode, option)
	router := NewRouter()
	router.Update(tree)
	router.SetPointerMetadata("mouse", 1, image.Pt(12, 12))
	if !router.Press(7, image.Pt(12, 12)) {
		t.Fatal("button press was not captured")
	}
	router.transient = Transient{Hovered: button.Handle, Pressed: button.Handle, Focused: button.Handle, OpenSelect: selectNode.Handle, ActiveOption: option.Handle}
	router.keyboardPress = button.Handle
	router.keyboardKey = "Space"
	router.scrollCapture = &scrollbarCapture{axis: bar}
	router.valueChange = &ControlValueChange{ID: slider.ID, Value: 40}
	router.scrollChange = &ScrollChange{ID: bar.ID, Mode: "by", Y: 10}

	router.SetInspecting(true)
	if snapshot := router.Snapshot(); snapshot.FocusedID != "" || len(snapshot.HoveredIDs) != 0 || len(snapshot.PressedIDs) != 0 || snapshot.OpenSelectID != "" || len(snapshot.ActiveIDs) != 0 || snapshot.PointerCapture != nil || snapshot.KeyboardPress != nil || snapshot.ScrollbarGestureOwner != "" || snapshot.SliderGestureOwner != "" || snapshot.QueueSizes.ValueChanges != 0 || snapshot.QueueSizes.ScrollChanges != 0 {
		t.Fatalf("inspect retained document ownership: %+v", snapshot)
	}
	if _, ok := router.TakeValueChange(); ok {
		t.Fatal("inspect retained stale value change")
	}
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("inspect retained stale scroll change")
	}

	router.SetInspecting(false)
	router.Update(tree)
	router.transient = Transient{Pressed: button.Handle, Focused: button.Handle}
	router.captureID = 9
	router.captureHandle = button.Handle
	router.keyboardPress = button.Handle
	router.keyboardKey = "Enter"
	router.scrollCapture = &scrollbarCapture{axis: bar}
	router.valueChange = &ControlValueChange{ID: slider.ID, Value: 55}
	router.scrollChange = &ScrollChange{ID: bar.ID, Mode: "by", Y: 4}
	router.Cancel(9)
	if snapshot := router.Snapshot(); snapshot.PointerCapture != nil || snapshot.KeyboardPress != nil || snapshot.ScrollbarGestureOwner != "" || snapshot.SliderGestureOwner != "" || len(snapshot.PressedIDs) != 0 || snapshot.QueueSizes.ValueChanges != 0 || snapshot.QueueSizes.ScrollChanges != 0 {
		t.Fatalf("cancel retained document ownership: %+v", snapshot)
	}
	if _, ok := router.TakeValueChange(); ok {
		t.Fatal("cancel retained stale value change")
	}
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("cancel retained stale scroll change")
	}

	router.Update(runtimeTree())
	if snapshot := router.Snapshot(); snapshot.PointerCapture != nil || snapshot.KeyboardPress != nil || snapshot.ScrollbarGestureOwner != "" || snapshot.SliderGestureOwner != "" || len(snapshot.PressedIDs) != 0 || snapshot.QueueSizes.ValueChanges != 0 || snapshot.QueueSizes.ScrollChanges != 0 {
		t.Fatalf("tree invalidation retained ownership: %+v", snapshot)
	}
}

func TestRouterUsesCompositeControlFocusAndCheckboxSpaceActivation(t *testing.T) {
	tree := runtimeTree(
		&semantic.Node{Handle: "toggle", Role: "checkbox", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 0, 40, 40)), Clip: semanticRect(image.Rect(0, 0, 200, 200)), FocusOrder: 0, Actions: []document.Action{{Action: "toggle", State: "enabled"}}},
		&semantic.Node{Handle: "unselected", Role: "radio", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(50, 0, 90, 40)), Clip: semanticRect(image.Rect(0, 0, 200, 200)), FocusOrder: -1},
		&semantic.Node{Handle: "selected", Role: "radio", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(100, 0, 140, 40)), Clip: semanticRect(image.Rect(0, 0, 200, 200)), FocusOrder: 1, Actions: []document.Action{{Action: "set", State: "plan", Value: "annual"}}},
	)
	router := NewRouter()
	router.Update(tree)
	if got := router.FocusNext(false); got != "toggle" {
		t.Fatalf("first focus = %q", got)
	}
	if _, activated := router.KeyDown("Enter"); activated {
		t.Fatal("Enter activated checkbox")
	}
	if _, activated := router.KeyDown("Space"); activated {
		t.Fatal("Space must activate on key up")
	}
	activation, activated := router.KeyUp("Space")
	if !activated || len(activation.Actions) != 1 || activation.Actions[0].Action != "toggle" {
		t.Fatalf("space activation = %+v, %v", activation, activated)
	}
	if got := router.FocusNext(false); got != "selected" {
		t.Fatalf("second focus = %q", got)
	}
}

func TestRouterRovesRadioFocusAndSelectActiveOption(t *testing.T) {
	radioA := &semantic.Node{Handle: "radio-a", Role: "radio", Group: "group", Scope: "scope", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 0, 40, 40)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: 0, Actions: []document.Action{{Action: "set", State: "plan", Value: "a"}}}
	radioB := &semantic.Node{Handle: "radio-b", Role: "radio", Group: "group", Scope: "scope", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(50, 0, 90, 40)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: -1, Actions: []document.Action{{Action: "set", State: "plan", Value: "b"}}}
	selectNode := &semantic.Node{Handle: "select", Role: "combobox", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 60, 100, 100)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: 1}
	optionA := &semantic.Node{Handle: "option-a", Role: "option", Group: "select", Scope: "scope", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 105, 100, 135)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: -1, Actions: []document.Action{{Action: "set", State: "team", Value: "a"}}}
	optionB := &semantic.Node{Handle: "option-b", Role: "option", Group: "select", Scope: "scope", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 135, 100, 165)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: -1, Actions: []document.Action{{Action: "set", State: "team", Value: "b"}}}
	router := NewRouter()
	router.Update(runtimeTree(radioA, radioB, selectNode, optionA, optionB))
	router.FocusNext(false)
	activation, ok := router.KeyDown("ArrowRight")
	if !ok || router.Transient().Focused != "radio-b" || activation.Actions[0].Value != "b" {
		t.Fatalf("radio arrow = %+v transient=%+v", activation, router.Transient())
	}
	router.FocusNext(false)
	activation, ok = router.KeyDown("Enter")
	if !ok || activation.OpenSelect != "select" || router.Transient().OpenSelect != "select" || router.Transient().ActiveOption != "option-a" {
		t.Fatalf("select open = %+v transient=%+v", activation, router.Transient())
	}
	if _, ok := router.KeyDown("ArrowDown"); ok || router.Transient().ActiveOption != "option-b" {
		t.Fatalf("select arrow transient=%+v", router.Transient())
	}
	activation, ok = router.KeyDown("Enter")
	if !ok || !activation.CloseSelect || activation.Actions[0].Value != "b" {
		t.Fatalf("select commit = %+v", activation)
	}
}

func TestRouterSliderDragProducesSemanticValueChanges(t *testing.T) {
	minimum, maximum, step := 0.0, 100.0, 5.0
	slider := &semantic.Node{ID: "slider-id", Handle: "slider", Role: "slider", Scope: "scope", Binding: "volume", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 0, 200, 40)), Clip: semanticRect(image.Rect(0, 0, 300, 200)), FocusOrder: 0, Min: &minimum, Max: &maximum, Step: &step, Orientation: "horizontal"}
	router := NewRouter()
	router.Update(runtimeTree(slider))
	if !router.Press(3, image.Pt(50, 20)) {
		t.Fatal("slider press was not captured")
	}
	change, ok := router.TakeValueChange()
	if !ok || change.ID != "slider-id" || change.Value != 25.0 {
		t.Fatalf("press change = %+v, %v", change, ok)
	}
	router.Move(image.Pt(150, 20), false)
	change, ok = router.TakeValueChange()
	if !ok || change.Value != 75.0 {
		t.Fatalf("drag change = %+v, %v", change, ok)
	}
}

func TestRouterScrollbarThumbDragUsesAbsoluteGrabOffset(t *testing.T) {
	maximum := 100.0
	axis := &semantic.Node{
		ID: "scrollbar-v", Handle: "scrollbar-v", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical",
		Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(90, 2, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)),
		Value: 0, Max: &maximum, ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: 0, PaintOrder: 1,
	}
	axis.Children = []*semantic.Node{{
		ID: "scrollbar-v/thumb", Handle: "scrollbar-v/thumb", Type: "scrollbar_thumb", Group: axis.Handle,
		Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(90, 2, 98, 34)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 3,
	}}
	router := NewRouter()
	router.Update(runtimeTree(axis))
	if !router.Press(7, image.Pt(94, 20)) {
		t.Fatal("thumb press was not captured")
	}
	router.Move(image.Pt(94, 52), false)
	change, ok := router.TakeScrollChange()
	if !ok || change.ID != axis.ID || change.Mode != "to" || change.X != 0 || change.Y != 50 {
		t.Fatalf("midpoint drag change = %+v, %v", change, ok)
	}
	router.MovePointer(7, image.Pt(94, 84), false)
	change, ok = router.TakeScrollChange()
	if !ok || change.Y != 100 {
		t.Fatalf("end drag change = %+v, %v", change, ok)
	}
	router.MovePointer(7, image.Pt(-1, -1), false)
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("pointer leave snapped the captured scrollbar")
	}
	if _, ok := router.Release(7, image.Pt(94, 200)); ok || router.ScrollbarPointerOwned() {
		t.Fatal("scrollbar drag unexpectedly activated on release")
	}
	change, ok = router.TakeScrollChange()
	if !ok || change.Y != 100 {
		t.Fatalf("release final position = %+v, %v", change, ok)
	}
	router.MovePointer(7, image.Pt(94, 60), false)
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("move after release changed scrollbar")
	}
}

func TestRouterHorizontalScrollbarThumbDragUsesAbsoluteGrabOffset(t *testing.T) {
	maximum := 120.0
	axis := &semantic.Node{
		ID: "scrollbar-h", Handle: "scrollbar-h", Type: "scrollbar", Role: "scrollbar", Orientation: "horizontal",
		Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(2, 90, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)),
		Max: &maximum, ViewportSize: &semantic.Rect{Width: 100}, ContentSize: &semantic.Rect{Width: 300}, FocusOrder: 0, PaintOrder: 1,
	}
	axis.Children = []*semantic.Node{{
		ID: "scrollbar-h/thumb", Handle: "scrollbar-h/thumb", Type: "scrollbar_thumb", Group: axis.Handle,
		Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(2, 90, 34, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 2,
	}}
	router := NewRouter()
	router.Update(runtimeTree(axis))
	if !router.Press(1, image.Pt(12, 94)) {
		t.Fatal("horizontal thumb press was not captured")
	}
	router.MovePointer(1, image.Pt(44, 94), false)
	change, ok := router.TakeScrollChange()
	if !ok || change.ID != axis.ID || change.X != 60 || change.Y != 0 {
		t.Fatalf("horizontal midpoint drag = %+v, %v", change, ok)
	}
	router.MovePointer(1, image.Pt(12, 94), false)
	change, ok = router.TakeScrollChange()
	if !ok || change.X != 0 {
		t.Fatalf("horizontal start drag = %+v, %v", change, ok)
	}
	router.MovePointer(1, image.Pt(76, 94), false)
	change, ok = router.TakeScrollChange()
	if !ok || change.X != 120 {
		t.Fatalf("horizontal end drag = %+v, %v", change, ok)
	}
	if _, ok := router.Release(1, image.Pt(76, 94)); ok || router.ScrollbarPointerOwned() {
		t.Fatal("horizontal release retained capture or activated")
	}
}

func TestRouterScrollbarCaptureCyclesRemainBounded(t *testing.T) {
	maximum := 100.0
	axis := &semantic.Node{ID: "axis", Handle: "axis", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: true,
		Bounds: semanticRect(image.Rect(90, 2, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), Max: &maximum,
		ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: 0, PaintOrder: 1}
	axis.Children = []*semantic.Node{{ID: "axis/thumb", Handle: "axis/thumb", Type: "scrollbar_thumb", Group: axis.Handle, Visible: true, Enabled: true,
		Bounds: semanticRect(image.Rect(90, 2, 98, 34)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 2}}
	router := NewRouter()
	router.Update(runtimeTree(axis))
	for cycle := 0; cycle < 100; cycle++ {
		if !router.Press(cycle+1, image.Pt(94, 12)) {
			t.Fatalf("cycle %d thumb press was not captured", cycle)
		}
		router.MovePointer(cycle+1, image.Pt(94, 60), false)
		if _, ok := router.TakeScrollChange(); !ok {
			t.Fatalf("cycle %d did not produce a drag change", cycle)
		}
		if _, ok := router.Release(cycle+1, image.Pt(94, 60)); ok || router.ScrollbarPointerOwned() {
			t.Fatalf("cycle %d retained capture after release", cycle)
		}
		if _, ok := router.TakeScrollChange(); !ok {
			t.Fatalf("cycle %d did not retain one final release change", cycle)
		}
		if _, ok := router.TakeScrollChange(); ok {
			t.Fatalf("cycle %d retained more than one queued change", cycle)
		}
	}
}

func TestRouterScrollbarHitUsesPaintOrderAndClips(t *testing.T) {
	maxA, maxB := 100.0, 100.0
	axisA := scrollbarTestAxis("a", 1, &maxA, image.Rect(90, 2, 98, 98), image.Rect(90, 2, 98, 34), image.Rect(0, 0, 100, 100))
	axisB := scrollbarTestAxis("b", 20, &maxB, image.Rect(90, 2, 98, 98), image.Rect(90, 2, 98, 34), image.Rect(0, 0, 100, 100))
	router := NewRouter()
	router.Update(runtimeTree(axisA, axisB))
	if !router.Press(1, image.Pt(94, 18)) {
		t.Fatal("later-painted scrollbar was not captured")
	}
	router.MovePointer(1, image.Pt(94, 50), false)
	change, ok := router.TakeScrollChange()
	if !ok || change.ID != axisB.ID {
		t.Fatalf("later-painted scrollbar change = %+v, %v", change, ok)
	}
	router.Cancel(1)
	axisB.Clip = semanticRect(image.Rect(0, 0, 10, 10))
	for _, child := range axisB.Children {
		child.Clip = semanticRect(image.Rect(0, 0, 10, 10))
	}
	router.Update(runtimeTree(axisA, axisB))
	if !router.Press(2, image.Pt(94, 18)) {
		t.Fatal("clipped-behind scrollbar did not fall through to visible sibling")
	}
	router.MovePointer(2, image.Pt(94, 50), false)
	change, ok = router.TakeScrollChange()
	if !ok || change.ID != axisA.ID {
		t.Fatalf("clip-excluded scrollbar change = %+v, %v", change, ok)
	}
}

func TestRouterScrollbarTrackPagesAndIgnoresCornerOrSecondPointer(t *testing.T) {
	maximum := 200.0
	axis := &semantic.Node{
		ID: "scrollbar-h", Handle: "scrollbar-h", Type: "scrollbar", Role: "scrollbar", Orientation: "horizontal",
		Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(2, 90, 90, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)),
		Value: 0, Max: &maximum, ViewportSize: &semantic.Rect{Width: 100}, ContentSize: &semantic.Rect{Width: 300}, FocusOrder: 0, PaintOrder: 1,
	}
	axis.Children = []*semantic.Node{
		{ID: "scrollbar-h/track", Handle: "scrollbar-h/track", Type: "scrollbar_track", Group: axis.Handle, Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(2, 90, 90, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 2},
		{ID: "scrollbar-h/thumb", Handle: "scrollbar-h/thumb", Type: "scrollbar_thumb", Group: axis.Handle, Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(34, 90, 66, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 3},
		{ID: "scrollbar-h/corner", Handle: "scrollbar-h/corner", Type: "scrollbar_corner", Group: axis.Handle, Visible: true, Enabled: false, Bounds: semanticRect(image.Rect(90, 90, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 4},
	}
	router := NewRouter()
	router.Update(runtimeTree(axis))
	if !router.Press(1, image.Pt(80, 94)) {
		t.Fatal("track press was not handled")
	}
	change, ok := router.TakeScrollChange()
	if !ok || change.ID != axis.ID || change.Mode != "by" || change.X != 84 || change.Y != 0 {
		t.Fatalf("forward page change = %+v, %v", change, ok)
	}
	if !router.ScrollbarPointerOwned() {
		t.Fatal("track press did not retain pointer ownership")
	}
	if _, ok := router.Release(1, image.Pt(80, 94)); ok || router.ScrollbarPointerOwned() {
		t.Fatal("track release did not clear pointer ownership")
	}
	if !router.Press(1, image.Pt(40, 94)) {
		t.Fatal("thumb press after track page was not captured")
	}
	if router.Press(2, image.Pt(50, 94)) {
		t.Fatal("second pointer was accepted during thumb ownership")
	}
	router.MovePointer(2, image.Pt(70, 94), false)
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("second pointer moved the captured scrollbar")
	}
	router.Cancel(1)
	if router.Press(2, image.Pt(4, 94)) == false {
		t.Fatal("track press after cancellation was not handled")
	}
	change, ok = router.TakeScrollChange()
	if !ok || change.X != -84 {
		t.Fatalf("backward page change = %+v, %v", change, ok)
	}
	if _, ok := router.Release(2, image.Pt(4, 94)); ok || router.ScrollbarPointerOwned() {
		t.Fatal("second track press did not release ownership")
	}
	if router.Press(3, image.Pt(94, 94)) {
		t.Fatal("corner press was interactive")
	}
}

func TestRouterScrollbarKeyboardOperationsAndFocusOrder(t *testing.T) {
	verticalMaximum := 200.0
	horizontalMaximum := 180.0
	vertical := &semantic.Node{ID: "v", Handle: "v", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(90, 2, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), Value: 0, Max: &verticalMaximum, ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: 0, PaintOrder: 1}
	horizontal := &semantic.Node{ID: "h", Handle: "h", Type: "scrollbar", Role: "scrollbar", Orientation: "horizontal", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(2, 90, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), Value: 0, Max: &horizontalMaximum, ViewportSize: &semantic.Rect{Width: 100}, ContentSize: &semantic.Rect{Width: 280}, FocusOrder: 1, PaintOrder: 2}
	disabledMaximum := 100.0
	disabled := &semantic.Node{ID: "disabled", Handle: "disabled", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: false, Bounds: semanticRect(image.Rect(70, 2, 78, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), Max: &disabledMaximum, FocusOrder: -1, PaintOrder: 3}
	router := NewRouter()
	router.Update(runtimeTree(vertical, horizontal, disabled))
	if got := router.FocusNext(false); got != vertical.Handle {
		t.Fatalf("first scrollbar focus = %q", got)
	}
	if _, activated := router.KeyDown("ArrowDown"); activated {
		t.Fatal("scrollbar arrow unexpectedly returned activation")
	}
	change, ok := router.TakeScrollChange()
	if !ok || change.ID != vertical.ID || change.Mode != "by" || change.Y != 40 {
		t.Fatalf("vertical arrow change = %+v, %v", change, ok)
	}
	if _, activated := router.KeyDown("ArrowRight"); activated {
		t.Fatal("cross-axis arrow unexpectedly activated")
	}
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("cross-axis arrow emitted a scroll change")
	}
	if _, activated := router.KeyDown("PageDown"); activated {
		t.Fatal("page key unexpectedly returned activation")
	}
	change, ok = router.TakeScrollChange()
	if !ok || change.Mode != "by" || change.Y != 84 {
		t.Fatalf("vertical page change = %+v, %v", change, ok)
	}
	if got := router.FocusNext(false); got != horizontal.Handle {
		t.Fatalf("second scrollbar focus = %q", got)
	}
	if _, activated := router.KeyDown("Home"); activated {
		t.Fatal("home unexpectedly returned activation")
	}
	change, ok = router.TakeScrollChange()
	if !ok || change.Mode != "to" || change.X != 0 || change.Y != 0 {
		t.Fatalf("horizontal home change = %+v, %v", change, ok)
	}
	if got := router.FocusNext(false); got != vertical.Handle {
		t.Fatalf("disabled scrollbar polluted focus order, got %q", got)
	}
}

func TestRouterScrollbarFocusOrderMixesAuthoredControlsWithoutParts(t *testing.T) {
	maximum := 100.0
	button := &semantic.Node{Handle: "button", Role: "button", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 0, 40, 30)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), FocusOrder: 0}
	field := &semantic.Node{Handle: "field", Role: "textbox", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(0, 35, 80, 65)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), FocusOrder: 1}
	vertical := scrollbarTestAxis("vertical", 10, &maximum, image.Rect(90, 2, 98, 98), image.Rect(90, 2, 98, 34), image.Rect(0, 0, 100, 100))
	vertical.FocusOrder = 2
	horizontalMaximum := 100.0
	horizontal := scrollbarTestAxis("horizontal", 20, &horizontalMaximum, image.Rect(2, 90, 98, 98), image.Rect(2, 90, 34, 98), image.Rect(0, 0, 100, 100))
	horizontal.Orientation = "horizontal"
	horizontal.FocusOrder = 3
	router := NewRouter()
	router.Update(runtimeTree(button, field, vertical, horizontal))
	want := []string{"button", "field", "vertical", "horizontal"}
	for index, expected := range want {
		if got := router.FocusNext(false); got != expected {
			t.Fatalf("focus %d = %q, want %q", index, got, expected)
		}
	}
	if got := router.FocusNext(false); got != "button" {
		t.Fatalf("focus wrapped to %q, want button", got)
	}
	for _, region := range router.regions {
		if scrollbarPart(region) && region.FocusOrder >= 0 {
			t.Fatalf("scrollbar part entered focus order: %+v", region)
		}
	}
}

func TestRouterScrollbarCaptureCancelsWhenAxisDisappears(t *testing.T) {
	maximum := 100.0
	axis := &semantic.Node{ID: "axis", Handle: "axis", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(90, 2, 98, 98)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), Max: &maximum, ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: 0, PaintOrder: 1}
	axis.Children = []*semantic.Node{{ID: "axis/thumb", Handle: "axis/thumb", Type: "scrollbar_thumb", Group: axis.Handle, Visible: true, Enabled: true, Bounds: semanticRect(image.Rect(90, 2, 98, 34)), Clip: semanticRect(image.Rect(0, 0, 100, 100)), PaintOrder: 2}}
	router := NewRouter()
	router.Update(runtimeTree(axis))
	if !router.Press(1, image.Pt(94, 12)) || !router.ScrollbarCaptured() {
		t.Fatal("scrollbar thumb did not capture")
	}
	router.MovePointer(1, image.Pt(94, 90), false)
	hidden := *axis
	hidden.Visible = false
	router.Update(runtimeTree(&hidden))
	if router.ScrollbarCaptured() || router.Transient().Pressed != "" {
		t.Fatalf("stale scrollbar capture survived update: captured=%v transient=%+v", router.ScrollbarCaptured(), router.Transient())
	}
	if _, ok := router.TakeScrollChange(); ok {
		t.Fatal("hidden scrollbar retained a stale queued scroll change")
	}
}

func runtimeTree(children ...*semantic.Node) *semantic.Node {
	return &semantic.Node{Type: "_viewport", Visible: true, Enabled: true, Children: children}
}

func semanticButton(handle, scope string, bounds, clip image.Rectangle, disabled bool, actions ...document.Action) *semantic.Node {
	return &semantic.Node{
		Handle: handle, Role: "button", Scope: scope, Visible: true, Enabled: !disabled,
		Bounds: semanticRect(bounds), Clip: semanticRect(clip), Actions: actions,
	}
}

func scrollbarTestAxis(handle string, paintOrder int, maximum *float64, bounds, thumb, clip image.Rectangle) *semantic.Node {
	axis := &semantic.Node{ID: handle, Handle: handle, Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: true,
		Bounds: semanticRect(bounds), Clip: semanticRect(clip), Max: maximum, ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: -1, PaintOrder: paintOrder}
	axis.Children = []*semantic.Node{{ID: handle + "/thumb", Handle: handle + "/thumb", Type: "scrollbar_thumb", Group: handle, Visible: true, Enabled: true,
		Bounds: semanticRect(thumb), Clip: semanticRect(clip), FocusOrder: -1, PaintOrder: paintOrder + 1}}
	return axis
}

func semanticRect(value image.Rectangle) *semantic.Rect {
	return &semantic.Rect{X: value.Min.X, Y: value.Min.Y, Width: value.Dx(), Height: value.Dy()}
}
