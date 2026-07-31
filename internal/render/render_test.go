package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/project"
)

func TestRenderStackRecordsBoundsAndPaintsSurface(t *testing.T) {
	root := &project.Node{
		Handle: "root",
		Type:   "stack",
		Props:  map[string]any{"direction": "vertical", "gap": int64(4), "padding": map[string]any{"top": int64(2), "right": int64(2), "bottom": int64(2), "left": int64(2)}},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"height": int64(10), "background": "#FF0000"}},
			{Handle: "b", Type: "surface", Props: map[string]any{"height": int64(10), "background": "#00FF00"}},
		},
	}

	result := Render(root, image.Pt(40, 30), State{})
	if got := result.Bounds["a"]; got != image.Rect(2, 2, 38, 12) {
		t.Fatalf("first child bounds = %v", got)
	}
	if got := result.Bounds["b"]; got != image.Rect(2, 16, 38, 26) {
		t.Fatalf("second child bounds = %v", got)
	}
	if got := result.Image.RGBAAt(3, 3); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("surface pixel = %#v", got)
	}
}

func TestCaptureRefusesExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Capture(path, &project.Node{Handle: "root", Type: "spacer"}, image.Pt(8, 8), State{}, 1)
	if err == nil {
		t.Fatal("Capture unexpectedly overwrote an existing file")
	}
}

func TestGridHonorsFractionalTracksAndSpans(t *testing.T) {
	root := &project.Node{
		Handle: "grid", Type: "grid",
		Props: map[string]any{"columns": []any{"1fr", "2fr"}, "gap": int64(3)},
		Children: []*project.Node{
			{Handle: "first", Type: "spacer"},
			{Handle: "span", Type: "spacer", Place: map[string]any{"column": int64(0), "row": int64(1), "column_span": int64(2)}},
		},
	}
	result := Render(root, image.Pt(93, 40), State{})
	if got := result.Bounds["first"]; got != image.Rect(0, 0, 30, 19) {
		t.Fatalf("first track bounds = %v", got)
	}
	if got := result.Bounds["span"]; got != image.Rect(0, 22, 93, 40) {
		t.Fatalf("spanning bounds = %v", got)
	}
}

func TestStackDistributesRemainingSpaceByGrowWeight(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "fixed", Type: "spacer", Props: map[string]any{"width": int64(20)}},
			{Handle: "one", Type: "spacer", Place: map[string]any{"grow": int64(1)}},
			{Handle: "two", Type: "spacer", Place: map[string]any{"grow": int64(2)}},
		},
	}
	result := Render(root, image.Pt(80, 10), State{})
	if got := result.Bounds["one"].Dx(); got != 20 {
		t.Fatalf("grow=1 width = %d", got)
	}
	if got := result.Bounds["two"].Dx(); got != 40 {
		t.Fatalf("grow=2 width = %d", got)
	}
}

func TestStackResolvesPercentageAndAspectRatio(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "media", Type: "surface",
			Props: map[string]any{
				"width":        map[string]any{"percent": float64(50)},
				"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)},
			},
		}},
	}

	result := Render(root, image.Pt(200, 120), State{})
	if got := result.Bounds["media"]; got != image.Rect(0, 0, 100, 50) {
		t.Fatalf("percentage ratio bounds = %v, want (0,0)-(100,50)", got)
	}
}

func TestStackWrapsWithDirectionalGaps(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{
			"direction": "horizontal", "wrap": true,
			"gap": int64(1), "column_gap": int64(10), "row_gap": int64(5),
		},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
			{Handle: "b", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
			{Handle: "c", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
		},
	}

	result := Render(root, image.Pt(100, 60), State{})
	if got := result.Bounds["b"]; got != image.Rect(55, 0, 100, 20) {
		t.Fatalf("second wrapped item = %v", got)
	}
	if got := result.Bounds["c"]; got != image.Rect(0, 25, 45, 45) {
		t.Fatalf("third wrapped item = %v", got)
	}
}

func TestStackShrinksProportionallyAndFreezesAtMinimum(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"min_width": int64(55)}, Place: map[string]any{"basis": int64(60), "shrink": int64(1)}},
			{Handle: "b", Type: "surface", Place: map[string]any{"basis": int64(60), "shrink": int64(1)}},
		},
	}

	result := Render(root, image.Pt(100, 20), State{})
	if got := result.Bounds["a"].Dx(); got != 55 {
		t.Fatalf("minimum-frozen width = %d, want 55", got)
	}
	if got := result.Bounds["b"].Dx(); got != 45 {
		t.Fatalf("remaining shrink width = %d, want 45", got)
	}
}

func TestStackRedistributesGrowthAfterMaximumFreeze(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Place: map[string]any{"basis": int64(20), "grow": int64(1)}},
			{Handle: "b", Type: "surface", Props: map[string]any{"max_width": int64(30)}, Place: map[string]any{"basis": int64(20), "grow": int64(1)}},
		},
	}
	result := Render(root, image.Pt(100, 20), State{})
	if got := result.Bounds["a"].Dx(); got != 70 {
		t.Fatalf("redistributed grow width = %d, want 70", got)
	}
	if got := result.Bounds["b"].Dx(); got != 30 {
		t.Fatalf("maximum-frozen width = %d, want 30", got)
	}
}

func TestStackChildAlignmentOverridesContainer(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "child", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(10)},
			Place: map[string]any{"alignment": "end"},
		}},
	}

	result := Render(root, image.Pt(80, 40), State{})
	if got := result.Bounds["child"]; got != image.Rect(0, 30, 20, 40) {
		t.Fatalf("child alignment bounds = %v", got)
	}
}

func TestStackMeasuresNestedAutoSurface(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "card", Type: "surface",
			Props:    map[string]any{"padding": map[string]any{"top": int64(5), "right": int64(5), "bottom": int64(5), "left": int64(5)}},
			Children: []*project.Node{{Handle: "content", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}}},
		}},
	}

	result := Render(root, image.Pt(100, 50), State{})
	if got := result.Bounds["card"]; got != image.Rect(0, 0, 40, 20) {
		t.Fatalf("nested auto surface bounds = %v", got)
	}
}

func TestStackPercentageAboveHundredOverflows(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "overflow", Type: "spacer",
			Props: map[string]any{"width": map[string]any{"percent": int64(125)}, "height": int64(10)},
		}},
	}
	result := Render(root, image.Pt(80, 20), State{})
	if got := result.Bounds["overflow"]; got != image.Rect(0, 0, 100, 10) {
		t.Fatalf("overflow percentage bounds = %v", got)
	}
}

func TestVerticalStackWrapsIntoColumns(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "vertical", "wrap": true, "row_gap": int64(10), "column_gap": int64(5)},
		Children: []*project.Node{
			{Handle: "a", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
			{Handle: "b", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
			{Handle: "c", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
		},
	}
	result := Render(root, image.Pt(60, 50), State{})
	if got := result.Bounds["c"]; got != image.Rect(20, 0, 35, 20) {
		t.Fatalf("third vertical item = %v", got)
	}
}

func TestStackBoundaryFitAndOneUnitOverflow(t *testing.T) {
	makeRoot := func() *project.Node {
		return &project.Node{
			Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "wrap": true, "gap": int64(5)},
			Children: []*project.Node{
				{Handle: "a", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
				{Handle: "b", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
				{Handle: "c", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
			},
		}
	}
	if got := Render(makeRoot(), image.Pt(100, 30), State{}).Bounds["c"].Min.Y; got != 0 {
		t.Fatalf("boundary-fit item y = %d, want 0", got)
	}
	if got := Render(makeRoot(), image.Pt(99, 30), State{}).Bounds["c"].Min.Y; got != 15 {
		t.Fatalf("one-unit-overflow item y = %d, want 15", got)
	}
}

func TestScrollUsesIntrinsicContentExtentAndClampsOffset(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "content", Type: "stack", Props: map[string]any{"direction": "vertical"},
			Children: []*project.Node{
				{Handle: "a", Type: "spacer", Props: map[string]any{"height": int64(50)}},
				{Handle: "b", Type: "spacer", Props: map[string]any{"height": int64(50)}},
				{Handle: "c", Type: "spacer", Props: map[string]any{"height": int64(50)}},
			},
		}},
	}
	result := Render(root, image.Pt(100, 100), State{Scroll: map[string]image.Point{"feed": image.Pt(0, 100)}})
	if got := result.Bounds["content"]; got != image.Rect(0, -50, 100, 100) {
		t.Fatalf("intrinsic scrolled content bounds = %v", got)
	}
}

func TestCaptureScalesPixelsWithoutStudioOverlays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	root := &project.Node{Handle: "root", Type: "surface", Props: map[string]any{"background": "#123456"}}
	if err := Capture(path, root, image.Pt(3, 2), State{}, 2); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Bounds().Size() != image.Pt(6, 4) {
		t.Fatalf("capture size = %v", captured.Bounds().Size())
	}
	if got := color.RGBAModel.Convert(captured.At(1, 1)).(color.RGBA); got != (color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff}) {
		t.Fatalf("capture pixel = %#v", got)
	}
}
