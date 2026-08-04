package studio

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/render"
	"gora/internal/semantic"
)

func TestRuntimeInspectPublishesCanonicalTreeAndLastGoodDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 80 }
entry: home
screens:
  home:
    type: link
    name: reports-link
    props: { label: Reports, to: reports }
    children:
      - { type: text, props: { text: Reports } }
  reports: { type: spacer }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	data, warning, err := runtime.Inspect("headless")
	if err != nil || warning != "" {
		t.Fatalf("inspect error=%v warning=%q", err, warning)
	}
	var envelope semantic.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Valid || envelope.HostMode != "headless" || envelope.CurrentScreen != "home" || envelope.Root == nil || envelope.Root.Role != "link" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	data, warning, err = runtime.Inspect("headless")
	if err != nil || warning == "" {
		t.Fatalf("last-good inspect error=%v warning=%q", err, warning)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Valid || envelope.Root == nil || len(envelope.Diagnostics) == 0 {
		t.Fatalf("last-good envelope = %+v", envelope)
	}
}

func TestRuntimeSemanticControlMethodsActivateSetResetAndScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 120, height: 80 }
state:
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: stack
    props: { direction: vertical }
    children:
      - type: button
        name: increment
        props: { label: Increment }
        on:
          activate: [{ action: increment, state: count }]
        children: [{ type: text, props: { text: Add } }]
      - type: scroll
        name: feed
        props: { axis: vertical }
        children: [{ type: spacer, props: { height: 200 } }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	button := namedSemanticNode(tree, "increment")
	if button == nil {
		t.Fatal("missing button")
	}
	if err := runtime.ActivateSemanticID(button.ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["count"]; got != float64(2) {
		t.Fatalf("count after activation = %#v", got)
	}
	if err := runtime.SetStateValues("screen:main", map[string]any{"count": float64(7)}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ResetStateScope("screen:main"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["count"]; got != float64(1) {
		t.Fatalf("count after reset = %#v", got)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	var scroll *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.Type == "scroll" {
			scroll = node
			break
		}
	}
	if scroll == nil {
		t.Fatal("missing scroll")
	}
	if err := runtime.ScrollSemanticID(scroll.ID, "to", 0, 500); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Scroll["feed"].Y; got != 120 {
		t.Fatalf("clamped scroll = %d, want 120", got)
	}
}

func TestRuntimeFieldDraftSubmitAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forms.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
  submitted: { type: boolean, default: false }
entry: main
screens:
  main:
    type: form
    name: profile-form
    on:
      submit: [{ action: set, state: submitted, value: true }]
    children:
      - type: stack
        props: { direction: vertical, gap: 8 }
        children:
          - type: text_field
            name: name-field
            props: { label: Name, bind: name, required: true, min_length: 2, placeholder: Name }
            children:
              - type: field_box
                props: { height: 36, padding: { top: 8, right: 8, bottom: 8, left: 8 } }
          - type: button
            name: submit-button
            props: { label: Save, form_action: submit }
            children: [{ type: text, props: { text: Save } }]
          - type: button
            name: reset-button
            props: { label: Reset, form_action: reset }
            children: [{ type: text, props: { text: Reset } }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	form := namedSemanticNode(tree, "profile-form")
	resetButton := namedSemanticNode(tree, "reset-button")
	if field == nil || field.Role != "textbox" || form == nil || form.Role != "form" || resetButton == nil {
		t.Fatalf("field=%+v form=%+v reset=%+v", field, form, resetButton)
	}
	if err := runtime.SetFieldDraft(field.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(form.ID); err == nil {
		t.Fatal("invalid form submission succeeded")
	}
	if focused := runtime.Snapshot().Transient.Focused; focused != field.Handle {
		t.Fatalf("invalid submit focused %q, want first invalid field %q", focused, field.Handle)
	}
	invalidTree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	invalidField := namedSemanticNode(invalidTree, "name-field")
	if invalidField == nil || !invalidField.Touched || invalidField.Valid == nil || *invalidField.Valid {
		t.Fatalf("invalid submitted field metadata = %+v", invalidField)
	}
	if values := runtime.Snapshot().StateValues["screen:main"]; values["name"] != "Ada" || values["submitted"] != false {
		t.Fatalf("invalid submit values = %+v", values)
	}
	if err := runtime.SetFieldDraft(field.ID, "Grace"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(form.ID); err != nil {
		t.Fatal(err)
	}
	if values := runtime.Snapshot().StateValues["screen:main"]; values["name"] != "Grace" || values["submitted"] != true {
		t.Fatalf("submitted values = %+v", values)
	}
	runtime.SetTransient(interaction.Transient{Focused: resetButton.Handle})
	if err := runtime.ActivateSemanticID(resetButton.ID); err != nil {
		t.Fatal(err)
	}
	if focused := runtime.Snapshot().Transient.Focused; focused != resetButton.Handle {
		t.Fatalf("form reset moved focus to %q, want %q", focused, resetButton.Handle)
	}
	if values := runtime.Snapshot().StateValues["screen:main"]; values["name"] != "Ada" || values["submitted"] != true {
		t.Fatalf("reset values = %+v", values)
	}
	if err := runtime.SetFieldDraft(field.ID, "unsaved"); err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot().RuntimeRevision
	if value, err := runtime.SetControlValue(field.ID, "Ada"); err != nil || value != "Ada" {
		t.Fatalf("same-value external write = %#v, %v", value, err)
	}
	if draft, _ := runtime.FieldDraft(field.ID); draft != "Ada" {
		t.Fatalf("same-value external write left draft %q", draft)
	}
	if runtime.Snapshot().RuntimeRevision <= before {
		t.Fatal("observable draft replacement did not increment runtime revision")
	}
	if value, err := runtime.SetControlValue(field.ID, "A\nB\r\nC"); err != nil || value != "ABC" {
		t.Fatalf("single-line external write = %#v, %v, want ABC", value, err)
	}
	if draft, _ := runtime.FieldDraft(field.ID); draft != "ABC" {
		t.Fatalf("single-line external write left draft %q", draft)
	}
	changedConstraints := strings.Replace(string(source), "min_length: 2", "min_length: 5", 1)
	if err := os.WriteFile(path, []byte(changedConstraints), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	if draft, _ := runtime.FieldDraft(field.ID); draft != "ABC" {
		t.Fatalf("compatible reload replaced draft %q", draft)
	}
	reloadedTree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	if reloadedField := namedSemanticNode(reloadedTree, "name-field"); reloadedField == nil || reloadedField.Valid == nil || *reloadedField.Valid {
		t.Fatalf("changed constraints did not revalidate retained draft: %+v", reloadedField)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	if draft, _ := runtime.FieldDraft(field.ID); draft != "ABC" || !runtime.Snapshot().Invalid {
		t.Fatalf("invalid reload draft=%q invalid=%v", draft, runtime.Snapshot().Invalid)
	}
}

func TestValidFieldDraftPublishesStateWhileInvalidDraftPreservesLastValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live-fields.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
  seats: { type: number, default: 3, min: 1, max: 9, step: 2 }
entry: main
screens:
  main:
    type: stack
    props: { direction: vertical }
    children:
      - type: text_field
        name: name-field
        props: { label: Name, bind: name, required: true, min_length: 2 }
        children: [{ type: field_box }]
      - type: text_field
        name: seats-field
        props: { label: Seats, bind: seats }
        children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	name := namedSemanticNode(tree, "name-field")
	seats := namedSemanticNode(tree, "seats-field")
	if err := runtime.SetFieldDraft(name.ID, "Grace"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("valid text draft published %#v, want Grace", got)
	}
	if err := runtime.SetFieldDraft(name.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("invalid text draft overwrote state with %#v", got)
	}
	if err := runtime.SetFieldDraft(seats.ID, "6"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["seats"]; got != float64(7) {
		t.Fatalf("normalized number draft published %#v, want 7", got)
	}
	if err := runtime.SetFieldDraft(seats.ID, "-"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["seats"]; got != float64(7) {
		t.Fatalf("partial number draft overwrote state with %#v", got)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	name = namedSemanticNode(tree, "name-field")
	seats = namedSemanticNode(tree, "seats-field")
	if name.Value != "" || name.CommittedValue != "Grace" || name.Valid == nil || *name.Valid {
		t.Fatalf("invalid text field semantics = %+v", name)
	}
	if seats.Value != "-" || seats.CommittedValue != float64(7) || seats.Valid == nil || *seats.Valid {
		t.Fatalf("partial number field semantics = %+v", seats)
	}
}

func TestFieldFocusChangeFinishesCompositionAndPublishesValidDraft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forms.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: form
    name: profile-form
    children:
      - type: stack
        props: { direction: vertical }
        children:
          - type: text_field
            name: name-field
            props: { label: Name, bind: name, required: true }
            children:
              - { type: field_box }
          - type: button
            name: save-button
            props: { label: Save }
            children:
              - { type: text, props: { text: Save } }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	button := namedSemanticNode(tree, "save-button")
	runtime.SetTransient(interaction.Transient{Focused: field.Handle})
	if err := runtime.SetFieldComposition(field.ID, 0, 3); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyFieldEdit(field.ID, 0, 3, "Grace"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Ada" {
		t.Fatalf("composing draft published early: %#v", got)
	}

	runtime.SetTransient(interaction.Transient{Focused: button.Handle})
	snapshot := runtime.Snapshot()
	if got := snapshot.StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("finished composition was not published on focus change: %#v", got)
	}
	state := snapshot.Editing[field.ID]
	if state.Composing || !state.Touched {
		t.Fatalf("field after focus change = %+v", state)
	}
}

func TestKeyboardEditsUndoAndRedoPublishOnlyValidDrafts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyboard-field.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 240, height: 100 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: text_field
    name: name-field
    props: { label: Name, bind: name, required: true, min_length: 2 }
    children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	if err := runtime.ApplyFieldEdit(field.ID, 0, 3, "Grace"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("keyboard edit published %#v, want Grace", got)
	}
	beforeNoOp := runtime.Snapshot().RuntimeRevision
	if err := runtime.ApplyFieldEdit(field.ID, 5, 5, ""); err != nil {
		t.Fatal(err)
	}
	if after := runtime.Snapshot().RuntimeRevision; after != beforeNoOp {
		t.Fatalf("empty edit changed runtime revision from %d to %d", beforeNoOp, after)
	}
	if !runtime.UndoField(field.ID) {
		t.Fatal("undo did not change the field")
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Ada" {
		t.Fatalf("undo published %#v, want Ada", got)
	}
	if !runtime.RedoField(field.ID) {
		t.Fatal("redo did not change the field")
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("redo published %#v, want Grace", got)
	}
	if err := runtime.SetFieldSelection(field.ID, 0, 5); err != nil {
		t.Fatal(err)
	}
	if !runtime.DeleteFieldSelection(field.ID, true, false) {
		t.Fatal("selection deletion did not change the field")
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Grace" {
		t.Fatalf("invalid deletion overwrote state with %#v", got)
	}
}

func TestValidDraftSynchronizesRepeatedBindingsWithoutReplacingItsOwnEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repeated-binding.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 240, height: 120 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: stack
    props: { direction: vertical }
    children:
      - type: text_field
        name: first-name
        props: { label: First, bind: name, required: true }
        children: [{ type: field_box }]
      - type: text_field
        name: second-name
        props: { label: Second, bind: name, required: true }
        children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	first := namedSemanticNode(tree, "first-name")
	if err := runtime.SetFieldDraft(first.ID, "Grace"); err != nil {
		t.Fatal(err)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	first = namedSemanticNode(tree, "first-name")
	second := namedSemanticNode(tree, "second-name")
	if first.Value != "Grace" || first.CommittedValue != "Grace" || !first.Dirty {
		t.Fatalf("source field = %+v", first)
	}
	if second.Value != "Grace" || second.CommittedValue != "Grace" || second.Dirty {
		t.Fatalf("repeated binding field = %+v", second)
	}
	if _, err := runtime.SetControlValue(first.ID, "Linus"); err != nil {
		t.Fatal(err)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	first = namedSemanticNode(tree, "first-name")
	second = namedSemanticNode(tree, "second-name")
	if first.Value != "Linus" || second.Value != "Linus" || first.Dirty || second.Dirty {
		t.Fatalf("external repeated binding sync = first %+v second %+v", first, second)
	}
}

func TestSetControlValueRejectsFieldValuesThatFailFieldValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "validated-control-value.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 240, height: 100 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: text_field
    name: name-field
    props:
      label: Name
      bind: name
      required: true
      min_length: 2
      pattern: '[A-Z][a-z]+'
    children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	before := runtime.Snapshot().RuntimeRevision
	if _, err := runtime.SetControlValue(field.ID, "x"); err == nil {
		t.Fatal("invalid field value was accepted")
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["name"]; got != "Ada" {
		t.Fatalf("invalid control value changed state to %#v", got)
	}
	if draft, _ := runtime.FieldDraft(field.ID); draft != "Ada" {
		t.Fatalf("invalid control value changed draft to %q", draft)
	}
	if after := runtime.Snapshot().RuntimeRevision; after != before {
		t.Fatalf("rejected control value changed revision from %d to %d", before, after)
	}
	value, err := runtime.SetControlValue(field.ID, "Grace")
	if err != nil || value != "Grace" {
		t.Fatalf("valid field value = %#v, %v", value, err)
	}
}

func TestActivateSemanticIDRejectsDisabledFormButtons(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disabled-form-button.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: form
    name: profile-form
    children:
      - type: stack
        props: { direction: vertical }
        children:
          - type: text_field
            name: name-field
            props: { label: Name, bind: name }
            children: [{ type: field_box }]
          - type: button
            name: disabled-reset
            props: { label: Reset, form_action: reset, disabled: true }
            children: [{ type: text, props: { text: Reset } }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	button := namedSemanticNode(tree, "disabled-reset")
	if field == nil || button == nil {
		t.Fatalf("field=%+v button=%+v", field, button)
	}
	if err := runtime.SetFieldDraft(field.ID, "unsaved"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ActivateSemanticID(button.ID); err == nil {
		t.Fatal("disabled reset button activated")
	}
	if draft, _ := runtime.FieldDraft(field.ID); draft != "unsaved" {
		t.Fatalf("disabled reset changed draft to %q", draft)
	}
}

func TestFocusRevealUsesFieldCaretInsteadOfEntireFieldBounds(t *testing.T) {
	runtime := &Runtime{scroll: map[string]image.Point{"feed": {}}, publishedTree: &semantic.Node{
		Type: "scroll", Name: "feed", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, Props: map[string]any{"axis": "vertical"},
		Children: []*semantic.Node{{
			Type: "surface", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 120}, Children: []*semantic.Node{{
				Handle: "notes", Role: "textbox", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Children: []*semantic.Node{{
					Type: "field_box", Bounds: &semantic.Rect{X: 0, Y: 20, Width: 100, Height: 70},
					Props: map[string]any{"text": "first\nsecond\nthird", "field_multiline": true, "selection_start": float64(0), "selection_end": float64(0), "size": float64(12)},
				}},
			}},
		}},
	}}
	runtime.revealFocusedLocked("notes")
	if got := runtime.scroll["feed"].Y; got != 0 {
		t.Fatalf("focus reveal scrolled by %d for an already-visible caret", got)
	}
}

func TestFocusRevealPropagatesAdjustedCaretThroughNestedScrollports(t *testing.T) {
	field := &semantic.Node{Handle: "notes", Role: "textbox", Bounds: &semantic.Rect{X: 0, Y: 180, Width: 100, Height: 40}, Children: []*semantic.Node{{
		Type: "field_box", Bounds: &semantic.Rect{X: 0, Y: 180, Width: 100, Height: 40},
		Props: map[string]any{"text": "caret", "field_multiline": true, "selection_start": float64(0), "selection_end": float64(0), "size": float64(12)},
	}}}
	inner := &semantic.Node{
		Type: "scroll", Name: "inner", Bounds: &semantic.Rect{X: 0, Y: 80, Width: 100, Height: 50}, Props: map[string]any{"axis": "vertical"},
		Children: []*semantic.Node{{Type: "surface", Bounds: &semantic.Rect{X: 0, Y: 80, Width: 100, Height: 150}, Children: []*semantic.Node{field}}},
	}
	runtime := &Runtime{scroll: map[string]image.Point{"outer": {}, "inner": {}}, publishedTree: &semantic.Node{
		Type: "scroll", Name: "outer", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Props: map[string]any{"axis": "vertical"},
		Children: []*semantic.Node{{Type: "surface", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 300}, Children: []*semantic.Node{inner}}},
	}}
	runtime.revealFocusedLocked("notes")
	caret := render.FieldCaretRect(field.Children[0].Props, field.Children[0].Bounds.ImageRectangle())
	wantInner := caret.Max.Y - inner.Bounds.ImageRectangle().Max.Y
	if innerOffset := runtime.scroll["inner"].Y; innerOffset != wantInner {
		t.Fatalf("inner reveal offset = %d, want minimum %d", innerOffset, wantInner)
	}
	if outerOffset := runtime.scroll["outer"].Y; outerOffset != 30 {
		t.Fatalf("outer reveal offset = %d, want 30 after inner translation", outerOffset)
	}
}

func TestFormSubmitIncludesHiddenAndReadOnlyFieldsButSkipsDisabledFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "form-boundaries.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 220 }
state:
  hide: { type: boolean, default: true }
  disable_field: { type: boolean, default: true }
  hidden_value: { type: text, default: "" }
  disabled_value: { type: text, default: "" }
  locked_value: { type: text, default: locked }
  submitted: { type: boolean, default: false }
entry: main
screens:
  main:
    type: form
    name: boundary-form
    on:
      submit: [{ action: set, state: submitted, value: true }]
    children:
      - type: stack
        props: { direction: vertical }
        children:
          - type: text_field
            name: hidden-field
            props: { label: Hidden, bind: hidden_value, required: true }
            variants:
              - when: { state: hide, equals: true }
                visible: false
            children: [{ type: field_box }]
          - type: text_field
            name: disabled-field
            props: { label: Disabled, bind: disabled_value, required: true, disabled: { ref: state.disable_field } }
            children: [{ type: field_box }]
          - type: text_field
            name: locked-field
            props: { label: Locked, bind: locked_value, required: true, read_only: true }
            children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	form := namedSemanticNode(tree, "boundary-form")
	disabled := namedSemanticNode(tree, "disabled-field")
	if disabled.Valid != nil || disabled.Issues != nil {
		t.Fatalf("disabled field exposed validation state: %+v", disabled)
	}
	if err := runtime.SetStateValues("screen:main", map[string]any{"disable_field": false}); err != nil {
		t.Fatal(err)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	disabled = namedSemanticNode(tree, "disabled-field")
	if disabled.Valid == nil || *disabled.Valid || disabled.Issues == nil {
		t.Fatalf("enabled empty required field was not revalidated: %+v", disabled)
	}
	if err := runtime.SetStateValues("screen:main", map[string]any{"disable_field": true}); err != nil {
		t.Fatal(err)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	form = namedSemanticNode(tree, "boundary-form")
	if err := runtime.SubmitForm(form.ID); err == nil {
		t.Fatal("hidden enabled invalid field did not block submission")
	}
	if err := runtime.SetStateValues("screen:main", map[string]any{"hidden_value": "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(form.ID); err != nil {
		t.Fatalf("disabled invalid field should have been skipped: %v", err)
	}
	values := runtime.Snapshot().StateValues["screen:main"]
	if values["hidden_value"] != "ready" || values["locked_value"] != "locked" || values["submitted"] != true {
		t.Fatalf("submitted values = %+v", values)
	}
}

func TestFormSubmitPublishesDraftBeforeActionsAndNavigatesLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "submit-order.gora")
	if err := os.WriteFile(path, []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
  saved: { type: text, default: "" }
entry: edit
screens:
  edit:
    type: form
    name: profile-form
    on:
      submit:
        - { action: set, state: saved, value: { ref: state.name } }
        - { action: navigate, to: done }
    children:
      - type: text_field
        name: name-field
        props: { label: Name, bind: name, required: true }
        children: [{ type: field_box }]
  done:
    type: text
    props: { text: Done }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetFieldDraft(namedSemanticNode(tree, "name-field").ID, "Grace"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(namedSemanticNode(tree, "profile-form").ID); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.Screen != "done" {
		t.Fatalf("selected screen = %q, want done", snapshot.Screen)
	}
	values := snapshot.StateValues["screen:edit"]
	if values["name"] != "Grace" || values["saved"] != "Grace" {
		t.Fatalf("published/action values = %+v", values)
	}
}

func TestFormSubmissionKeepsComponentFieldScopesIsolated(t *testing.T) {
	dir := t.TempDir()
	componentPath := filepath.Join(dir, "contact.gora")
	component := []byte(`
gora: 1
kind: component
name: Contact
viewport: { width: 220, height: 48 }
state:
  nickname: { type: text, default: Countess }
previews:
  default: {}
root:
  type: text_field
  name: nickname-field
  props: { label: Nickname, bind: nickname, required: true }
  children: [{ type: field_box, props: { height: 40 } }]
`)
	if err := os.WriteFile(componentPath, component, 0o600); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(dir, "app.gora")
	appSource := []byte(`
gora: 1
kind: app
imports:
  components: { contact: ./contact.gora }
viewport: { width: 320, height: 180 }
entry: main
screens:
  main:
    type: form
    name: contacts-form
    children:
      - type: stack
        props: { direction: vertical, gap: 8 }
        children:
          - { type: instance, name: primary-contact, props: { component: contact } }
          - { type: instance, name: secondary-contact, props: { component: contact } }
`)
	if err := os.WriteFile(appPath, appSource, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, appPath)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	form := namedSemanticNode(tree, "contacts-form")
	var fields []*semantic.Node
	var nodes []string
	for _, node := range semantic.Flatten(tree) {
		nodes = append(nodes, node.Type+":"+node.Name+":"+node.Role)
		if node.Name == "nickname-field" {
			fields = append(fields, node)
		}
	}
	if form == nil || len(fields) != 2 || fields[0].Scope == fields[1].Scope {
		t.Fatalf("form=%+v fields=%+v nodes=%v", form, fields, nodes)
	}
	if err := runtime.SetFieldDraft(fields[0].ID, "Primary"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetFieldDraft(fields[1].ID, "Secondary"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(form.ID); err != nil {
		t.Fatal(err)
	}
	values := runtime.Snapshot().StateValues
	if got := values[fields[0].Scope]["nickname"]; got != "Primary" {
		t.Fatalf("primary scope value = %#v", got)
	}
	if got := values[fields[1].Scope]["nickname"]; got != "Secondary" {
		t.Fatalf("secondary scope value = %#v", got)
	}
}

func TestFormSubmissionKeepsScreenFieldScopesIsolated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screens.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 180 }
state:
  name: { type: text, default: Ada }
entry: first
screens:
  first:
    type: form
    name: first-form
    children:
      - type: text_field
        name: first-name
        props: { label: First name, bind: name }
        children: [{ type: field_box }]
  second:
    type: form
    name: second-form
    children:
      - type: text_field
        name: second-name
        props: { label: Second name, bind: name }
        children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetFieldDraft(namedSemanticNode(tree, "first-name").ID, "Grace"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(namedSemanticNode(tree, "first-form").ID); err != nil {
		t.Fatal(err)
	}
	if !runtime.SelectScreen("second") {
		t.Fatal("second screen was not selected")
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetFieldDraft(namedSemanticNode(tree, "second-name").ID, "Linus"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SubmitForm(namedSemanticNode(tree, "second-form").ID); err != nil {
		t.Fatal(err)
	}
	values := runtime.Snapshot().StateValues
	if got := values["screen:first"]["name"]; got != "Grace" {
		t.Fatalf("first screen value = %#v", got)
	}
	if got := values["screen:second"]["name"]; got != "Linus" {
		t.Fatalf("second screen value = %#v", got)
	}
	if !runtime.SelectScreen("first") {
		t.Fatal("first screen was not restored")
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "first-name")
	if field.Value != "Grace" || field.CommittedValue != "Grace" {
		t.Fatalf("restored first field = draft %#v committed %#v", field.Value, field.CommittedValue)
	}
}

func TestRuntimeActivatesAndSetsBoundSemanticControls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "controls.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 240, height: 140 }
state:
  enabled: { type: boolean, default: false }
  plan: { type: enum, values: [monthly, annual], default: monthly }
  volume: { type: number, default: 20, min: 0, max: 100, step: 10 }
entry: main
screens:
  main:
    type: stack
    children:
      - type: toggle
        name: enabled-toggle
        props: { label: Enabled, bind: enabled, height: 30 }
        children: [{ type: text, props: { text: Enabled } }]
      - type: radio_group
        name: plan-group
        props: { label: Plan, bind: plan }
        children:
          - type: radio
            name: monthly-radio
            props: { label: Monthly, value: monthly }
            children: [{ type: text, props: { text: Monthly } }]
          - type: radio
            name: annual-radio
            props: { label: Annual, value: annual }
            children: [{ type: text, props: { text: Annual } }]
      - type: slider
        name: volume-slider
        props: { label: Volume, bind: volume, height: 20 }
        children:
          - { type: slider_track, props: { height: 4 } }
          - { type: slider_thumb, props: { width: 12, height: 12 } }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ActivateSemanticID(namedSemanticNode(tree, "enabled-toggle").ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["enabled"]; got != true {
		t.Fatalf("toggle value = %#v", got)
	}
	tree, _ = runtime.RuntimeTree()
	annual := namedSemanticNode(tree, "annual-radio")
	if annual == nil || len(annual.Actions) != 1 || annual.Actions[0].State != "plan" || annual.Actions[0].Value != "annual" {
		t.Fatalf("annual semantics = %+v", annual)
	}
	if err := runtime.ActivateSemanticID(annual.ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("radio value = %#v, annual=%+v", got, annual)
	}
	tree, _ = runtime.RuntimeTree()
	value, err := runtime.SetControlValue(namedSemanticNode(tree, "volume-slider").ID, float64(56))
	if err != nil || value != float64(60) {
		t.Fatalf("set control value = %#v, %v", value, err)
	}
	if _, err := runtime.SetControlValue(namedSemanticNode(tree, "volume-slider").ID, float64(100)); err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot().RuntimeRevision
	if err := runtime.Activate(interaction.Activation{Scope: "screen:main", Actions: []document.Action{{Action: "increment", State: "volume", By: float64(10)}}}); err != nil {
		t.Fatal(err)
	}
	if after := runtime.Snapshot().RuntimeRevision; after != before {
		t.Fatalf("bound no-op changed runtime revision: before=%d after=%d", before, after)
	}
}

func TestNorthstarPointerKeyboardHistoryAndScrollRestoration(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "dashboard", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	router := interaction.NewRouter()
	renderFrame := func() (Snapshot, *semantic.Node) {
		snapshot := runtime.Snapshot()
		tree := render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot)).Tree
		router.Update(tree)
		return snapshot, tree
	}
	snapshot, _ := renderFrame()
	if snapshot.Screen != "overview" {
		t.Fatalf("initial screen = %q", snapshot.Screen)
	}
	if got := router.FocusNext(false); got == "" {
		t.Fatal("overview link did not receive keyboard focus")
	}
	router.FocusNext(false)
	activation, ok := router.KeyDown("Enter")
	if !ok {
		t.Fatal("Enter did not activate revenue link")
	}
	if err := runtime.Activate(activation); err != nil {
		t.Fatal(err)
	}
	snapshot, tree := renderFrame()
	if snapshot.Screen != "revenue" || !snapshot.CanBack {
		t.Fatalf("after keyboard navigation = %+v", snapshot)
	}
	current := namedSemanticNode(tree, "revenue-link")
	if current == nil || !current.Current || current.Props["background"] != "#635BFF" {
		t.Fatalf("current revenue link = %+v", current)
	}
	runtime.SetScrollOffset("revenue-feed", "vertical", 90)
	_, tree = renderFrame()
	reports := namedSemanticNode(tree, "reports-link")
	if reports == nil || reports.Bounds == nil || reports.Clip == nil {
		t.Fatalf("reports link = %+v", reports)
	}
	point := reports.Bounds.ImageRectangle().Intersect(reports.Clip.ImageRectangle()).Min.Add(image.Pt(4, 4))
	if !router.Press(7, point) {
		t.Fatal("reports pointer press was rejected")
	}
	activation, ok = router.Release(7, point)
	if !ok {
		t.Fatal("reports pointer release did not activate")
	}
	if err := runtime.Activate(activation); err != nil {
		t.Fatal(err)
	}
	snapshot, tree = renderFrame()
	if snapshot.Screen != "reports" || namedSemanticNode(tree, "reports-link") == nil || !namedSemanticNode(tree, "reports-link").Current {
		t.Fatalf("after reports navigation = %+v", snapshot)
	}
	back := namedSemanticNode(tree, "history-back")
	if back == nil {
		t.Fatal("missing history back button")
	}
	if err := runtime.Activate(interaction.Activation{Scope: back.Scope, Actions: back.Actions}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = renderFrame()
	if snapshot.Screen != "revenue" || snapshot.Scroll["revenue-feed"].Y != 90 || !snapshot.CanForward {
		t.Fatalf("restored revenue entry = %+v", snapshot)
	}
}

func TestRuntimeRepeatedNavigationAndReloadRetentionIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 120, height: 80 }
entry: first
screens:
  first:
    type: scroll
    name: first-feed
    props: { axis: vertical }
    children: [{ type: spacer, props: { height: 200 } }]
  second:
    type: scroll
    name: second-feed
    props: { axis: vertical }
    children: [{ type: spacer, props: { height: 200 } }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 300; index++ {
		target := "second"
		if runtime.Snapshot().Screen == "second" {
			target = "first"
		}
		runtime.SetScrollOffset(runtime.Snapshot().Screen+"-feed", "vertical", index)
		if err := runtime.Activate(interaction.Activation{Actions: []document.Action{{Action: "navigate", To: target}}}); err != nil {
			t.Fatal(err)
		}
		if index%25 == 0 {
			runtime.Reload()
		}
	}
	if runtime.navigation == nil || runtime.navigation.Len() != 100 {
		t.Fatalf("history length = %d", runtime.navigation.Len())
	}
	if len(runtime.scroll) > 1 || len(runtime.state.AllValues()) != 0 {
		t.Fatalf("unbounded runtime state: scroll=%v state=%v", runtime.scroll, runtime.state.AllValues())
	}
}

func namedSemanticNode(root *semantic.Node, name string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.Name == name {
			return node
		}
	}
	return nil
}

func TestRuntimeNavigationCommitsStateRestoresScrollAndResetsStudioSelection(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`
gora: 1
kind: app
viewport: { width: 400, height: 300 }
state:
  visits: { type: number, default: 0 }
entry: home
screens:
  home:
    type: scroll
    name: home-feed
    props: { axis: vertical }
    children:
      - { type: spacer, props: { height: 800 } }
  reports:
    type: scroll
    name: reports-feed
    props: { axis: vertical }
    children:
      - { type: spacer, props: { height: 800 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.SetScrollOffset("home-feed", "vertical", 40)
	if err := runtime.Activate(interaction.Activation{Scope: "screen:home", Actions: []document.Action{
		{Action: "increment", State: "visits", By: float64(1)},
		{Action: "navigate", To: "reports"},
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.Screen != "reports" || !snapshot.CanBack || snapshot.CanForward || len(snapshot.Scroll) != 0 {
		t.Fatalf("after navigate = %+v", snapshot)
	}
	if snapshot.StateValues["screen:home"]["visits"] != float64(1) {
		t.Fatalf("home state = %+v", snapshot.StateValues["screen:home"])
	}
	runtime.SetScrollOffset("reports-feed", "vertical", 80)
	if err := runtime.Activate(interaction.Activation{Scope: "screen:reports", Actions: []document.Action{{Action: "back"}}}); err != nil {
		t.Fatal(err)
	}
	snapshot = runtime.Snapshot()
	if snapshot.Screen != "home" || snapshot.Scroll["home-feed"].Y != 40 || snapshot.CanBack || !snapshot.CanForward {
		t.Fatalf("after back = %+v", snapshot)
	}

	if !runtime.SelectScreen("reports") {
		t.Fatal("SelectScreen reports failed")
	}
	snapshot = runtime.Snapshot()
	if snapshot.Screen != "reports" || snapshot.CanBack || snapshot.CanForward || len(snapshot.Scroll) != 0 {
		t.Fatalf("Studio selection did not reset navigation = %+v", snapshot)
	}
}

func TestRepeatedButtonActivationAcrossPersistentRebuilds(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var cache render.GioCache
	theme := material.NewTheme()
	router := interaction.NewRouter()
	nextResult := func() (Snapshot, render.GioResult) {
		snapshot := runtime.Snapshot()
		var operations op.Ops
		result := cache.Layout(layout.Context{
			Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(snapshot.Viewport),
		}, theme, snapshot.Root, snapshot.Viewport, renderState(snapshot))
		router.Update(result.Tree)
		return snapshot, result
	}
	activate := func(name string, pointerID int) {
		t.Helper()
		_, result := nextResult()
		var region *semantic.Node
		for _, node := range semantic.Flatten(result.Tree) {
			if node.Name == name && (node.Role == "button" || node.Role == "link") {
				region = node
				break
			}
		}
		if region == nil {
			t.Fatalf("missing interaction region %q", name)
		}
		visible := region.Bounds.ImageRectangle().Intersect(region.Clip.ImageRectangle())
		point := image.Pt((visible.Min.X+visible.Max.X)/2, (visible.Min.Y+visible.Max.Y)/2)
		if !router.Press(pointerID, point) {
			t.Fatalf("press %q was rejected", name)
		}
		activation, ok := router.Release(pointerID, point)
		if !ok {
			t.Fatalf("release %q did not activate", name)
		}
		if err := runtime.Activate(activation); err != nil {
			t.Fatal(err)
		}
	}

	activate("annual-plan", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("annual plan = %#v", got)
	}
	activate("monthly-plan", 2)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "monthly" {
		t.Fatalf("monthly plan = %#v", got)
	}
	activate("increment", 3)
	activate("decrement", 4)
	if got := runtime.Snapshot().StateValues["screen:main/team-seats"]["count"]; got != float64(3) {
		t.Fatalf("stepper count = %#v", got)
	}
	activate("toggle-details", 5)
	activate("toggle-details", 6)
	if got := runtime.Snapshot().StateValues["screen:main"]["details"]; got != false {
		t.Fatalf("details = %#v", got)
	}
}

func TestRuntimeOwnsStateAcrossActivationReloadAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
state:
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: text
    props: { content: { ref: state.count } }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "1" {
		t.Fatalf("initial content = %#v", got)
	}
	if err := runtime.Activate(interaction.Activation{Scope: "screen:main", Actions: []document.Action{{Action: "increment", State: "count", By: float64(2)}}}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("mutated content = %#v", got)
	}
	persistentRoot := runtime.Snapshot().Root
	runtime.SetTransient(interaction.Transient{Focused: "button"})
	if transientRoot := runtime.Snapshot().Root; transientRoot != persistentRoot {
		t.Fatal("transient interaction rebuilt persistent geometry")
	}
	runtime.Reload()
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("reloaded content = %#v", got)
	}
	runtime.ResetState()
	snapshot := runtime.Snapshot()
	if got := snapshot.Root.Props["content"]; got != "1" || snapshot.Transient != (interaction.Transient{}) {
		t.Fatalf("reset snapshot = %#v", snapshot)
	}
}

func TestReloadPreservesLastGoodFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 200 }
entry: main
screens:
  main:
    type: surface
    props: { background: "#112233" }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot()
	if before.Root == nil || before.Invalid {
		t.Fatalf("initial snapshot = %#v", before)
	}

	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	after := runtime.Snapshot()
	if after.Root != before.Root || !after.Invalid || len(after.Diagnostics) == 0 {
		t.Fatalf("last-good state not preserved: %#v", after)
	}
}

func TestNamedScrollPersistsAndUnnamedScrollResets(t *testing.T) {
	tests := []struct {
		name      string
		nodeName  string
		preserved bool
	}{
		{name: "named", nodeName: "feed", preserved: true},
		{name: "unnamed", preserved: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.gora")
			nameLine := ""
			if test.nodeName != "" {
				nameLine = "\n    name: " + test.nodeName
			}
			source := `gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: scroll` + nameLine + `
    props: { axis: vertical }
    children:
      - type: spacer
        props: { height: 400 }
`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(dir, path)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Scroll(30)
			if len(runtime.Snapshot().Scroll) != 1 {
				t.Fatal("scroll offset was not recorded")
			}
			runtime.Reload()
			got := len(runtime.Snapshot().Scroll) == 1
			if got != test.preserved {
				t.Fatalf("preserved=%v, want %v", got, test.preserved)
			}
		})
	}
}

func TestScrollAxisTargetsHorizontalScrollAlongsideVerticalScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: overlay
    children:
      - type: scroll
        name: feed
        props: { axis: vertical }
        children:
          - type: spacer
            props: { height: 400 }
      - type: scroll
        name: rail
        props: { axis: horizontal }
        children:
          - type: spacer
            props: { width: 400 }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}

	runtime.ScrollAxis("horizontal", 30)

	scroll := runtime.Snapshot().Scroll
	if got := scroll["rail"].X; got != 30 {
		t.Fatalf("horizontal offset = %d", got)
	}
	if _, ok := scroll["feed"]; ok {
		t.Fatal("horizontal gesture moved the vertical scroll node")
	}
}

func TestSetScrollOffsetSupportsDraggableScrollbar(t *testing.T) {
	runtime := &Runtime{scroll: make(map[string]image.Point)}
	runtime.SetScrollOffset("feed", "vertical", 95)
	runtime.SetScrollOffset("gallery", "horizontal", 42)
	snapshot := runtime.Snapshot()
	if got := snapshot.Scroll["feed"].Y; got != 95 {
		t.Fatalf("vertical offset = %d", got)
	}
	if got := snapshot.Scroll["gallery"].X; got != 42 {
		t.Fatalf("horizontal offset = %d", got)
	}
}

func TestCaptureUsesViewportScaleAndWarnsForLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 20, height: 10 }
entry: main
screens:
  main: { type: surface, props: { background: "#112233" } }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	output := filepath.Join(dir, "capture.png")
	warning, err := runtime.Capture(output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if warning == "" {
		t.Fatal("missing last-good warning")
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Width != 40 || captured.Height != 20 {
		t.Fatalf("capture size = %dx%d", captured.Width, captured.Height)
	}
}
