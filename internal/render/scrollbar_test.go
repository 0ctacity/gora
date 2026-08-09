package render

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

func TestPlanScrollbarsUsesSharedTrackThumbGeometry(t *testing.T) {
	node := &project.Node{Type: "scroll", Props: map[string]any{"axis": "vertical", "scrollbar_y": "auto", "scrollbar_x": "hidden"}}
	plan := scrollPlan{
		Viewport: image.Rect(0, 0, 100, 100), ContentSize: image.Pt(100, 300),
		Maximum: image.Pt(0, 200), EnabledY: true,
	}
	for _, test := range []struct {
		name   string
		offset int
		thumb  image.Rectangle
	}{
		{name: "start", offset: 0, thumb: image.Rect(90, 2, 98, 34)},
		{name: "middle", offset: 100, thumb: image.Rect(90, 34, 98, 66)},
		{name: "end", offset: 200, thumb: image.Rect(90, 66, 98, 98)},
	} {
		t.Run(test.name, func(t *testing.T) {
			bars := planScrollbars(node, plan, image.Pt(0, test.offset))
			if len(bars) != 1 || bars[0].Axis != "vertical" {
				t.Fatalf("planned bars = %+v", bars)
			}
			if bars[0].Track != image.Rect(90, 2, 98, 98) {
				t.Fatalf("track = %v", bars[0].Track)
			}
			if bars[0].Thumb != test.thumb {
				t.Fatalf("thumb = %v, want %v", bars[0].Thumb, test.thumb)
			}
		})
	}
}

func TestPlanScrollbarsPoliciesMinThumbAndCorner(t *testing.T) {
	node := &project.Node{Type: "scroll", Props: map[string]any{
		"axis": "both", "scrollbar_x": "auto", "scrollbar_y": "auto",
	}}
	plan := scrollPlan{
		Viewport: image.Rect(0, 0, 100, 80), ContentSize: image.Pt(200, 160),
		Maximum: image.Pt(100, 80), EnabledX: true, EnabledY: true,
	}
	bars := planScrollbars(node, plan, image.Point{})
	if len(bars) != 2 {
		t.Fatalf("both-axis bars = %+v", bars)
	}
	if bars[0].Track != image.Rect(2, 70, 90, 78) || bars[1].Track != image.Rect(90, 2, 98, 70) {
		t.Fatalf("shortened tracks = %+v", bars)
	}
	if bars[0].Thumb != image.Rect(2, 70, 46, 78) || bars[1].Thumb != image.Rect(90, 2, 98, 36) {
		t.Fatalf("both-axis thumbs = %+v", bars)
	}
	if bars[0].Corner != image.Rect(90, 70, 98, 78) || bars[1].Corner != (image.Rectangle{}) {
		t.Fatalf("corner = horizontal:%v vertical:%v", bars[0].Corner, bars[1].Corner)
	}

	node.Props["scrollbar_y"] = "hidden"
	bars = planScrollbars(node, plan, image.Point{})
	if len(bars) != 1 || bars[0].Axis != "horizontal" || bars[0].Track != image.Rect(2, 70, 98, 78) || !bars[0].Corner.Empty() {
		t.Fatalf("mixed policy bars = %+v", bars)
	}

	node.Props["scrollbar_x"] = "always"
	node.Props["scrollbar_y"] = "always"
	zero := plan
	zero.ContentSize = zero.Viewport.Size()
	zero.Maximum = image.Point{}
	bars = planScrollbars(node, zero, image.Point{})
	if len(bars) != 2 || bars[0].Enabled || bars[1].Enabled || bars[0].Thumb != bars[0].Track || bars[1].Thumb != bars[1].Track {
		t.Fatalf("zero-overflow always bars = %+v", bars)
	}

	node.Props["scrollbar_x"] = "auto"
	node.Props["scrollbar_y"] = "auto"
	if bars = planScrollbars(node, zero, image.Point{}); len(bars) != 0 {
		t.Fatalf("zero-overflow auto bars = %+v", bars)
	}
	node.Props["scrollbar_x"] = "hidden"
	node.Props["scrollbar_y"] = "hidden"
	if bars = planScrollbars(node, plan, image.Point{}); len(bars) != 0 {
		t.Fatalf("hidden bars = %+v", bars)
	}
}

func TestPlanScrollbarsUsesTwentyFourPixelMinimumAndDeterministicMapping(t *testing.T) {
	node := &project.Node{Type: "scroll", Props: map[string]any{
		"axis": "vertical", "scrollbar_y": "auto", "scrollbar_x": "hidden",
	}}
	plan := scrollPlan{
		Viewport: image.Rect(0, 0, 100, 100), ContentSize: image.Pt(100, 10000),
		Maximum: image.Pt(0, 9900), EnabledY: true,
	}
	for _, test := range []struct {
		name   string
		offset int
		thumb  image.Rectangle
	}{
		{name: "start", offset: 0, thumb: image.Rect(90, 2, 98, 26)},
		{name: "middle", offset: 4950, thumb: image.Rect(90, 38, 98, 62)},
		{name: "end", offset: 9900, thumb: image.Rect(90, 74, 98, 98)},
	} {
		t.Run(test.name, func(t *testing.T) {
			bars := planScrollbars(node, plan, image.Pt(0, test.offset))
			if len(bars) != 1 {
				t.Fatalf("planned bars = %+v", bars)
			}
			if bars[0].Thumb != test.thumb {
				t.Fatalf("thumb = %v, want %v", bars[0].Thumb, test.thumb)
			}
			if bars[0].Thumb.Dy() != 24 {
				t.Fatalf("thumb length = %d, want 24", bars[0].Thumb.Dy())
			}
		})
	}
}

func TestPlanScrollbarsCapsMinimumThumbToShortTrack(t *testing.T) {
	node := &project.Node{Type: "scroll", Props: map[string]any{
		"axis": "vertical", "scrollbar_y": "auto", "scrollbar_x": "hidden",
	}}
	plan := scrollPlan{
		Viewport: image.Rect(0, 0, 10, 10), ContentSize: image.Pt(10, 1000),
		Maximum: image.Pt(0, 990), EnabledY: true,
	}
	bars := planScrollbars(node, plan, image.Point{})
	if len(bars) != 1 {
		t.Fatalf("planned bars = %+v", bars)
	}
	track := bars[0].Track
	if track.Dy() >= 24 || bars[0].Thumb != track {
		t.Fatalf("short-track thumb = %v, track = %v; want exact track under min", bars[0].Thumb, track)
	}
	if !bars[0].Thumb.Min.In(track) || !bars[0].Thumb.Max.Sub(image.Pt(1, 1)).In(track) || bars[0].Thumb.Empty() {
		t.Fatalf("short-track thumb escaped or inverted: thumb=%v track=%v", bars[0].Thumb, track)
	}
}

func TestScrollbarColorsRemainExactAcrossCPUAndGioModels(t *testing.T) {
	wantTrack := color.NRGBA{R: 80, G: 88, B: 104, A: 50}
	wantThumb := color.NRGBA{R: 80, G: 88, B: 104, A: 130}
	if got := scrollbarTrackColor; got != wantTrack {
		t.Fatalf("track color conversion = %#v, want %#v", got, wantTrack)
	}
	if got := color.NRGBAModel.Convert(scrollbarTrackColor).(color.NRGBA); got != wantTrack {
		t.Fatalf("Gio track color conversion = %#v, want %#v", got, wantTrack)
	}
	if got := scrollbarThumbColor; got != wantThumb {
		t.Fatalf("thumb color conversion = %#v, want %#v", got, wantThumb)
	}
	if got := color.NRGBAModel.Convert(scrollbarThumbColor).(color.NRGBA); got != wantThumb {
		t.Fatalf("Gio thumb color conversion = %#v, want %#v", got, wantThumb)
	}
}

func TestPlanScrollbarsMapsLegacyVisibilityPerEnabledAxis(t *testing.T) {
	plan := scrollPlan{
		Viewport: image.Rect(0, 0, 100, 80), ContentSize: image.Pt(200, 160),
		Maximum: image.Pt(100, 80), EnabledX: true, EnabledY: true,
	}
	node := &project.Node{Type: "scroll", Props: map[string]any{"axis": "both", "scrollbar": true}}
	if bars := planScrollbars(node, plan, image.Point{}); len(bars) != 2 {
		t.Fatalf("legacy true bars = %+v", bars)
	}
	node.Props["scrollbar"] = false
	if bars := planScrollbars(node, plan, image.Point{}); len(bars) != 0 {
		t.Fatalf("legacy false bars = %+v", bars)
	}
	node.Props["axis"] = "vertical"
	node.Props["scrollbar"] = true
	plan.EnabledX = false
	if bars := planScrollbars(node, plan, image.Point{}); len(bars) != 1 || bars[0].Axis != "vertical" {
		t.Fatalf("legacy vertical bars = %+v", bars)
	}
}

func TestCPUAndGioPublishIdenticalDerivedScrollbarGeometry(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll",
		Props:    map[string]any{"axis": "both", "scrollbar_x": "auto", "scrollbar_y": "always"},
		Children: []*project.Node{{Handle: "content", Type: "surface", Props: map[string]any{"width": int64(180), "height": int64(140)}}},
	}
	viewport := image.Pt(100, 80)
	state := State{Scroll: map[string]image.Point{"feed": image.Pt(30, 20)}}
	cpu := Render(root, viewport, state)
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, state)
	if len(cpu.Derived) != 2 || len(gio.Derived) != 2 {
		t.Fatalf("derived bars CPU=%+v Gio=%+v", cpu.Derived, gio.Derived)
	}
	for index := range cpu.Derived {
		left, right := cpu.Derived[index], gio.Derived[index]
		if left.Axis != right.Axis || left.Track != right.Track || left.Thumb != right.Thumb || left.Corner != right.Corner || left.Bounds != right.Bounds || left.Clip != right.Clip || left.Offset != right.Offset || left.Maximum != right.Maximum || left.Enabled != right.Enabled {
			t.Fatalf("derived[%d] CPU=%+v Gio=%+v", index, left, right)
		}
	}
	if bar := firstSemanticRole(cpu.Tree, "scrollbar"); bar == nil || bar.Role != "scrollbar" {
		t.Fatal("CPU semantic tree omitted derived scrollbar")
	}
	if bar := firstSemanticRole(gio.Tree, "scrollbar"); bar == nil || bar.Role != "scrollbar" {
		t.Fatal("Gio semantic tree omitted derived scrollbar")
	}
}

func firstSemanticRole(root *semantic.Node, role string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.Role == role {
			return node
		}
	}
	return nil
}

func TestGioCacheReplaysDerivedScrollbarWithoutRebuilding(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll",
		Props:    map[string]any{"axis": "both", "scrollbar_x": "auto", "scrollbar_y": "auto"},
		Children: []*project.Node{{Handle: "content", Type: "surface", Props: map[string]any{"width": int64(180), "height": int64(140)}}},
	}
	viewport := image.Pt(100, 80)
	theme := material.NewTheme()
	var cache GioCache
	layoutCached := func(state State) GioResult {
		var operations op.Ops
		return cache.Layout(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, state)
	}
	initial := layoutCached(State{})
	shifted := layoutCached(State{Scroll: map[string]image.Point{"feed": image.Pt(40, 30)}})
	if cache.builds != 1 {
		t.Fatalf("scene builds = %d, want one", cache.builds)
	}
	if len(initial.Derived) != 2 || len(shifted.Derived) != 2 {
		t.Fatalf("cached derived bars initial=%+v shifted=%+v", initial.Derived, shifted.Derived)
	}
	if initial.Derived[0].Thumb == shifted.Derived[0].Thumb || initial.Derived[1].Thumb == shifted.Derived[1].Thumb {
		t.Fatalf("scroll-only replay did not move thumbs: initial=%+v shifted=%+v", initial.Derived, shifted.Derived)
	}
	if initial.Derived[0].Corner.Empty() || initial.Derived[0].Corner != shifted.Derived[0].Corner {
		t.Fatalf("scroll-only replay changed shared corner: initial=%v shifted=%v", initial.Derived[0].Corner, shifted.Derived[0].Corner)
	}
	wantInitial := []struct {
		axis   string
		thumb  image.Rectangle
		offset int
		max    float64
	}{
		{axis: "horizontal", thumb: image.Rect(2, 70, 51, 78), offset: 0, max: 80},
		{axis: "vertical", thumb: image.Rect(90, 2, 98, 41), offset: 0, max: 60},
	}
	wantShifted := []struct {
		axis   string
		thumb  image.Rectangle
		offset int
		max    float64
	}{
		{axis: "horizontal", thumb: image.Rect(22, 70, 71, 78), offset: 40, max: 80},
		{axis: "vertical", thumb: image.Rect(90, 17, 98, 56), offset: 30, max: 60},
	}
	for _, test := range []struct {
		name string
		root *semantic.Node
		bars []semantic.DerivedDescriptor
		want []struct {
			axis   string
			thumb  image.Rectangle
			offset int
			max    float64
		}
	}{
		{name: "initial", root: initial.Tree, bars: initial.Derived, want: wantInitial},
		{name: "shifted", root: shifted.Tree, bars: shifted.Derived, want: wantShifted},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.bars) != len(test.want) {
				t.Fatalf("derived bars = %+v, want %d", test.bars, len(test.want))
			}
			for index, want := range test.want {
				got := test.bars[index]
				if got.Axis != want.axis || got.Thumb != want.thumb || got.Offset != want.offset || got.Maximum != int(want.max) || !got.Enabled {
					t.Fatalf("derived[%d] = %+v, want axis=%s thumb=%v offset=%d max=%v enabled", index, got, want.axis, want.thumb, want.offset, want.max)
				}
			}
			var semanticBars []*semantic.Node
			for _, node := range semantic.Flatten(test.root) {
				if node.Role == "scrollbar" {
					semanticBars = append(semanticBars, node)
				}
			}
			if len(semanticBars) != len(test.want) {
				t.Fatalf("semantic scrollbar nodes = %d, want %d", len(semanticBars), len(test.want))
			}
			for index, want := range test.want {
				bar := semanticBars[index]
				if bar.Orientation != want.axis || bar.Value != want.offset || bar.Max == nil || *bar.Max != want.max || !bar.Enabled {
					t.Fatalf("semantic scrollbar[%d] = %+v, want axis=%s value=%d max=%v enabled", index, bar, want.axis, want.offset, want.max)
				}
			}
		})
	}
}

func TestScrollbarCaptureUsesRendererChromeAtRequestedScale(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Type: "scroll",
		Props: map[string]any{"axis": "vertical", "scrollbar_y": "always", "scrollbar_x": "hidden"},
		Children: []*project.Node{{Handle: "content", Type: "surface", Props: map[string]any{
			"height": int64(200), "background": "#FFFFFF",
		}}},
	}
	for _, scale := range []int{1, 2} {
		name := "1x"
		if scale == 2 {
			name = "2x"
		}
		t.Run(name, func(t *testing.T) {
			captured, err := captureGio(root, image.Pt(40, 40), State{}, scale)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "metal device") {
					t.Skipf("headless capture requires Metal: %v", err)
				}
				t.Fatal(err)
			}
			// The track is inset by two logical units and painted after the
			// content. A point well inside it must therefore differ from the
			// white content at both requested scales.
			x := 34 * scale
			y := 12 * scale
			got := color.RGBAModel.Convert(captured.At(x, y)).(color.RGBA)
			if got.R == 0xff && got.G == 0xff && got.B == 0xff {
				t.Fatalf("capture omitted renderer scrollbar at %dx: pixel=%#v", scale, got)
			}
		})
	}
}

func TestGioCacheRetainsOffscreenDerivedScrollbarWithEmptyClip(t *testing.T) {
	root := &project.Node{
		Handle: "outer", Type: "scroll",
		Props: map[string]any{"axis": "both", "scrollbar_x": "auto", "scrollbar_y": "auto"},
		Children: []*project.Node{{
			Handle: "canvas", Type: "overlay", Props: map[string]any{"width": int64(400), "height": int64(400)},
			Children: []*project.Node{{
				Handle: "inner", Type: "scroll", Props: map[string]any{
					"axis": "both", "width": int64(100), "height": int64(100),
					"scrollbar_x": "auto", "scrollbar_y": "auto",
				}, Place: map[string]any{"x": int64(0), "y": int64(0)},
				Children: []*project.Node{{Handle: "content", Type: "surface", Props: map[string]any{"width": int64(180), "height": int64(160)}}},
			}},
		}},
	}
	viewport := image.Pt(100, 100)
	var cache GioCache
	theme := material.NewTheme()
	layoutCached := func(state State) GioResult {
		var operations op.Ops
		return cache.Layout(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, state)
	}
	layoutCached(State{})
	offscreen := layoutCached(State{Scroll: map[string]image.Point{"outer": image.Pt(100, 100)}})
	var found int
	for _, descriptor := range offscreen.Derived {
		if descriptor.OwnerHandle == "inner" {
			found++
			if !descriptor.Clip.Empty() {
				t.Fatalf("offscreen inner derived clip = %v, want empty", descriptor.Clip)
			}
		}
	}
	if found != 2 {
		t.Fatalf("offscreen inner derived descriptors = %d, want both axes", found)
	}
	if bar := firstSemanticRole(offscreen.Tree, "scrollbar"); bar == nil || bar.InViewport {
		t.Fatalf("offscreen derived semantic node = %+v", bar)
	}
	if cache.builds != 1 {
		t.Fatalf("offscreen replay rebuilt scene %d times", cache.builds)
	}
}

func TestCPUScrollbarPaintOrderAllowsLaterSiblingToCoverChrome(t *testing.T) {
	root := &project.Node{
		Handle: "root", Type: "overlay",
		Children: []*project.Node{
			{
				Handle: "feed", Name: "feed", Type: "scroll",
				Props:    map[string]any{"axis": "vertical", "scrollbar_y": "always", "scrollbar_x": "hidden"},
				Children: []*project.Node{{Handle: "content", Type: "surface", Props: map[string]any{"height": int64(300), "background": "#FFFFFF"}}},
			},
			{
				Handle: "cover", Name: "cover", Type: "surface",
				Props: map[string]any{"width": int64(8), "height": int64(10), "background": "#FF0000"},
				Place: map[string]any{"x": int64(90), "y": int64(10)},
			},
		},
	}
	result := Render(root, image.Pt(100, 100), State{})
	covered := color.RGBAModel.Convert(result.Image.At(94, 14)).(color.RGBA)
	if covered != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("later sibling did not cover scrollbar pixel: %#v", covered)
	}
	visible := color.RGBAModel.Convert(result.Image.At(94, 20)).(color.RGBA)
	if visible == (color.RGBA{R: 255, G: 255, B: 255, A: 255}) || visible == (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("uncovered scrollbar pixel = %#v, want scrollbar paint", visible)
	}
	var bar *semantic.Node
	var cover *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Role == "scrollbar" {
			bar = node
		}
		if node.Name == "cover" {
			cover = node
		}
	}
	if bar == nil || cover == nil || len(bar.Children) < 2 {
		t.Fatalf("paint-order semantic nodes = bar:%+v cover:%+v", bar, cover)
	}
	if !(result.Geometry["content"].PaintOrder < bar.PaintOrder && bar.PaintOrder < bar.Children[0].PaintOrder && bar.Children[0].PaintOrder < bar.Children[1].PaintOrder && bar.Children[1].PaintOrder < result.Geometry["cover"].PaintOrder) {
		t.Fatalf("paint order content=%d axis=%d track=%d thumb=%d cover=%d", result.Geometry["content"].PaintOrder, bar.PaintOrder, bar.Children[0].PaintOrder, bar.Children[1].PaintOrder, result.Geometry["cover"].PaintOrder)
	}
	if bar.Clip == nil || bar.Clip.ImageRectangle() != image.Rect(0, 0, 100, 100) {
		t.Fatalf("scrollbar clip = %+v, want owner viewport", bar.Clip)
	}
}

func TestCPUScrollbarDescriptorsComposeNestedPartialClips(t *testing.T) {
	root := &project.Node{
		Handle: "outer", Type: "scroll",
		Props: map[string]any{"axis": "both", "scrollbar_x": "hidden", "scrollbar_y": "hidden"},
		Children: []*project.Node{{
			Handle: "canvas", Type: "overlay", Props: map[string]any{"width": int64(240), "height": int64(240)},
			Children: []*project.Node{{
				Handle: "inner", Type: "scroll", Props: map[string]any{
					"axis": "vertical", "width": int64(80), "height": int64(80),
					"scrollbar_y": "always", "scrollbar_x": "hidden",
				}, Place: map[string]any{"x": int64(40), "y": int64(40)},
				Children: []*project.Node{{Handle: "inner-content", Type: "surface", Props: map[string]any{"height": int64(160), "background": "#FFFFFF"}}},
			}},
		}},
	}
	result := Render(root, image.Pt(100, 100), State{})
	var descriptor *semantic.DerivedDescriptor
	for index := range result.Derived {
		if result.Derived[index].OwnerHandle == "inner" {
			descriptor = &result.Derived[index]
			break
		}
	}
	if descriptor == nil {
		t.Fatal("nested inner scrollbar descriptor missing")
	}
	if descriptor.Clip != image.Rect(40, 40, 100, 100) {
		t.Fatalf("nested scrollbar clip = %v, want partial outer clip", descriptor.Clip)
	}
	if descriptor.Track.Empty() || descriptor.Thumb.Empty() {
		t.Fatalf("nested scrollbar geometry = track:%v thumb:%v", descriptor.Track, descriptor.Thumb)
	}
}
