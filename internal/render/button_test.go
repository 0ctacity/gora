package render

import (
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/project"
)

func TestButtonUsesSurfaceLayoutAndRecordsSemanticRegion(t *testing.T) {
	root := &project.Node{
		Handle: "save", Type: "button", Name: "save-button", Scope: "screen:main",
		Props: map[string]any{
			"label": "Save", "padding": map[string]any{"top": float64(8), "right": float64(12), "bottom": float64(8), "left": float64(12)},
			"background": "#172033", "disabled": false,
		},
		On:       document.Events{Activate: []document.Action{{Action: "toggle", State: "saved"}}},
		Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"content": "Save"}}},
	}
	result := Render(root, image.Pt(180, 48), State{})
	if len(result.Interactions) != 1 {
		t.Fatalf("interactions = %+v", result.Interactions)
	}
	region := result.Interactions[0]
	if region.Handle != "save" || region.Scope != "screen:main" || region.Label != "Save" || region.Disabled || len(region.Actions) != 1 {
		t.Fatalf("region = %+v", region)
	}
	if result.Bounds["label"].Min != image.Pt(12, 8) {
		t.Fatalf("label bounds = %v", result.Bounds["label"])
	}
}

func TestUnsizedButtonUsesChildAndPaddingIntrinsicSize(t *testing.T) {
	button := &project.Node{
		Handle: "button", Type: "button",
		Props:    map[string]any{"label": "Monthly", "padding": map[string]any{"top": float64(10), "right": float64(18), "bottom": float64(10), "left": float64(18)}},
		Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"content": "Monthly", "size": float64(16)}}},
	}
	if size := cpuIntrinsicSize(button, image.Pt(300, 80)); size.X <= 36 || size.Y <= 20 {
		t.Fatalf("intrinsic button size = %v", size)
	}
}
