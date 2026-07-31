package interaction

import (
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/render"
)

func TestRouterCapturesPointerAndActivatesOnlyOnReleaseInside(t *testing.T) {
	router := NewRouter()
	router.Update([]render.InteractionRegion{{
		Handle: "button", Scope: "screen:main", Bounds: image.Rect(0, 0, 100, 40), Clip: image.Rect(0, 0, 80, 40),
		Actions: []document.Action{{Action: "toggle", State: "on"}},
	}})
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
	router.Update([]render.InteractionRegion{
		{Handle: "first", Bounds: image.Rect(0, 0, 100, 40), Clip: image.Rect(0, 0, 100, 40)},
		{Handle: "disabled", Bounds: image.Rect(0, 50, 100, 90), Clip: image.Rect(0, 50, 100, 90), Disabled: true},
		{Handle: "top", Bounds: image.Rect(0, 0, 100, 40), Clip: image.Rect(0, 0, 100, 40)},
	})
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
	router.Update([]render.InteractionRegion{{Handle: "button", Scope: "scope", Bounds: image.Rect(0, 0, 10, 10), Clip: image.Rect(0, 0, 10, 10)}})
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
