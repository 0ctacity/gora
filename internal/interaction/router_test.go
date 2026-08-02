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

func runtimeTree(children ...*semantic.Node) *semantic.Node {
	return &semantic.Node{Type: "_viewport", Visible: true, Enabled: true, Children: children}
}

func semanticButton(handle, scope string, bounds, clip image.Rectangle, disabled bool, actions ...document.Action) *semantic.Node {
	return &semantic.Node{
		Handle: handle, Role: "button", Scope: scope, Visible: true, Enabled: !disabled,
		Bounds: semanticRect(bounds), Clip: semanticRect(clip), Actions: actions,
	}
}

func semanticRect(value image.Rectangle) *semantic.Rect {
	return &semantic.Rect{X: value.Min.X, Y: value.Min.Y, Width: value.Dx(), Height: value.Dy()}
}
