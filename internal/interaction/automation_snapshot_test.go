package interaction

import (
	"encoding/json"
	"image"
	"strings"
	"testing"

	"gora/internal/semantic"
)

func TestRouterSnapshotDoesNotDrainQueuesAndIncludesTransientOwners(t *testing.T) {
	router := NewRouter()
	button := &semantic.Node{
		Type: "button", Role: "button", Handle: "button-handle", ID: "button-id",
		Visible: true, InViewport: true, Enabled: true, Bounds: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 30}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 30},
		FocusOrder: 0,
	}
	router.Update(button)
	router.SetPointerMetadata("mouse", 1, image.Pt(10, 12))
	if !router.Press(7, image.Pt(10, 12)) {
		t.Fatal("button press was not captured")
	}
	router.valueChange = &ControlValueChange{ID: button.ID, Value: true}
	router.scrollChange = &ScrollChange{ID: "scrollbar-id", Mode: "by", X: 2, Y: 3}

	snapshot := router.Snapshot()
	if snapshot.FocusedID != button.ID || len(snapshot.PressedIDs) != 1 || snapshot.PressedIDs[0] != button.ID {
		t.Fatalf("transient snapshot = %+v", snapshot)
	}
	if snapshot.PointerCapture == nil || snapshot.PointerCapture.PointerID != 7 || snapshot.PointerCapture.Source != "mouse" || snapshot.PointerCapture.Point != image.Pt(10, 12) {
		t.Fatalf("pointer snapshot = %+v", snapshot.PointerCapture)
	}
	if snapshot.QueueSizes.ValueChanges != 1 || snapshot.QueueSizes.ScrollChanges != 1 {
		t.Fatalf("queue snapshot = %+v", snapshot.QueueSizes)
	}
	if _, ok := router.TakeValueChange(); !ok {
		t.Fatal("value queue was drained by snapshot")
	}
	if _, ok := router.TakeScrollChange(); !ok {
		t.Fatal("scroll queue was drained by snapshot")
	}
}

func TestRouterSnapshotIncludesCanonicalTransientControlOwners(t *testing.T) {
	selectNode := &semantic.Node{Type: "select", Role: "combobox", Handle: "select-handle", ID: "select-id", Visible: true, InViewport: true, Enabled: true, Bounds: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 30}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200}, FocusOrder: 0}
	option := &semantic.Node{Type: "option", Role: "option", Handle: "option-handle", ID: "option-id", Visible: true, InViewport: true, Enabled: true, Bounds: &semantic.Rect{X: 0, Y: 30, Width: 80, Height: 30}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200}}
	disabled := &semantic.Node{Type: "button", Role: "button", Handle: "disabled-handle", ID: "disabled-id", Visible: true, InViewport: true, Enabled: false, Bounds: &semantic.Rect{X: 0, Y: 60, Width: 80, Height: 30}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200}}
	bar := &semantic.Node{Type: "scrollbar", Role: "scrollbar", Handle: "bar-handle", ID: "bar-id", Visible: true, InViewport: true, Enabled: true, Bounds: &semantic.Rect{X: 90, Y: 0, Width: 8, Height: 100}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200}}
	slider := &semantic.Node{Type: "slider", Role: "slider", Handle: "slider-handle", ID: "slider-id", Visible: true, InViewport: true, Enabled: true, Bounds: &semantic.Rect{X: 0, Y: 100, Width: 100, Height: 20}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200}}
	tree := &semantic.Node{Type: "surface", Visible: true, InViewport: true, Enabled: true, Children: []*semantic.Node{selectNode, option, disabled, bar, slider}}
	router := NewRouter()
	router.Update(tree)
	router.transient = Transient{Hovered: selectNode.Handle, Pressed: disabled.Handle, Focused: slider.Handle, OpenSelect: selectNode.Handle, ActiveOption: option.Handle}
	router.keyboardPress = selectNode.Handle
	router.keyboardKey = "Space"
	router.scrollCapture = &scrollbarCapture{axis: bar}
	router.captureHandle = slider.Handle
	router.captureID = 5
	snapshot := router.Snapshot()
	if snapshot.HoveredIDs[0] != selectNode.ID || snapshot.PressedIDs[0] != disabled.ID || snapshot.FocusedID != slider.ID || snapshot.OpenSelectID != selectNode.ID || snapshot.ActiveIDs[0] != option.ID {
		t.Fatalf("transient IDs = %+v", snapshot)
	}
	if len(snapshot.DisabledIDs) != 1 || snapshot.DisabledIDs[0] != disabled.ID || snapshot.KeyboardPress == nil || snapshot.KeyboardPress.OwnerID != selectNode.ID || snapshot.KeyboardPress.Key != "Space" {
		t.Fatalf("disabled/keyboard snapshot = %+v", snapshot)
	}
	if snapshot.ScrollbarGestureOwner != bar.ID || snapshot.SliderGestureOwner != slider.ID {
		t.Fatalf("gesture owners = %+v", snapshot)
	}
}

func TestEditingSnapshotIsImmutableAndIncludesComposition(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen:main", Binding: "name", Type: "text", Value: "Ada"}})
	if err := store.SetRuneSelection("field", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetComposition("field", 0, 1); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	field := snapshot.Fields["field"]
	if !field.Composing || field.CompositionStart != 0 || field.CompositionEnd != 1 || field.SelectionStart != 0 || field.SelectionEnd != 1 {
		t.Fatalf("editing snapshot = %+v", field)
	}
	field.Issues = append(field.Issues, ValidationIssue{Code: "mutated"})
	field.Draft = "mutated"
	if got := store.Snapshot().Fields["field"]; len(got.Issues) != 0 {
		t.Fatalf("snapshot mutation leaked into store: %+v", got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, hidden := range []string{"undo", "redo", "internal_offset", "manual_scroll", "preferred_column", "visual_columns"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("public editing snapshot exposed %q: %s", hidden, text)
		}
	}
	if !strings.Contains(text, `"id":"field"`) || !strings.Contains(text, `"selection_start":0`) {
		t.Fatalf("public editing snapshot omitted required metadata: %s", text)
	}
}
