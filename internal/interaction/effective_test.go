package interaction

import (
	"testing"

	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/semantic"
)

func TestEffectiveTreeResolvesStateTextAndVariantPrecedence(t *testing.T) {
	visible := true
	root := &project.Node{
		Handle: "root", Type: "text", Scope: "screen:main",
		Props: map[string]any{
			"content": project.StateReference{Scope: "screen:main", Name: "count"},
			"opacity": float64(1),
		},
		Variants: []document.Variant{
			{When: document.Condition{State: "count", Operator: "greater_than_or_equal", Value: float64(2)}, Props: map[string]any{"opacity": float64(0.8)}},
			{When: document.Condition{State: "count", Operator: "equals", Value: float64(3)}, Props: map[string]any{"opacity": float64(0.6)}, Visible: &visible},
		},
	}

	effective := ResolveTree(root, map[string]map[string]any{"screen:main": {"count": float64(3)}}, Transient{})
	if effective.Props["content"] != "3" || effective.Props["opacity"] != float64(0.6) {
		t.Fatalf("effective props = %#v", effective.Props)
	}
	if root.Props["content"] == "3" {
		t.Fatal("ResolveTree mutated the authored resolved node")
	}
}

func TestEffectiveFieldBoxConvertsGraphemeRangesToRuneOffsetsForPainting(t *testing.T) {
	field := &project.Node{
		Handle: "field", Type: "text_area", Name: "bio-field", Scope: "screen:main",
		Children: []*project.Node{{Handle: "box", Type: "field_box", Props: map[string]any{}}},
	}
	draft := "A👨‍👩‍👧‍👦éZ"
	fields := map[string]EditingState{
		semantic.StableID(field, "main"): {
			Draft: draft, SelectionStart: 1, SelectionEnd: 3,
			Composing: true, Composition: "é", CompositionStart: 2, CompositionEnd: 3,
		},
	}

	effective := ResolveTreeWithFields(field, nil, Transient{}, fields, "main")
	box := effective.Children[0]
	if effective.Props["selection_start"] != 1 || effective.Props["selection_end"] != 3 {
		t.Fatalf("semantic grapheme range = %v..%v", effective.Props["selection_start"], effective.Props["selection_end"])
	}
	if box.Props["selection_start"] != 1 || box.Props["selection_end"] != 10 {
		t.Fatalf("paint rune selection = %v..%v, want 1..10", box.Props["selection_start"], box.Props["selection_end"])
	}
	if box.Props["composition_start"] != 8 || box.Props["composition_end"] != 10 {
		t.Fatalf("paint rune composition = %v..%v, want 8..10", box.Props["composition_start"], box.Props["composition_end"])
	}
}

func TestEffectiveFieldSupportUsesInheritedValidationState(t *testing.T) {
	hide := false
	show := true
	field := &project.Node{
		Handle: "email", Type: "text_field", Name: "email-field", Scope: "screen:main", Binding: "email",
		Props: map[string]any{"label": "Email"},
		Children: []*project.Node{
			{Handle: "box", Type: "field_box", Props: map[string]any{}},
			{
				Handle: "support", Type: "field_support", Props: map[string]any{},
				Variants: []document.Variant{
					{When: document.Condition{Interaction: "valid"}, Visible: &hide},
					{When: document.Condition{Interaction: "invalid"}, Props: map[string]any{"validation_state": "invalid"}, Visible: &show},
				},
				Children: []*project.Node{{Handle: "message", Type: "text", Props: map[string]any{"text": "Invalid"}}},
			},
		},
	}
	id := semantic.StableID(field, "main")
	effective := ResolvePersistentTreeWithFields(field, map[string]map[string]any{"screen:main": {"email": "ada@example.com"}}, map[string]EditingState{
		id: {Draft: "ada@example.com", Committed: "ada@example.com", Valid: true, Validated: true},
	}, "main")
	if !effective.Children[1].Hidden {
		t.Fatalf("valid field support remained visible: %+v", effective.Children[1])
	}

	unvalidated := ResolvePersistentTreeWithFields(field, map[string]map[string]any{"screen:main": {"email": ""}}, map[string]EditingState{
		id: {Draft: "", Committed: "", Valid: false, Validated: false},
	}, "main")
	if _, ok := unvalidated.Children[1].Props["validation_state"]; ok {
		t.Fatalf("unvalidated field matched invalid variant: %+v", unvalidated.Children[1].Props)
	}
}

func TestEffectiveTextAreaPassesLineLimitsToItsFieldBox(t *testing.T) {
	field := &project.Node{
		Handle: "field", Type: "text_area", Name: "bio-field", Scope: "screen:main",
		Props:    map[string]any{"min_lines": float64(3), "max_lines": float64(8)},
		Children: []*project.Node{{Handle: "box", Type: "field_box", Props: map[string]any{}}},
	}
	effective := ResolveTreeWithFields(field, nil, Transient{}, map[string]EditingState{
		semantic.StableID(field, "main"): {Draft: "Biography"},
	}, "main")
	props := effective.Children[0].Props
	if props["field_min_lines"] != float64(3) || props["field_max_lines"] != float64(8) {
		t.Fatalf("field box line limits = %#v", props)
	}
}

func TestEffectiveTreeAppliesButtonInteractionVariantsAndVisibility(t *testing.T) {
	hidden := false
	root := &project.Node{
		Handle: "save", Type: "button", Scope: "screen:main",
		Props: map[string]any{"background": "#000000"},
		Variants: []document.Variant{
			{When: document.Condition{Interaction: "hovered"}, Props: map[string]any{"background": "#111111"}},
			{When: document.Condition{Interaction: "focused"}, Props: map[string]any{"background": "#222222"}},
			{When: document.Condition{State: "shown", Operator: "equals", Value: false}, Visible: &hidden},
		},
	}
	if got := ResolveTree(root, map[string]map[string]any{"screen:main": {"shown": false}}, Transient{Hovered: "save", Focused: "save"}); got == nil || !got.Hidden {
		t.Fatalf("hidden node = %#v", got)
	}
	root.Variants = root.Variants[:2]
	effective := ResolveTree(root, nil, Transient{Hovered: "save", Focused: "save"})
	if effective.Props["background"] != "#222222" {
		t.Fatalf("background = %#v", effective.Props["background"])
	}
}

func TestEffectiveTreeDerivesBoundControlStateAndTabPanelVisibility(t *testing.T) {
	root := &project.Node{
		Handle: "tabs", Type: "tabs", Name: "plan-tabs", Scope: "screen:main", Binding: "plan",
		Children: []*project.Node{
			{Handle: "monthly", Type: "tab", Name: "monthly-tab", Scope: "screen:main", Props: map[string]any{"value": "monthly"}},
			{Handle: "annual", Type: "tab", Name: "annual-tab", Scope: "screen:main", Props: map[string]any{"value": "annual"}},
			{Handle: "monthly-panel", Type: "tab_panel", Scope: "screen:main", Props: map[string]any{"value": "monthly"}},
			{Handle: "annual-panel", Type: "tab_panel", Scope: "screen:main", Props: map[string]any{"value": "annual"}},
		},
	}
	effective := ResolvePersistentTree(root, map[string]map[string]any{"screen:main": {"plan": "annual"}})
	if effective.Props["value"] != "annual" || effective.Children[0].Props["selected"] != false || effective.Children[1].Props["selected"] != true {
		t.Fatalf("tab state = %#v / %#v / %#v", effective.Props, effective.Children[0].Props, effective.Children[1].Props)
	}
	if !effective.Children[2].Hidden || effective.Children[3].Hidden {
		t.Fatalf("panel visibility = %v / %v", effective.Children[2].Hidden, effective.Children[3].Hidden)
	}

	toggle := &project.Node{Handle: "toggle", Type: "toggle", Scope: "screen:main", Binding: "enabled", Props: map[string]any{}}
	checked := ResolvePersistentTree(toggle, map[string]map[string]any{"screen:main": {"enabled": true}})
	if checked.Props["checked"] != true {
		t.Fatalf("toggle props = %#v", checked.Props)
	}
}

func TestEffectiveTreeShowsOnlyTheOpenSelectPopupAndActiveOption(t *testing.T) {
	root := &project.Node{Handle: "select", Type: "select", Scope: "screen:main", Binding: "team", Children: []*project.Node{
		{Handle: "trigger", Type: "select_trigger", Scope: "screen:main"},
		{Handle: "popup", Type: "select_popup", Scope: "screen:main", Children: []*project.Node{
			{Handle: "design", Type: "option", Scope: "screen:main", Props: map[string]any{"value": "design"}},
			{Handle: "engineering", Type: "option", Scope: "screen:main", Props: map[string]any{"value": "engineering"}},
		}},
	}}
	closed := ResolveTree(root, map[string]map[string]any{"screen:main": {"team": "design"}}, Transient{})
	if !closed.Children[1].Hidden {
		t.Fatal("closed select popup is visible")
	}
	open := ResolveTree(root, map[string]map[string]any{"screen:main": {"team": "design"}}, Transient{OpenSelect: "select", ActiveOption: "engineering"})
	if open.Children[1].Hidden || open.Props["open"] != true || open.Children[1].Children[1].Props["active"] != true {
		t.Fatalf("open select = %+v", open)
	}
}
