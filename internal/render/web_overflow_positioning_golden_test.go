package render

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/semantic"
)

func TestWebOverflowPositioningExampleLoadsAtRequiredViewports(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "web-overflow-positioning", "app.gora")
	for _, width := range []int{1280, 720, 420} {
		loaded, diagnostics := project.Load(repositoryRoot, entry, width)
		if len(diagnostics) != 0 {
			t.Fatalf("width %d diagnostics: %+v", width, diagnostics)
		}
		if loaded == nil {
			t.Fatalf("width %d produced no loaded document", width)
		}
		root := loaded.Root
		if screen := loaded.Screens[loaded.Selected]; screen != nil {
			root = screen
		}
		if root == nil {
			t.Fatalf("width %d produced no selected root", width)
		}
		if loaded.Viewport.Height != 900 {
			t.Fatalf("width %d viewport = %+v, want height 900", width, loaded.Viewport)
		}
	}
}

func TestWebOverflowPositioningConformanceGoldensAndGeometry(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "web-overflow-positioning", "app.gora")
	tests := []struct {
		name  string
		width int
		scale int
		state func(*project.Node, map[string]map[string]any) State
	}{
		{name: "start", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory"}
		}},
		{name: "nested-chain", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory", Scroll: map[string]image.Point{"observatory-scroll": image.Pt(120, 90), "canvas-scroll": image.Pt(200, 150)}}
		}},
		{name: "sticky-start", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory"}
		}},
		{name: "sticky-middle", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory", Scroll: map[string]image.Point{"observatory-scroll": image.Pt(0, 150), "canvas-scroll": image.Pt(0, 120)}}
		}},
		{name: "sticky-end", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory", Scroll: map[string]image.Point{"observatory-scroll": image.Pt(240, 340), "canvas-scroll": image.Pt(300, 260)}}
		}},
		{name: "fixed", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory", Scroll: map[string]image.Point{"observatory-scroll": image.Pt(180, 260), "canvas-scroll": image.Pt(260, 180)}}
		}},
		{name: "z-order", width: 1280, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory"}
		}},
		{name: "popup-open", width: 1280, state: func(root *project.Node, values map[string]map[string]any) State {
			selectNode, optionNode := namedProjectNode(root, "specimen-mode"), namedProjectNode(root, "detail-option")
			if selectNode == nil || optionNode == nil {
				return State{Values: values, Screen: "observatory"}
			}
			return State{Values: values, Screen: "observatory", OpenSelect: selectNode.Handle, ActiveOption: optionNode.Handle}
		}},
		{name: "narrow", width: 420, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory"}
		}},
		{name: "wide-2x", width: 1280, scale: 2, state: func(_ *project.Node, values map[string]map[string]any) State {
			return State{Values: values, Screen: "observatory"}
		}},
	}
	var fixedBounds image.Rectangle
	var stickyNormal image.Rectangle
	var fixedChildBounds image.Rectangle
	var fixedChildClip image.Rectangle
	var fixedStartImage *image.RGBA
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded, diagnostics := project.Load(repositoryRoot, entry, test.width)
			if len(diagnostics) != 0 {
				t.Fatalf("load diagnostics: %+v", diagnostics)
			}
			authored := loaded.Screens[loaded.Selected]
			values := goldenStateValues(loaded.StateScopes)
			state := test.state(authored, values)
			// A direct reference capture has no field focus; use an explicit
			// sentinel so empty field handles cannot be mistaken for focus.
			if state.Focused == "" {
				state.Focused = "__golden_no_focus__"
			}
			fields := goldenFieldStates(authored, values, state.Screen)
			effective := interaction.ResolveTreeWithFields(authored, values, interaction.Transient{OpenSelect: state.OpenSelect, ActiveOption: state.ActiveOption}, fields, state.Screen)
			if effective == nil {
				t.Fatal("effective root is nil")
			}
			viewport := image.Pt(test.width, loaded.Viewport.Height)
			scale := test.scale
			if scale < 1 {
				scale = 1
			}
			result := renderScaled(effective, viewport, state, scale)
			if result.Image == nil || result.Image.Bounds().Dx() != viewport.X*scale || result.Image.Bounds().Dy() != viewport.Y*scale {
				t.Fatalf("capture dimensions = %v, want %dx%d", result.Image.Bounds(), viewport.X*scale, viewport.Y*scale)
			}
			if len(result.Scroll) < 2 {
				t.Fatalf("scroll metrics = %+v, want outer and nested named scrollports", result.Scroll)
			}
			for _, name := range []string{"specimen-query", "specimen-notes"} {
				field := namedProjectNode(effective, name)
				if field == nil || len(field.Children) == 0 || field.Children[0].Props["text"] == nil {
					t.Fatalf("field %q has no materialized visible text: %+v", name, field)
				}
			}
			if test.name == "start" {
				axes := map[string]bool{}
				for _, node := range semantic.Flatten(result.Tree) {
					if node.Role != "scrollbar" {
						continue
					}
					if node.Bounds == nil || node.Max == nil || node.ViewportSize == nil || node.ContentSize == nil {
						t.Fatalf("derived scrollbar lacks geometry/metrics: %+v", node)
					}
					axes[node.Orientation] = true
				}
				if len(axes) != 2 || !axes["horizontal"] || !axes["vertical"] {
					t.Fatalf("derived scrollbar axes = %+v, want horizontal and vertical policies", axes)
				}
				assertObservableHeader(t, result, effective, viewport)
			}
			if test.name == "popup-open" {
				assertObservableHeader(t, result, effective, viewport)
			}
			fixed := namedGeometry(result, effective, "fixed-status-action")
			if fixed.Empty() {
				t.Fatal("fixed status control has no final geometry")
			}
			fixedNode := namedProjectNode(effective, "fixed-status-action")
			if fixedNode == nil || len(fixedNode.Children) != 1 {
				t.Fatal("fixed status control presentation child missing")
			}
			fixedChild := fixedNode.Children[0]
			if result.Layout[fixedChild.Handle].Final.Empty() || result.Geometry[fixedChild.Handle].Clip.Empty() {
				t.Fatalf("fixed presentation child has no geometry: %+v/%+v", result.Layout[fixedChild.Handle], result.Geometry[fixedChild.Handle])
			}
			if test.name == "start" {
				fixedBounds = fixed
				fixedChildBounds = result.Layout[fixedChild.Handle].Final
				fixedChildClip = result.Geometry[fixedChild.Handle].Clip
				fixedStartImage = result.Image
				stickyNormal = namedLayout(result, effective, "sticky-ruler").Normal
			}
			if test.name == "fixed" || test.name == "nested-chain" || test.name == "sticky-middle" || test.name == "sticky-end" {
				if fixed != fixedBounds || result.Layout[fixedChild.Handle].Final != fixedChildBounds || result.Geometry[fixedChild.Handle].Clip != fixedChildClip {
					t.Fatalf("fixed subtree moved/clipped under %s scroll: parent=%v/%v child=%v/%v clip=%v/%v", test.name, fixed, fixedBounds, result.Layout[fixedChild.Handle].Final, fixedChildBounds, result.Geometry[fixedChild.Handle].Clip, fixedChildClip)
				}
				pixelBounds := fixedBounds.Inset(4)
				for y := pixelBounds.Min.Y; y < pixelBounds.Max.Y; y++ {
					for x := pixelBounds.Min.X; x < pixelBounds.Max.X; x++ {
						if fixedStartImage != nil && result.Image.At(x, y) != fixedStartImage.At(x, y) {
							t.Fatalf("fixed subtree pixels changed under %s scroll at (%d,%d): start=%#v current=%#v", test.name, x, y, fixedStartImage.At(x, y), result.Image.At(x, y))
						}
					}
				}
				for y := fixedChildBounds.Min.Y; y < fixedChildBounds.Max.Y; y++ {
					for x := fixedChildBounds.Min.X; x < fixedChildBounds.Max.X; x++ {
						if fixedStartImage != nil && result.Image.At(x, y) != fixedStartImage.At(x, y) {
							t.Fatalf("fixed presentation child pixels changed under %s scroll at (%d,%d): start=%#v current=%#v", test.name, x, y, fixedStartImage.At(x, y), result.Image.At(x, y))
						}
					}
				}
			}
			if test.name == "sticky-middle" || test.name == "sticky-end" {
				sticky := namedLayout(result, effective, "sticky-ruler")
				if sticky.Normal != stickyNormal || sticky.Final == sticky.Normal {
					t.Fatalf("sticky ruler did not preserve normal/final split: %+v", sticky)
				}
			}
			if test.name == "z-order" {
				negative := namedGeometryRecord(result, effective, "negative-context")
				zero := namedGeometryRecord(result, effective, "zero-context")
				positive := namedGeometryRecord(result, effective, "positive-context")
				if !(negative.PaintOrder < zero.PaintOrder && zero.PaintOrder < positive.PaintOrder) {
					t.Fatalf("z-order ranks = %d/%d/%d", negative.PaintOrder, zero.PaintOrder, positive.PaintOrder)
				}
			}
			if test.name == "popup-open" {
				popup := semanticNodeByName(result.Tree, "detail-option")
				if popup == nil || !popup.Visible || popup.Bounds == nil {
					t.Fatalf("open popup option = %+v", popup)
				}
				positive := semanticNodeByName(result.Tree, "positive-context")
				if positive == nil || popup.PaintOrder <= positive.PaintOrder || popup.Bounds.Y < 0 || popup.Bounds.Y+popup.Bounds.Height > viewport.Y {
					t.Fatalf("popup precedence/clamp = popup:%+v positive:%+v viewport:%v", popup, positive, viewport)
				}
			}
			if test.name == "narrow" {
				assertObservableHeader(t, result, effective, viewport)
				header := semanticNodeByName(result.Tree, "sticky-ruler")
				outerNode := namedProjectNode(effective, "observatory-scroll")
				if header == nil || header.Bounds == nil || header.Bounds.Width > viewport.X || !header.InViewport || outerNode == nil || result.Scroll[outerNode.Handle].Maximum.X <= 0 {
					t.Fatalf("narrow header/workspace geometry = header:%+v outer:%+v viewport:%v", header, result.Scroll[outerNode.Handle], viewport)
				}
			}
			if test.name == "nested-chain" {
				outerNode, innerNode := namedProjectNode(effective, "observatory-scroll"), namedProjectNode(effective, "canvas-scroll")
				if outerNode == nil || innerNode == nil {
					t.Fatal("named scrollports missing from effective tree")
				}
				outer := result.Scroll[outerNode.Handle]
				inner := result.Scroll[innerNode.Handle]
				if outer.Maximum.X <= 0 || outer.Maximum.Y <= 0 || inner.Maximum.X <= 0 || inner.Maximum.Y <= 0 {
					t.Fatalf("nested scroll maxima = outer:%+v inner:%+v", outer, inner)
				}
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, result.Image); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "web-overflow-positioning-"+test.name+".png")
			if os.Getenv("GORA_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, encoded.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with GORA_UPDATE_GOLDEN=1)", err)
			}
			if !bytes.Equal(encoded.Bytes(), want) {
				t.Fatalf("capture differs from %s (run with GORA_UPDATE_GOLDEN=1 to review and update)", golden)
			}
		})
	}
}

func goldenStateValues(scopes []project.StateScope) map[string]map[string]any {
	values := make(map[string]map[string]any, len(scopes))
	for _, scope := range scopes {
		values[scope.ID] = make(map[string]any, len(scope.State))
		for name, declaration := range scope.State {
			values[scope.ID][name] = declaration.Default
		}
		for name, value := range scope.Initial {
			values[scope.ID][name] = value
		}
	}
	return values
}

func goldenFieldStates(root *project.Node, values map[string]map[string]any, screen string) map[string]interaction.EditingState {
	fields := make(map[string]interaction.EditingState)
	var walk func(*project.Node)
	walk = func(node *project.Node) {
		if node == nil {
			return
		}
		if node.Type == "text_field" || node.Type == "text_area" {
			text, _ := values[node.Scope][node.Binding].(string)
			caret := len([]rune(text))
			fields[semantic.StableID(node, screen)] = interaction.EditingState{
				Draft: text, Committed: text, SelectionStart: caret, SelectionEnd: caret,
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return fields
}

func namedProjectNode(root *project.Node, name string) *project.Node {
	if root == nil {
		return nil
	}
	if root.Name == name {
		return root
	}
	for _, child := range root.Children {
		if found := namedProjectNode(child, name); found != nil {
			return found
		}
	}
	return nil
}

func namedGeometryRecord(result Result, root *project.Node, name string) semantic.Geometry {
	node := namedProjectNode(root, name)
	if node == nil {
		return semantic.Geometry{}
	}
	return result.Geometry[node.Handle]
}

func namedGeometry(result Result, root *project.Node, name string) image.Rectangle {
	node := namedProjectNode(root, name)
	if node == nil {
		return image.Rectangle{}
	}
	return result.Bounds[node.Handle]
}

func namedLayout(result Result, root *project.Node, name string) LayoutRecord {
	node := namedProjectNode(root, name)
	if node == nil {
		return LayoutRecord{}
	}
	return result.Layout[node.Handle]
}

func semanticNodeByName(root *semantic.Node, name string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.Name == name {
			return node
		}
	}
	return nil
}

func assertObservableHeader(t *testing.T, result Result, root *project.Node, viewport image.Point) {
	t.Helper()
	header := semanticNodeByName(result.Tree, "sticky-ruler")
	selectNode := semanticNodeByName(result.Tree, "specimen-mode")
	query := semanticNodeByName(result.Tree, "specimen-query")
	notes := semanticNodeByName(result.Tree, "specimen-notes")
	trigger := semanticChildByType(selectNode, "select_trigger")
	if header == nil || header.Bounds == nil || !header.InViewport || selectNode == nil || selectNode.Bounds == nil || !selectNode.InViewport || trigger == nil || trigger.Bounds == nil || !trigger.InViewport || query == nil || query.Bounds == nil || !query.InViewport || notes == nil || notes.Bounds == nil || !notes.InViewport {
		t.Fatalf("observable header nodes = header:%+v select:%+v trigger:%+v query:%+v notes:%+v viewport:%v", header, selectNode, trigger, query, notes, viewport)
	}
	if header.Bounds.Width > viewport.X {
		t.Fatalf("sticky header exceeds visible viewport: bounds=%+v viewport=%v", header.Bounds, viewport)
	}
	wantWidth := 1216
	if viewport.X <= 720 {
		wantWidth = 384
	}
	if header.Bounds.Width != wantWidth {
		t.Fatalf("sticky header width=%d, want %d for viewport %v", header.Bounds.Width, wantWidth, viewport)
	}
	if query.Bounds.Width > header.Bounds.Width || notes.Bounds.Width > header.Bounds.Width {
		t.Fatalf("field roots exceed sticky header: query=%+v notes=%+v header=%+v", query.Bounds, notes.Bounds, header.Bounds)
	}
	if viewport.X <= 720 && (notes.Clip == nil || !semanticRectContains(notes.Clip, notes.Bounds)) {
		t.Fatalf("narrow notes field is clipped by its header: notes=%+v clip=%+v header=%+v", notes.Bounds, notes.Clip, header.Bounds)
	}
	_ = root
}

func semanticRectContains(outer, inner *semantic.Rect) bool {
	return outer != nil && inner != nil && outer.X <= inner.X && outer.Y <= inner.Y && outer.X+outer.Width >= inner.X+inner.Width && outer.Y+outer.Height >= inner.Y+inner.Height
}

func semanticChildByType(parent *semantic.Node, typ string) *semantic.Node {
	if parent == nil {
		return nil
	}
	for _, child := range parent.Children {
		if child.Type == typ {
			return child
		}
		if found := semanticChildByType(child, typ); found != nil {
			return found
		}
	}
	return nil
}
