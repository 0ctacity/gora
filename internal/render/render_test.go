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
