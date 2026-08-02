package render

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	gioopentype "gioui.org/font/opentype"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

// GioResult describes the nodes laid out into a native Gio frame.
type GioResult struct {
	Bounds   map[string]image.Rectangle
	Geometry map[string]semantic.Geometry
	Tree     *semantic.Node
}

type gioRenderer struct {
	gtx           layout.Context
	theme         *material.Theme
	state         State
	result        GioResult
	opacity       float64
	scene         *gioScene
	scrolls       []sceneScroll
	paintOrder    int
	geometryOrder []string
	viewport      image.Rectangle
	topLayers     []topLayer
}

type nativeFont struct {
	modified time.Time
	size     int64
	shaper   *text.Shaper
	typeface font.Typeface
}

type shadowLayer struct {
	bounds image.Rectangle
	color  color.NRGBA
	radius int
}

var nativeFontCache = struct {
	sync.Mutex
	fonts map[nativeFontKey]nativeFont
}{fonts: make(map[nativeFontKey]nativeFont)}

type nativeFontKey struct {
	theme *material.Theme
	path  string
}

// LayoutGio lays out and paints a resolved document directly into Gio operations.
// Bounds and viewport are expressed in document logical units.
func LayoutGio(gtx layout.Context, theme *material.Theme, root *project.Node, viewport image.Point, state State) GioResult {
	if theme == nil {
		theme = material.NewTheme()
	}
	result := GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry)}
	if root == nil || viewport.X <= 0 || viewport.Y <= 0 {
		return result
	}
	r := gioRenderer{gtx: gtx, theme: theme, state: state, result: result, opacity: 1, viewport: image.Rectangle{Max: viewport}}
	bounds := image.Rectangle{Max: viewport}
	r.layout(root, bounds, bounds)
	for index := 0; index < len(r.topLayers); index++ {
		layer := r.topLayers[index]
		r.layoutFinal(layer.node, layer.bounds, r.viewport)
	}
	r.result.Tree = semantic.Build(root, r.result.Geometry, semanticContext(state))
	return r.result
}

func captureGio(root *project.Node, viewport image.Point, state State, scale int) (*image.RGBA, error) {
	size := image.Pt(viewport.X*scale, viewport.Y*scale)
	window, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return nil, err
	}
	defer window.Release()
	var operations op.Ops
	gtx := layout.Context{
		Ops:         &operations,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: float32(scale), PxPerSp: float32(scale)},
		Constraints: layout.Exact(size),
	}
	theme := material.NewTheme()
	defer releaseNativeFonts(theme)
	LayoutGio(gtx, theme, root, viewport, state)
	if err := window.Frame(&operations); err != nil {
		return nil, err
	}
	captured := image.NewRGBA(image.Rectangle{Max: size})
	if err := window.Screenshot(captured); err != nil {
		return nil, err
	}
	return captured, nil
}

func (r *gioRenderer) layout(node *project.Node, bounds, currentClip image.Rectangle) {
	r.layoutNode(node, bounds, currentClip, false)
}

func (r *gioRenderer) layoutFinal(node *project.Node, bounds, currentClip image.Rectangle) {
	r.layoutNode(node, bounds, currentClip, true)
}

func (r *gioRenderer) layoutNode(node *project.Node, bounds, currentClip image.Rectangle, final bool) {
	if node == nil || node.Hidden || bounds.Empty() {
		return
	}
	if !final {
		bounds = applySize(node, bounds)
	}
	node = interactiveNodeForState(node, r.state)
	nodeClip := currentClip.Intersect(bounds)
	previousOpacity := r.opacity
	r.opacity *= clamp(number(node.Props["opacity"], 1), 0, 1)
	defer func() { r.opacity = previousOpacity }()

	r.result.Bounds[node.Handle] = bounds
	r.result.Geometry[node.Handle] = semantic.Geometry{
		Bounds: bounds, Clip: nodeClip, PaintOrder: r.paintOrder, Props: cloneMap(node.Props),
	}
	r.geometryOrder = append(r.geometryOrder, node.Handle)
	r.paintOrder++
	if r.scene != nil {
		r.scene.geometries = append(r.scene.geometries, sceneGeometry{
			handle: node.Handle, geometry: r.result.Geometry[node.Handle], node: node,
			scrolls: append([]sceneScroll(nil), r.scrolls...),
		})
	}

	switch node.Type {
	case "_viewport":
		r.recordPaint(func() {
			r.paintBackground(bounds, currentClip, node.Props["background"], 0)
		})
		if len(node.Children) == 1 {
			r.layout(node.Children[0], bounds, nodeClip)
		}
	case "surface", "toggle", "checkbox", "radio", "tab", "tab_panel", "option", "select_trigger",
		"slider_track", "slider_fill", "slider_thumb", "stepper_decrement", "stepper_value", "stepper_increment":
		r.recordPaint(func() {
			r.paintSurfaceGio(node, bounds, currentClip)
		})
		if len(node.Children) == 1 {
			inner := inset(bounds, insets(node.Props["padding"]))
			childBounds := r.surfaceChildBounds(node.Children[0], inner)
			r.layout(node.Children[0], childBounds, chooseClip(node, currentClip, bounds))
		}
	case "button", "link":
		if r.scene == nil {
			r.paintSurfaceGio(node, bounds, currentClip)
		} else {
			r.scene.items = append(r.scene.items, sceneItem{
				button:  &sceneButton{node: node, bounds: bounds, clip: currentClip, opacity: r.opacity},
				scrolls: append([]sceneScroll(nil), r.scrolls...),
			})
		}
		if len(node.Children) == 1 {
			inner := inset(bounds, insets(node.Props["padding"]))
			childBounds := r.surfaceChildBounds(node.Children[0], inner)
			r.layout(node.Children[0], childBounds, chooseClip(node, currentClip, bounds))
		}
	case "stack", "radio_group":
		r.stackGio(node, bounds, nodeClip)
	case "stepper":
		clone := *node
		clone.Props = cloneMap(node.Props)
		clone.Props["direction"] = "horizontal"
		r.stackGio(&clone, bounds, nodeClip)
	case "slider":
		r.recordPaint(func() { r.paintSurfaceGio(node, bounds, currentClip) })
		parts := sliderParts(node, inset(bounds, insets(node.Props["padding"])))
		for _, child := range node.Children {
			if childBounds, ok := parts[child.Handle]; ok && !childBounds.Empty() {
				r.layoutFinal(child, childBounds, nodeClip)
			}
		}
	case "tabs":
		parts := tabsParts(node, inset(bounds, insets(node.Props["padding"])), r.intrinsicLeafSize)
		for _, child := range node.Children {
			if childBounds, ok := parts[child.Handle]; ok && !childBounds.Empty() {
				r.layoutFinal(child, childBounds, nodeClip)
			}
		}
	case "select":
		trigger, popup, popupBounds := selectPopupBounds(node, bounds, r.viewport, r.intrinsicLeafSize)
		if trigger != nil {
			r.layoutFinal(trigger, bounds, nodeClip)
		}
		if popup != nil && !popupBounds.Empty() {
			r.topLayers = append(r.topLayers, topLayer{node: popup, bounds: popupBounds})
		}
	case "select_popup":
		r.recordPaint(func() { r.paintSurfaceGio(node, bounds, currentClip) })
		clone := *node
		clone.Props = cloneMap(node.Props)
		clone.Props["direction"] = "vertical"
		r.stackGio(&clone, bounds, nodeClip)
	case "grid":
		r.gridGio(node, bounds, nodeClip)
	case "overlay":
		for _, child := range node.Children {
			r.layout(child, overlayPlace(child, bounds), nodeClip)
		}
	case "scroll":
		r.scrollGio(node, bounds, nodeClip)
	case "divider":
		r.recordPaint(func() {
			r.paintDividerGio(node, bounds, nodeClip)
		})
	case "text":
		r.recordPaint(func() {
			r.paintTextGio(node, bounds, nodeClip)
		})
	case "image":
		r.recordPaint(func() {
			r.paintImageGio(node, bounds, nodeClip)
		})
	default:
		for _, child := range node.Children {
			r.layout(child, place(child, bounds), nodeClip)
		}
	}
}

func (r *gioRenderer) surfaceChildBounds(child *project.Node, bounds image.Rectangle) image.Rectangle {
	alignment := stringValue(child.Place["alignment"], "")
	if alignment == "" {
		return bounds
	}
	size := r.intrinsicSize(child, bounds.Size())
	if width := int(number(child.Props["width"], 0)); width > 0 {
		size.X = width
	}
	if height := int(number(child.Props["height"], 0)); height > 0 {
		size.Y = height
	}
	size.X = min(max(0, size.X), bounds.Dx())
	size.Y = min(max(0, size.Y), bounds.Dy())
	return alignedRect(bounds, size, alignment)
}

func alignedRect(parent image.Rectangle, size image.Point, alignment string) image.Rectangle {
	x, y := parent.Min.X, parent.Min.Y
	switch alignment {
	case "top", "center", "bottom":
		x += (parent.Dx() - size.X) / 2
	case "top_right", "right", "bottom_right", "end":
		x += parent.Dx() - size.X
	}
	switch alignment {
	case "left", "center", "right":
		y += (parent.Dy() - size.Y) / 2
	case "bottom_left", "bottom", "bottom_right", "end":
		y += parent.Dy() - size.Y
	}
	return image.Rect(x, y, x+size.X, y+size.Y)
}

func (r *gioRenderer) paintSurfaceGio(node *project.Node, bounds, currentClip image.Rectangle) {
	if shadow, ok := node.Props["shadow"].(map[string]any); ok {
		for _, layer := range shadowLayers(bounds, shadow) {
			r.fillRounded(layer.bounds, currentClip, layer.color, radiusValue(node.Props["radius"])+layer.radius)
		}
	}
	r.paintBackground(bounds, currentClip, node.Props["background"], radiusValue(node.Props["radius"]))
	if border, ok := node.Props["border"].(map[string]any); ok {
		thickness := max(1, int(number(border["thickness"], 1)))
		value := colorValue(border["color"], color.RGBA{A: 255})
		r.paintRoundedBorder(bounds, currentClip, value, thickness, radiusValue(node.Props["radius"]))
	}
}

func (r *gioRenderer) paintRoundedBorder(bounds, currentClip image.Rectangle, value color.Color, thickness, radius int) {
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	pixelThickness := max(1, r.gtx.Dp(unit.Dp(thickness)))
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	roundStack := clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Push(r.gtx.Ops)
	colorValue := r.nrgba(value)
	for _, edge := range []image.Rectangle{
		image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y, pixelBounds.Max.X, pixelBounds.Min.Y+pixelThickness),
		image.Rect(pixelBounds.Min.X, pixelBounds.Max.Y-pixelThickness, pixelBounds.Max.X, pixelBounds.Max.Y),
		image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y+pixelThickness, pixelBounds.Min.X+pixelThickness, pixelBounds.Max.Y-pixelThickness),
		image.Rect(pixelBounds.Max.X-pixelThickness, pixelBounds.Min.Y+pixelThickness, pixelBounds.Max.X, pixelBounds.Max.Y-pixelThickness),
	} {
		paint.FillShape(r.gtx.Ops, colorValue, clip.Rect(edge).Op())
	}
	roundStack.Pop()
	clipStack.Pop()
}

func shadowLayers(bounds image.Rectangle, shadow map[string]any) []shadowLayer {
	base := color.NRGBAModel.Convert(colorValue(shadow["color"], color.Transparent)).(color.NRGBA)
	if base.A == 0 {
		return nil
	}
	offset := image.Pt(int(number(shadow["x"], 0)), int(number(shadow["y"], 0)))
	offsetBounds := bounds.Add(offset)
	blur := max(0, int(number(shadow["blur"], 0)))
	if blur == 0 {
		return []shadowLayer{{bounds: offsetBounds, color: base}}
	}
	count := min(16, max(8, blur/2))
	weightTotal := count * (count + 1) / 2
	layers := make([]shadowLayer, 0, count)
	for index := count; index >= 1; index-- {
		expansion := max(1, int(math.Ceil(float64(blur*index)/float64(count*2))))
		weight := count - index + 1
		layerColor := base
		layerColor.A = uint8(max(1, int(base.A)*weight/weightTotal))
		layers = append(layers, shadowLayer{
			bounds: offsetBounds.Inset(-expansion),
			color:  layerColor,
			radius: expansion,
		})
	}
	return layers
}

func (r *gioRenderer) paintBackground(bounds, currentClip image.Rectangle, value any, radius int) {
	gradient, ok := value.(map[string]any)
	if !ok {
		r.fillRounded(bounds, currentClip, colorValue(value, color.Transparent), radius)
		return
	}
	stops := anySlice(gradient["stops"])
	if len(stops) < 2 {
		return
	}
	first, _ := stops[0].(map[string]any)
	last, _ := stops[len(stops)-1].(map[string]any)
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	angle := number(gradient["angle"], 0) * math.Pi / 180
	center := f32.Pt(float32(pixelBounds.Min.X+pixelBounds.Max.X)/2, float32(pixelBounds.Min.Y+pixelBounds.Max.Y)/2)
	extent := float32(math.Abs(math.Cos(angle))*float64(pixelBounds.Dx())+math.Abs(math.Sin(angle))*float64(pixelBounds.Dy())) / 2
	vector := f32.Pt(float32(math.Cos(angle))*extent, float32(math.Sin(angle))*extent)
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	roundStack := clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Push(r.gtx.Ops)
	paint.LinearGradientOp{
		Stop1: center.Sub(vector), Color1: r.nrgba(colorValue(first["color"], color.Transparent)),
		Stop2: center.Add(vector), Color2: r.nrgba(colorValue(last["color"], color.Transparent)),
	}.Add(r.gtx.Ops)
	paint.PaintOp{}.Add(r.gtx.Ops)
	roundStack.Pop()
	clipStack.Pop()
}

func (r *gioRenderer) fillRounded(bounds, currentClip image.Rectangle, value color.Color, radius int) {
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	paint.FillShape(r.gtx.Ops, r.nrgba(value), clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Op(r.gtx.Ops))
	clipStack.Pop()
}

func (r *gioRenderer) fillRect(bounds, currentClip image.Rectangle, value color.Color) {
	target := r.pxRect(bounds.Intersect(currentClip))
	if !target.Empty() {
		paint.FillShape(r.gtx.Ops, r.nrgba(value), clip.Rect(target).Op())
	}
}

func (r *gioRenderer) nrgba(value color.Color) color.NRGBA {
	c := color.NRGBAModel.Convert(value).(color.NRGBA)
	opacity := clamp(r.opacity, 0, 1)
	c.A = uint8(math.Round(float64(c.A) * opacity))
	return c
}

func (r *gioRenderer) paintTextGio(node *project.Node, bounds, currentClip image.Rectangle) {
	label := r.label(node)
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	stack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	offset := op.Offset(pixelBounds.Min).Push(r.gtx.Ops)
	gtx := r.gtx
	gtx.Constraints = layout.Exact(pixelBounds.Size())
	label.Layout(gtx)
	offset.Pop()
	stack.Pop()
}

func (r *gioRenderer) label(node *project.Node) material.LabelStyle {
	content := stringValue(node.Props["text"], stringValue(node.Props["content"], ""))
	label := material.Label(r.theme, unit.Sp(number(node.Props["size"], 16)), content)
	label.Color = r.nrgba(colorValue(node.Props["color"], color.RGBA{A: 255}))
	if number(node.Props["weight"], 400) >= 600 {
		label.Font.Weight = font.Bold
	} else if number(node.Props["weight"], 400) >= 500 {
		label.Font.Weight = font.Medium
	}
	if boolValue(node.Props["italic"], false) {
		label.Font.Style = font.Italic
	}
	fontPath := stringValue(node.Props["font"], "")
	if token, ok := node.Props["font"].(map[string]any); ok {
		fontPath = stringValue(token["src"], "")
	}
	if fontPath != "" {
		if !filepath.IsAbs(fontPath) {
			fontPath = filepath.Join(filepath.Dir(node.Source.File), fontPath)
		}
		if shaper, typeface, err := loadNativeFont(r.theme, fontPath); err == nil {
			label.Shaper = shaper
			label.Font.Typeface = typeface
		}
	}
	switch stringValue(node.Props["alignment"], "start") {
	case "center":
		label.Alignment = text.Middle
	case "end", "right":
		label.Alignment = text.End
	default:
		label.Alignment = text.Start
	}
	label.MaxLines = int(number(node.Props["max_lines"], 0))
	if stringValue(node.Props["overflow"], "clip") == "ellipsis" {
		label.Truncator = "…"
	}
	if lineHeight := number(node.Props["line_height"], 0); lineHeight > 0 {
		label.LineHeight = unit.Sp(lineHeight)
	}
	return label
}

func loadNativeFont(theme *material.Theme, path string) (*text.Shaper, font.Typeface, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	key := nativeFontKey{theme: theme, path: path}
	nativeFontCache.Lock()
	cached, ok := nativeFontCache.fonts[key]
	nativeFontCache.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.shaper, cached.typeface, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	faces, err := gioopentype.ParseCollection(data)
	if err != nil {
		return nil, "", err
	}
	collection := make([]font.FontFace, 0, len(faces)+len(gofont.Collection()))
	collection = append(collection, faces...)
	collection = append(collection, gofont.Collection()...)
	shaper := text.NewShaper(text.NoSystemFonts(), text.WithCollection(collection))
	typeface := font.Typeface("")
	if len(faces) > 0 {
		typeface = faces[0].Font.Typeface
	}
	nativeFontCache.Lock()
	nativeFontCache.fonts[key] = nativeFont{
		modified: info.ModTime(), size: info.Size(), shaper: shaper, typeface: typeface,
	}
	nativeFontCache.Unlock()
	return shaper, typeface, nil
}

func releaseNativeFonts(theme *material.Theme) {
	nativeFontCache.Lock()
	for key := range nativeFontCache.fonts {
		if key.theme == theme {
			delete(nativeFontCache.fonts, key)
		}
	}
	nativeFontCache.Unlock()
}

func (r *gioRenderer) intrinsicSize(node *project.Node, limit image.Point) image.Point {
	return measureIntrinsic(node, limit, r.intrinsicLeafSize)
}

func (r *gioRenderer) intrinsicLeafSize(node *project.Node, limit image.Point) image.Point {
	switch node.Type {
	case "text":
		label := r.label(node)
		gtx := r.gtx
		maximum := r.pxPoint(limit)
		if maximum.X <= 0 {
			maximum.X = 1 << 20
		}
		if maximum.Y <= 0 {
			maximum.Y = 1 << 20
		}
		gtx.Constraints = layout.Constraints{Max: maximum}
		recording := op.Record(gtx.Ops)
		dimensions := label.Layout(gtx)
		_ = recording.Stop()
		return r.logicalPoint(dimensions.Size)
	case "divider":
		thickness := max(1, int(number(node.Props["thickness"], 1)))
		if stringValue(node.Props["orientation"], "horizontal") == "vertical" {
			return image.Pt(thickness, limit.Y)
		}
		return image.Pt(limit.X, thickness)
	case "image":
		source := stringValue(node.Props["src"], "")
		if source != "" {
			if !filepath.IsAbs(source) {
				source = filepath.Join(filepath.Dir(node.Source.File), source)
			}
			if decoded, err := loadImage(source); err == nil {
				return decoded.Bounds().Size()
			}
		}
		return image.Point{}
	default:
		return image.Point{}
	}
}

func (r *gioRenderer) paintImageGio(node *project.Node, bounds, currentClip image.Rectangle) {
	source := stringValue(node.Props["src"], "")
	if source == "" {
		return
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(filepath.Dir(node.Source.File), source)
	}
	decoded, err := loadImage(source)
	if err != nil {
		return
	}
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	stack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	offset := op.Offset(pixelBounds.Min).Push(r.gtx.Ops)
	gtx := r.gtx
	gtx.Constraints = layout.Exact(pixelBounds.Size())
	imageWidget := widget.Image{
		Src:      paint.NewImageOp(decoded),
		Fit:      gioFit(stringValue(node.Props["fit"], "contain")),
		Position: gioDirection(stringValue(node.Props["alignment"], "center")),
		Scale:    1 / r.gtx.Metric.PxPerDp,
	}
	imageWidget.Layout(gtx)
	offset.Pop()
	stack.Pop()
}

func gioFit(value string) widget.Fit {
	switch value {
	case "cover":
		return widget.Cover
	case "fill":
		return widget.Fill
	default:
		return widget.Contain
	}
}

func gioDirection(value string) layout.Direction {
	switch value {
	case "top_left":
		return layout.NW
	case "top":
		return layout.N
	case "top_right":
		return layout.NE
	case "left":
		return layout.W
	case "right":
		return layout.E
	case "bottom_left":
		return layout.SW
	case "bottom":
		return layout.S
	case "bottom_right":
		return layout.SE
	default:
		return layout.Center
	}
}

func (r *gioRenderer) stackGio(node *project.Node, bounds, currentClip image.Rectangle) {
	children := visibleLayoutChildren(node.Children)
	clone := *node
	clone.Children = children
	for index, childBounds := range planStack(&clone, bounds, r.intrinsicSize) {
		r.layoutFinal(children[index], childBounds, currentClip)
	}
}

func alignCross(bounds image.Rectangle, size int, vertical bool, alignment string) image.Rectangle {
	if vertical {
		switch alignment {
		case "center":
			bounds.Min.X += (bounds.Dx() - size) / 2
		case "end":
			bounds.Min.X = bounds.Max.X - size
		}
		bounds.Max.X = bounds.Min.X + size
		return bounds
	}
	switch alignment {
	case "center":
		bounds.Min.Y += (bounds.Dy() - size) / 2
	case "end":
		bounds.Min.Y = bounds.Max.Y - size
	}
	bounds.Max.Y = bounds.Min.Y + size
	return bounds
}

func (r *gioRenderer) gridGio(node *project.Node, bounds, currentClip image.Rectangle) {
	children := visibleLayoutChildren(node.Children)
	gap := int(number(node.Props["gap"], 0))
	columnDefs := anySlice(node.Props["columns"])
	if len(columnDefs) == 0 {
		count := max(1, int(number(node.Props["columns"], 1)))
		columnDefs = make([]any, count)
		for index := range columnDefs {
			columnDefs[index] = "1fr"
		}
	}
	columns := len(columnDefs)
	rows := max(1, int(math.Ceil(float64(len(children))/float64(columns))))
	for index, child := range children {
		row := index / columns
		if value, ok := child.Place["row"]; ok {
			row = int(number(value, float64(row)))
		}
		rows = max(rows, row+max(1, int(number(child.Place["row_span"], 1))))
	}
	rowDefs := anySlice(node.Props["rows"])
	for len(rowDefs) < rows {
		rowDefs = append(rowDefs, "1fr")
	}
	columnSizes := tracks(columnDefs, bounds.Dx(), gap)
	rowSizes := tracks(rowDefs, bounds.Dy(), gap)
	for index, child := range children {
		column := index % columns
		row := index / columns
		if value, ok := child.Place["column"]; ok {
			column = int(number(value, float64(column)))
		}
		if value, ok := child.Place["row"]; ok {
			row = int(number(value, float64(row)))
		}
		column = max(0, min(column, columns-1))
		row = max(0, min(row, len(rowSizes)-1))
		columnSpan := max(1, min(int(number(child.Place["column_span"], 1)), columns-column))
		rowSpan := max(1, min(int(number(child.Place["row_span"], 1)), len(rowSizes)-row))
		x := bounds.Min.X + trackOffset(columnSizes, column, gap)
		y := bounds.Min.Y + trackOffset(rowSizes, row, gap)
		r.layout(child, image.Rect(
			x, y,
			x+trackSpan(columnSizes, column, columnSpan, gap),
			y+trackSpan(rowSizes, row, rowSpan, gap),
		), currentClip)
	}
}

func (r *gioRenderer) scrollGio(node *project.Node, bounds, currentClip image.Rectangle) {
	if len(node.Children) != 1 {
		return
	}
	offset := r.state.Scroll[scrollKey(node)]
	axis := stringValue(node.Props["axis"], "vertical")
	childBounds := bounds
	contentSize := scrollContentSize(node.Children[0], bounds, axis, r.intrinsicSize)
	if axis == "vertical" {
		offset.Y = min(max(0, offset.Y), contentSize-bounds.Dy())
		offset.X = 0
		childBounds = bounds.Sub(offset)
		childBounds.Max.Y = childBounds.Min.Y + contentSize
	} else {
		offset.X = min(max(0, offset.X), contentSize-bounds.Dx())
		offset.Y = 0
		childBounds = bounds.Sub(offset)
		childBounds.Max.X = childBounds.Min.X + contentSize
	}
	if r.scene != nil {
		visibleClip := currentClip.Intersect(bounds)
		contentClip := visibleClip
		if axis == "vertical" {
			childBounds = bounds
			childBounds.Max.Y = childBounds.Min.Y + contentSize
			contentClip.Min.Y = childBounds.Min.Y
			contentClip.Max.Y = childBounds.Max.Y
		} else {
			childBounds = bounds
			childBounds.Max.X = childBounds.Min.X + contentSize
			contentClip.Min.X = childBounds.Min.X
			contentClip.Max.X = childBounds.Max.X
		}
		scroll := sceneScroll{
			key:         scrollKey(node),
			axis:        axis,
			viewport:    bounds,
			contentSize: contentSize,
		}
		r.scrolls = append(r.scrolls, scroll)
		r.layoutFinal(node.Children[0], childBounds, contentClip)
		r.scrolls = r.scrolls[:len(r.scrolls)-1]
		if boolValue(node.Props["scrollbar"], false) && contentSize > scroll.viewportSize() {
			r.scene.items = append(r.scene.items, sceneItem{
				scrolls: append([]sceneScroll(nil), r.scrolls...),
				scrollbar: &sceneScrollbar{
					bounds:  bounds,
					clip:    currentClip,
					scroll:  scroll,
					opacity: r.opacity,
				},
			})
		}
		return
	}
	visibleClip, prepaintClip := scrollClips(currentClip.Intersect(bounds), childBounds, axis)
	hardClip := clip.Rect(r.pxRect(visibleClip)).Push(r.gtx.Ops)
	firstGeometry := len(r.geometryOrder)
	r.layoutFinal(node.Children[0], childBounds, prepaintClip)
	hardClip.Pop()
	for _, handle := range r.geometryOrder[firstGeometry:] {
		geometry := r.result.Geometry[handle]
		geometry.Clip = geometry.Clip.Intersect(visibleClip)
		r.result.Geometry[handle] = geometry
	}
	if boolValue(node.Props["scrollbar"], false) && contentSize > map[bool]int{true: bounds.Dy(), false: bounds.Dx()}[axis == "vertical"] {
		r.paintScrollbarGio(bounds, currentClip, axis, contentSize, offset)
	}
}

func scrollClips(viewport, content image.Rectangle, axis string) (visible, prepaint image.Rectangle) {
	return viewport, viewport
}

func (r *gioRenderer) paintScrollbarGio(bounds, currentClip image.Rectangle, axis string, contentSize int, offset image.Point) {
	const thickness = 5
	if axis == "vertical" {
		thumbSize := max(18, bounds.Dy()*bounds.Dy()/contentSize)
		position := offset.Y * (bounds.Dy() - thumbSize) / (contentSize - bounds.Dy())
		r.fillRounded(image.Rect(bounds.Max.X-thickness-2, bounds.Min.Y+position, bounds.Max.X-2, bounds.Min.Y+position+thumbSize), currentClip, color.NRGBA{R: 80, G: 88, B: 104, A: 130}, thickness/2)
		return
	}
	thumbSize := max(18, bounds.Dx()*bounds.Dx()/contentSize)
	position := offset.X * (bounds.Dx() - thumbSize) / (contentSize - bounds.Dx())
	r.fillRounded(image.Rect(bounds.Min.X+position, bounds.Max.Y-thickness-2, bounds.Min.X+position+thumbSize, bounds.Max.Y-2), currentClip, color.NRGBA{R: 80, G: 88, B: 104, A: 130}, thickness/2)
}

func (r *gioRenderer) paintDividerGio(node *project.Node, bounds, currentClip image.Rectangle) {
	thickness := max(1, int(number(node.Props["thickness"], 1)))
	line := bounds
	if stringValue(node.Props["orientation"], "horizontal") == "vertical" {
		line.Max.X = min(line.Max.X, line.Min.X+thickness)
	} else {
		line.Max.Y = min(line.Max.Y, line.Min.Y+thickness)
	}
	r.fillRect(line, currentClip, colorValue(node.Props["color"], color.RGBA{A: 255}))
}

func (r *gioRenderer) pxRect(value image.Rectangle) image.Rectangle {
	return image.Rectangle{Min: r.pxPoint(value.Min), Max: r.pxPoint(value.Max)}
}

func (r *gioRenderer) pxPoint(value image.Point) image.Point {
	return image.Pt(r.gtx.Dp(unit.Dp(value.X)), r.gtx.Dp(unit.Dp(value.Y)))
}

func (r *gioRenderer) logicalPoint(value image.Point) image.Point {
	scale := r.gtx.Metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	return image.Pt(
		int(math.Ceil(float64(float32(value.X)/scale))),
		int(math.Ceil(float64(float32(value.Y)/scale))),
	)
}
