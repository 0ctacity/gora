package interaction

import (
	"testing"

	"gora/internal/document"
	"gora/internal/project"
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
	if got := ResolveTree(root, map[string]map[string]any{"screen:main": {"shown": false}}, Transient{Hovered: "save", Focused: "save"}); got != nil {
		t.Fatalf("hidden node = %#v", got)
	}
	root.Variants = root.Variants[:2]
	effective := ResolveTree(root, nil, Transient{Hovered: "save", Focused: "save"})
	if effective.Props["background"] != "#222222" {
		t.Fatalf("background = %#v", effective.Props["background"])
	}
}
