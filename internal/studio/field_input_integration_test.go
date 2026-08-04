package studio

import (
	"image"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/semantic"
)

func TestAppFrameRoutesKeyboardClipboardAndIMEToFocusedField(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "forms", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}

	var inputRouter input.Router
	var operations op.Ops
	state := newAppUIState()
	theme := material.NewTheme()
	window := new(app.Window)
	now := time.UnixMilli(750)
	gtx := layout.Context{
		Ops: &operations, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(960, 760)), Now: now,
	}
	frame := func() {
		gtx.Reset()
		gtx.Constraints = layout.Exact(image.Pt(960, 760))
		gtx.Now = now
		layoutAppContent(gtx, theme, runtime, state, window)
		inputRouter.Frame(gtx.Ops)
	}
	queue := func(events ...event.Event) {
		for _, event := range events {
			inputRouter.Queue(event)
		}
		frame()
	}

	frame()
	field := namedSemanticNode(state.runtimeTree, "name-field")
	if field == nil {
		t.Fatal("name field is absent from the app runtime tree")
	}
	var box *semantic.Node
	for _, child := range field.Children {
		if child != nil && child.Type == "field_box" {
			box = child
			break
		}
	}
	if box == nil || box.Bounds == nil || box.Clip == nil {
		t.Fatalf("name field box has no interactive geometry: %+v", box)
	}
	visible := box.Bounds.ImageRectangle().Intersect(box.Clip.ImageRectangle())
	point := f32.Pt(float32(visible.Min.X+visible.Max.X)/2, float32(visible.Min.Y+visible.Max.Y)/2)
	queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: point})
	queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Release, Position: point})
	if focused := runtime.Snapshot().Transient.Focused; focused != field.Handle {
		t.Fatalf("pointer focus = %q, want %q", focused, field.Handle)
	}
	if !inputRouter.Source().Focused(&state.interactionInput) {
		t.Fatal("Gio interaction tag did not receive keyboard focus")
	}
	state.caretBlinkStart = time.Time{}
	queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	if !state.caretBlinkStart.Equal(now) {
		t.Fatalf("caret blink start = %v, want movement time %v", state.caretBlinkStart, now)
	}

	queue(key.SelectionEvent(key.Range{Start: 0, End: 3}))
	if start, end, _ := runtime.FieldRuneSelection(field.ID); start != 0 || end != 3 {
		t.Fatalf("IME selection = %d..%d, want 0..3", start, end)
	}
	queue(key.Event{Name: key.Name("X"), Modifiers: key.ModShortcut, State: key.Press})
	if draft, _ := runtime.FieldDraft(field.ID); draft != " Lovelace" {
		t.Fatalf("shortcut cut draft = %q, want %q", draft, " Lovelace")
	}
	queue(key.Event{Name: key.Name("V"), Modifiers: key.ModShortcut, State: key.Press})
	queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser {
		return io.NopCloser(strings.NewReader("Ada"))
	}})
	if draft, _ := runtime.FieldDraft(field.ID); draft != "Ada Lovelace" {
		t.Fatalf("clipboard paste draft = %q, want %q", draft, "Ada Lovelace")
	}

	queue(key.SelectionEvent(key.Range{Start: 0, End: 3}))
	queue(key.CompositionEvent(key.Range{Start: 0, End: 3}))
	queue(key.EditEvent{Range: key.Range{Start: 0, End: 3}, Text: "İ"})
	editing, _ := runtime.editing.State(field.ID)
	if !editing.Composing || editing.Draft != "İ Lovelace" {
		t.Fatalf("active composition = %+v", editing)
	}
	queue(key.CompositionEvent(key.Range{}))
	editing, _ = runtime.editing.State(field.ID)
	if editing.Composing || editing.Draft != "İ Lovelace" {
		t.Fatalf("finished composition = %+v", editing)
	}
	if value := runtime.Snapshot().StateValues["screen:profile"]["name"]; value != "İ Lovelace" {
		t.Fatalf("published composition value = %#v, want %q", value, "İ Lovelace")
	}

	queue(key.Event{Name: key.Name("A"), Modifiers: key.ModShortcut, State: key.Press})
	start, end, _ := runtime.FieldRuneSelection(field.ID)
	current, _ := runtime.FieldDraft(field.ID)
	if start != 0 || end != len([]rune(current)) {
		t.Fatalf("select-all range = %d..%d, want 0..%d", start, end, len([]rune(current)))
	}
	text := []rune("Grace Hopper")
	burst := make([]event.Event, 0, len(text))
	for index, value := range text {
		replaceStart, replaceEnd := index, index
		if index == 0 {
			replaceStart, replaceEnd = start, end
		}
		burst = append(burst, key.EditEvent{Range: key.Range{Start: replaceStart, End: replaceEnd}, Text: string(value)})
	}
	queue(burst...)
	if draft, _ := runtime.FieldDraft(field.ID); draft != string(text) {
		t.Fatalf("burst IME draft = %q, want %q", draft, string(text))
	}
}
