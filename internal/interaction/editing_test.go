package interaction

import (
	"fmt"
	"testing"

	"gora/internal/document"
)

func TestEditingStoreValidatesGraphemesAndCommitsText(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "screen:main/name", Scope: "screen:main", Binding: "name", Type: "text", Value: "Ada",
		Required: true, MinLength: intPointer(2), MaxLength: intPointer(3),
	}})

	if err := store.SetDraft("screen:main/name", "e\u0301"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("screen:main/name")
	if state.Valid || len(state.Issues) != 1 || state.Issues[0].Code != "min_length" {
		t.Fatalf("one grapheme validation = %+v", state)
	}
	if err := store.SetDraft("screen:main/name", "Ava"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Commit("screen:main/name")
	if err != nil || value != "Ava" {
		t.Fatalf("commit = %#v, %v", value, err)
	}
}

func TestEditingStoreMatchesExampleEmailPattern(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "email", Scope: "screen:main", Binding: "email", Type: "text", Value: "ada@example.com",
		Pattern: `[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+`, HasPattern: true,
	}})
	state, _ := store.State("email")
	if !state.Valid || len(state.Issues) != 0 {
		t.Fatalf("example email pattern rejected default value: %+v", state)
	}
}

func TestEditingStoreKeepsInvalidNumberDraftOutOfState(t *testing.T) {
	minimum, maximum, step := float64(1), float64(20), float64(2)
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "seats", Scope: "screen:main", Binding: "seats", Type: "number", Value: float64(3),
		Declaration: document.StateDeclaration{Type: "number", Min: &minimum, Max: &maximum, Step: &step},
	}})
	if err := store.SetDraft("seats", "not a number"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit("seats"); err == nil {
		t.Fatal("invalid number draft committed")
	}
	state, _ := store.State("seats")
	if state.Committed != float64(3) || state.Valid {
		t.Fatalf("invalid number state = %+v", state)
	}
	if err := store.SetDraft("seats", "18"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Commit("seats")
	if err != nil || value != float64(19) {
		t.Fatalf("normalized commit = %#v, %v; want 19", value, err)
	}
}

func TestEditingStoreReconcilePreservesCompatibleDraftAndReplacesExternalValue(t *testing.T) {
	store := NewEditingStore()
	spec := FieldSpec{ID: "field", Scope: "screen:main", Binding: "name", Type: "text", Value: "Ada"}
	store.Reconcile([]FieldSpec{spec})
	if err := store.SetDraft("field", "draft"); err != nil {
		t.Fatal(err)
	}
	store.Reconcile([]FieldSpec{spec})
	state, _ := store.State("field")
	if state.Draft != "draft" {
		t.Fatalf("compatible draft = %q", state.Draft)
	}
	if err := store.ReplaceCommitted("field", "Grace"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.State("field")
	if state.Draft != "Grace" || state.Committed != "Grace" || state.Composing {
		t.Fatalf("external replacement = %+v", state)
	}
}

func TestEditingStoreUndoHistoryIsBounded(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen:main", Binding: "name", Type: "text", Value: ""}})
	for index := 0; index < 120; index++ {
		if err := store.SetDraft("field", fmt.Sprint(index)); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := store.State("field")
	if got := len(state.Undo); got != 100 {
		t.Fatalf("undo history = %d, want 100", got)
	}
}

func TestEditingStoreTreatsEmojiZWJSequenceAsOneGrapheme(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen:main", Binding: "value", Type: "text", Value: ""}})
	if err := store.SetDraft("field", "👩‍👩‍👧‍👦"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("field")
	if state.SelectionStart != 1 || state.SelectionEnd != 1 {
		t.Fatalf("family emoji selection = %d..%d, want 1..1", state.SelectionStart, state.SelectionEnd)
	}
}

func TestEditingStoreUsesUnicodeExtendedGraphemeBoundaries(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "crlf", text: "\r\n"},
		{name: "hangul-jamo", text: "\u1100\u1161\u11a8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := graphemeCount(test.text); got != 1 {
				t.Fatalf("graphemeCount(%q) = %d, want 1", test.text, got)
			}
			if got := runeOffsetForGrapheme(test.text, 1); got != len([]rune(test.text)) {
				t.Fatalf("runeOffsetForGrapheme(%q, 1) = %d, want %d", test.text, got, len([]rune(test.text)))
			}
		})
	}
}

func TestEditingStoreKeyboardMovementAndDeletionUseTextBoundaries(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "one two\nxy", Declaration: document.StateDeclaration{Type: "text"}}})

	if !store.MoveSelection("field", "word-left", false) {
		t.Fatal("word-left should move the caret")
	}
	start, end, _ := store.RuneSelection("field")
	if start != 8 || end != 8 {
		t.Fatalf("word-left selection = %d..%d, want 8..8", start, end)
	}
	if !store.MoveSelection("field", "line-up", false) {
		t.Fatal("line-up should preserve the visual column")
	}
	start, end, _ = store.RuneSelection("field")
	if start != 0 || end != 0 {
		t.Fatalf("line-up selection = %d..%d, want 0..0", start, end)
	}
	if !store.MoveSelection("field", "grapheme-right", true) {
		t.Fatal("shift-right should extend the selection")
	}
	start, end, _ = store.RuneSelection("field")
	if start != 0 || end != 1 {
		t.Fatalf("extended selection = %d..%d, want 0..1", start, end)
	}
	if !store.DeleteSelection("field", true, false) {
		t.Fatal("backspace should delete the selection")
	}
	if draft, _ := store.Draft("field"); draft != "ne two\nxy" {
		t.Fatalf("draft after selection deletion = %q", draft)
	}

	_ = store.SetRuneSelection("field", 2, 2)
	if !store.DeleteSelection("field", true, true) {
		t.Fatal("word-backspace should delete the preceding word")
	}
	if draft, _ := store.Draft("field"); draft != " two\nxy" {
		t.Fatalf("draft after word deletion = %q", draft)
	}
}

func TestEditingStoreTouchAndNoOpSelectionRevision(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "Ada", Declaration: document.StateDeclaration{Type: "text"}}})
	revision := store.Revision()
	if err := store.SetRuneSelection("field", 3, 3); err != nil {
		t.Fatal(err)
	}
	if store.Revision() != revision {
		t.Fatal("an unchanged selection must not increment the revision")
	}
	if !store.Touch("field") {
		t.Fatal("touch should be observable the first time")
	}
	if store.Touch("field") {
		t.Fatal("touch should be a no-op after the field is touched")
	}
}

func TestEditingStoreEmptyReplacementWithoutSelectionIsNoOp(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "Ada", Declaration: document.StateDeclaration{Type: "text"}}})
	if err := store.SetRuneSelection("field", 1, 1); err != nil {
		t.Fatal(err)
	}
	before := store.Revision()
	if err := store.ApplyRuneEdit("field", 1, 1, ""); err != nil {
		t.Fatal(err)
	}
	if got := store.Revision(); got != before {
		t.Fatalf("empty replacement revision = %d, want %d", got, before)
	}
	state, _ := store.State("field")
	if len(state.Undo) != 0 || state.Draft != "Ada" {
		t.Fatalf("empty replacement changed state: draft=%q undo=%d", state.Draft, len(state.Undo))
	}
}

func TestEditingStoreSupportsPlainTextClipboardRanges(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "Ada Lovelace",
		Declaration: document.StateDeclaration{Type: "text"},
	}})

	if err := store.SetRuneSelection("field", 0, 3); err != nil {
		t.Fatal(err)
	}
	selected, ok := store.SelectedText("field")
	if !ok || selected != "Ada" {
		t.Fatalf("copied text = %q, %v; want Ada, true", selected, ok)
	}

	start, end, ok := store.RuneSelection("field")
	if !ok {
		t.Fatal("selected field has no rune range")
	}
	if err := store.ApplyRuneEdit("field", start, end, ""); err != nil {
		t.Fatal(err)
	}
	if draft, _ := store.Draft("field"); draft != " Lovelace" {
		t.Fatalf("draft after cut = %q, want %q", draft, " Lovelace")
	}

	if err := store.SetRuneSelection("field", 9, 9); err != nil {
		t.Fatal(err)
	}
	start, end, _ = store.RuneSelection("field")
	if err := store.ApplyRuneEdit("field", start, end, selected); err != nil {
		t.Fatal(err)
	}
	if draft, _ := store.Draft("field"); draft != " LovelaceAda" {
		t.Fatalf("draft after paste = %q, want %q", draft, " LovelaceAda")
	}
}

func TestEditingStoreVerticalMovementPreservesPreferredColumn(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "12345\nx\n12345", Declaration: document.StateDeclaration{Type: "text"}}})
	if err := store.SetRuneSelection("field", 5, 5); err != nil {
		t.Fatal(err)
	}
	if !store.MoveSelection("field", "line-down", false) {
		t.Fatal("first line-down did not move")
	}
	start, end, _ := store.RuneSelection("field")
	if start != 7 || end != 7 {
		t.Fatalf("short-line caret = %d..%d, want 7..7", start, end)
	}
	if !store.MoveSelection("field", "line-down", false) {
		t.Fatal("second line-down did not move")
	}
	start, end, _ = store.RuneSelection("field")
	if start != 13 || end != 13 {
		t.Fatalf("restored preferred-column caret = %d..%d, want 13..13", start, end)
	}
}

func TestEditingStoreMovementUsesWrappedVisualLines(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Multiline: true, Value: "abcdef", Declaration: document.StateDeclaration{Type: "text"}}})
	if !store.SetVisualColumns("field", 3) {
		t.Fatal("visual columns were not installed")
	}
	_ = store.SetRuneSelection("field", 2, 2)
	if !store.MoveSelection("field", "line-down", false) {
		t.Fatal("line-down did not move to the wrapped visual line")
	}
	start, end, _ := store.RuneSelection("field")
	if start != 5 || end != 5 {
		t.Fatalf("wrapped line-down = %d..%d, want 5..5", start, end)
	}
	if !store.MoveSelection("field", "line-start", false) {
		t.Fatal("line-start did not move")
	}
	start, end, _ = store.RuneSelection("field")
	if start != 3 || end != 3 {
		t.Fatalf("wrapped line-start = %d..%d, want 3..3", start, end)
	}
	if !store.MoveSelection("field", "line-end", false) {
		t.Fatal("line-end did not move")
	}
	start, end, _ = store.RuneSelection("field")
	if start != 6 || end != 6 {
		t.Fatalf("wrapped line-end = %d..%d, want 6..6", start, end)
	}
}

func TestEditingStoreScrollsOverflowingTextAreaWithinItsLineBounds(t *testing.T) {
	maxLines := 2
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "notes", Scope: "screen", Binding: "notes", Type: "text", Multiline: true,
		Value: "12345\n67890\nabcde", MaxLines: &maxLines, Declaration: document.StateDeclaration{Type: "text"},
	}})
	store.SetVisualColumns("notes", 5)
	if !store.ScrollInternal("notes", -100) {
		t.Fatal("initial caret reveal could not be scrolled back to the top")
	}
	if !store.ScrollInternal("notes", 1) {
		t.Fatal("overflowing text area did not consume downward scrolling")
	}
	state, _ := store.State("notes")
	if state.InternalOffset != 1 || !state.ManualScroll {
		t.Fatalf("internal scroll state offset=%v manual=%v", state.InternalOffset, state.ManualScroll)
	}
	if store.ScrollInternal("notes", 1) {
		t.Fatal("text area consumed scrolling beyond its lower boundary")
	}
	if !store.ScrollInternal("notes", -1) {
		t.Fatal("text area did not consume upward scrolling")
	}
	state, _ = store.State("notes")
	if state.InternalOffset != 0 {
		t.Fatalf("restored internal offset = %v, want 0", state.InternalOffset)
	}
}

func TestEditingStoreCaretMovementReclaimsManualTextAreaScroll(t *testing.T) {
	maxLines := 2
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{
		ID: "notes", Scope: "screen", Binding: "notes", Type: "text", Multiline: true,
		Value: "12345\n67890\nabcde", MaxLines: &maxLines, Declaration: document.StateDeclaration{Type: "text"},
	}})
	store.SetVisualColumns("notes", 5)
	if !store.ScrollInternal("notes", -1) {
		t.Fatal("text area did not accept manual scrolling away from its caret")
	}
	if err := store.SetRuneSelection("notes", 13, 13); err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("notes")
	if state.ManualScroll {
		t.Fatal("explicit caret movement left manual-scroll ownership active")
	}
	if state.InternalOffset != 1 {
		t.Fatalf("caret reveal offset = %v, want 1", state.InternalOffset)
	}
}

func TestEditingStoreSingleLineFieldsRemoveLineBreaks(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "", Declaration: document.StateDeclaration{Type: "text"}}})
	if err := store.SetDraft("field", "one\r\ntwo\nthree"); err != nil {
		t.Fatal(err)
	}
	if draft, _ := store.Draft("field"); draft != "onetwothree" {
		t.Fatalf("single-line draft = %q, want onetwothree", draft)
	}
}

func TestEditingStoreGroupsAndCancelsIMEComposition(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "cafe", Declaration: document.StateDeclaration{Type: "text"}}})
	if err := store.SetComposition("field", 3, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyRuneEdit("field", 3, 4, "é"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetComposition("field", 3, 5); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyRuneEdit("field", 3, 5, "é"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetComposition("field", 0, 0); err != nil {
		t.Fatal(err)
	}
	if !store.Undo("field") {
		t.Fatal("composed edit did not create one undo entry")
	}
	if draft, _ := store.Draft("field"); draft != "cafe" {
		t.Fatalf("undo composition = %q, want cafe", draft)
	}

	if err := store.SetComposition("field", 3, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyRuneEdit("field", 3, 4, "é"); err != nil {
		t.Fatal(err)
	}
	if !store.CancelComposition("field") {
		t.Fatal("composition cancellation was a no-op")
	}
	if draft, _ := store.Draft("field"); draft != "cafe" {
		t.Fatalf("cancel composition = %q, want cafe", draft)
	}
}

func TestEditingStoreDeadKeyLikeEditBeforeCompositionIsAppliedOnce(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "a", Declaration: document.StateDeclaration{Type: "text"}}})
	if err := store.ApplyRuneEdit("field", 1, 1, "́"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetComposition("field", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.SetComposition("field", 0, 0); err != nil {
		t.Fatal(err)
	}
	if draft, _ := store.Draft("field"); draft != "á" {
		t.Fatalf("dead-key-like draft = %q", draft)
	}
	if !store.Undo("field") {
		t.Fatal("dead-key-like edit had no undo entry")
	}
	if draft, _ := store.Draft("field"); draft != "a" {
		t.Fatalf("undo dead-key-like edit = %q", draft)
	}
	if store.Undo("field") {
		t.Fatal("dead-key-like edit was recorded twice")
	}
}

func TestEditingStoreExternalWritesAndCommitsClearCompleteCompositionState(t *testing.T) {
	store := NewEditingStore()
	store.Reconcile([]FieldSpec{{ID: "field", Scope: "screen", Binding: "value", Type: "text", Value: "cafe", Declaration: document.StateDeclaration{Type: "text"}}})
	startComposition := func() {
		t.Helper()
		if err := store.SetComposition("field", 3, 4); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyRuneEdit("field", 3, 4, "é"); err != nil {
			t.Fatal(err)
		}
	}
	assertCleared := func(operation string) {
		t.Helper()
		state := store.fields["field"]
		if state.Composing || state.Composition != "" || state.CompositionStart != 0 || state.CompositionEnd != 0 || state.compositionBase != nil {
			t.Fatalf("%s left composition state: %+v base=%+v", operation, state, state.compositionBase)
		}
	}

	startComposition()
	if err := store.ReplaceCommitted("field", "external"); err != nil {
		t.Fatal(err)
	}
	assertCleared("external write")

	if err := store.SetDraft("field", "cafe"); err != nil {
		t.Fatal(err)
	}
	startComposition()
	if _, err := store.PrepareCommit("field"); err != nil {
		t.Fatal(err)
	}
	assertCleared("form prepare")
}

func intPointer(value int) *int { return &value }
