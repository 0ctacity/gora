package render

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"gora/internal/document"
	"gora/internal/project"
)

// State contains the ephemeral values that are intentionally absent from .gora files.
type State struct {
	Scroll  map[string]image.Point
	Values  map[string]map[string]any
	Hovered string
	Pressed string
	Focused string
}

type Inspection struct {
	Handle     string
	Type       string
	Name       string
	Bounds     image.Rectangle
	Clip       image.Rectangle
	Props      map[string]any
	Source     string
	Line       int
	Column     int
	Breadcrumb []string
	Role       string
	Label      string
	Enabled    bool
	Scope      string
	Actions    []document.Action
	State      map[string]any
	Hovered    bool
	Pressed    bool
	Focused    bool
}

type InteractionRegion struct {
	Handle   string
	Bounds   image.Rectangle
	Clip     image.Rectangle
	Scope    string
	Label    string
	Disabled bool
	Actions  []document.Action
}

type Result struct {
	Image        *image.RGBA
	Bounds       map[string]image.Rectangle
	Inspections  []Inspection
	Interactions []InteractionRegion
}

type renderer struct {
	result  *Result
	state   State
	scale   int
	opacity float64
}

type cachedImage struct {
	modified time.Time
	size     int64
	image    image.Image
}

type cachedFont struct {
	modified time.Time
	size     int64
	font     *opentype.Font
}

var assetCache = struct {
	sync.Mutex
	images map[string]cachedImage
	fonts  map[string]cachedFont
}{images: make(map[string]cachedImage), fonts: make(map[string]cachedFont)}

var fallbackRegular = sync.OnceValue(func() *opentype.Font {
	parsed, _ := opentype.Parse(goregular.TTF)
	return parsed
})
var fallbackBold = sync.OnceValue(func() *opentype.Font {
	parsed, _ := opentype.Parse(gobold.TTF)
	return parsed
})
var fallbackItalic = sync.OnceValue(func() *opentype.Font {
	parsed, _ := opentype.Parse(goitalic.TTF)
	return parsed
})
var fallbackBoldItalic = sync.OnceValue(func() *opentype.Font {
	parsed, _ := opentype.Parse(gobolditalic.TTF)
	return parsed
})

func Render(root *project.Node, viewport image.Point, state State) Result {
	return renderScaled(root, viewport, state, 1)
}

func renderScaled(root *project.Node, viewport image.Point, state State, scale int) Result {
	if scale < 1 {
		scale = 1
	}
	canvas := image.NewRGBA(image.Rect(0, 0, viewport.X*scale, viewport.Y*scale))
	result := Result{Image: canvas, Bounds: make(map[string]image.Rectangle)}
	r := renderer{result: &result, state: state, scale: scale, opacity: 1}
	r.layout(root, image.Rect(0, 0, viewport.X, viewport.Y), image.Rect(0, 0, viewport.X, viewport.Y))
	return result
}

func Capture(path string, root *project.Node, viewport image.Point, state State, scale int) error {
	if scale <= 0 {
		return errors.New("scale must be a positive integer")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	captured, err := captureGio(root, viewport, state, scale)
	if err != nil {
		return err
	}
	if err := png.Encode(file, captured); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func (r *renderer) layout(node *project.Node, bounds, clip image.Rectangle) {
	r.layoutNode(node, bounds, clip, false)
}

func (r *renderer) layoutFinal(node *project.Node, bounds, clip image.Rectangle) {
	r.layoutNode(node, bounds, clip, true)
}

func (r *renderer) layoutNode(node *project.Node, bounds, clip image.Rectangle, final bool) {
	if node == nil || bounds.Empty() {
		return
	}
	if !final {
		bounds = applySize(node, bounds)
	}
	node = buttonNodeForState(node, r.state)
	incomingClip := clip
	clip = clip.Intersect(bounds)
	previousOpacity := r.opacity
	r.opacity *= clamp(number(node.Props["opacity"], 1), 0, 1)
	defer func() { r.opacity = previousOpacity }()
	r.result.Bounds[node.Handle] = bounds
	inspection := Inspection{
		Handle: node.Handle, Type: node.Type, Name: node.Name, Bounds: bounds, Clip: clip,
		Props: cloneMap(node.Props), Source: node.Source.File, Line: node.Source.Line,
		Column: node.Source.Column, Breadcrumb: append([]string(nil), node.Breadcrumb...),
	}
	if node.Type == "button" {
		disabled := boolValue(node.Props["disabled"], false)
		label := stringValue(node.Props["label"], "")
		inspection.Role, inspection.Label, inspection.Enabled, inspection.Scope = "button", label, !disabled, node.Scope
		inspection.Actions = append([]document.Action(nil), node.On.Activate...)
		inspection.State = cloneMap(r.state.Values[node.Scope])
		inspection.Hovered = r.state.Hovered == node.Handle
		inspection.Pressed = r.state.Pressed == node.Handle
		inspection.Focused = r.state.Focused == node.Handle
		r.result.Interactions = append(r.result.Interactions, InteractionRegion{
			Handle: node.Handle, Bounds: bounds, Clip: clip, Scope: node.Scope, Label: label,
			Disabled: disabled, Actions: append([]document.Action(nil), node.On.Activate...),
		})
	}
	r.result.Inspections = append(r.result.Inspections, inspection)

	switch node.Type {
	case "_viewport":
		if gradient, ok := node.Props["background"].(map[string]any); ok {
			r.paintGradient(bounds, clip, gradient, 0)
		} else {
			r.paintRect(bounds, clip, colorValue(node.Props["background"], color.Transparent), 1)
		}
		if len(node.Children) == 1 {
			r.layout(node.Children[0], bounds, clip)
		}
	case "surface", "button":
		r.paintSurface(node, bounds, incomingClip)
		inner := inset(bounds, insets(node.Props["padding"]))
		if len(node.Children) == 1 {
			r.layout(node.Children[0], inner, chooseClip(node, incomingClip, bounds))
		}
	case "stack":
		r.stack(node, bounds, clip)
	case "grid":
		r.grid(node, bounds, clip)
	case "overlay":
		for _, child := range node.Children {
			r.layout(child, overlayPlace(child, bounds), clip)
		}
	case "scroll":
		if len(node.Children) == 1 {
			offset := r.state.Scroll[scrollKey(node)]
			axis := stringValue(node.Props["axis"], "vertical")
			childBounds := bounds
			contentSize := scrollContentSize(node.Children[0], bounds, axis, cpuIntrinsicSize)
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
			r.layoutFinal(node.Children[0], childBounds, clip.Intersect(bounds))
			if boolValue(node.Props["scrollbar"], false) && contentSize > func() int {
				if axis == "vertical" {
					return bounds.Dy()
				}
				return bounds.Dx()
			}() {
				r.paintScrollbar(bounds, clip, axis, contentSize, offset)
			}
		}
	case "divider":
		thickness := max(1, int(number(node.Props["thickness"], 1)))
		line := bounds
		if stringValue(node.Props["orientation"], "horizontal") == "vertical" {
			line.Max.X = min(line.Max.X, line.Min.X+thickness)
		} else {
			line.Max.Y = min(line.Max.Y, line.Min.Y+thickness)
		}
		r.paintRect(line, clip, colorValue(node.Props["color"], color.RGBA{A: 255}), 1)
	case "text":
		r.text(node, bounds, clip)
	case "image":
		r.image(node, bounds, clip)
	default:
		for _, child := range node.Children {
			r.layout(child, place(child, bounds), clip)
		}
	}
}

func buttonNodeForState(node *project.Node, state State) *project.Node {
	if node == nil || node.Type != "button" {
		return node
	}
	props := node.Props
	changed := false
	for _, variant := range node.Variants {
		matched := false
		switch variant.When.Interaction {
		case "hovered":
			matched = state.Hovered == node.Handle
		case "pressed":
			matched = state.Pressed == node.Handle
		case "focused":
			matched = state.Focused == node.Handle
		case "disabled":
			matched = boolValue(props["disabled"], false)
		}
		if !matched {
			continue
		}
		if !changed {
			props = cloneMap(props)
			changed = true
		}
		for key, value := range variant.Props {
			props[key] = value
		}
	}
	if !changed {
		return node
	}
	clone := *node
	clone.Props = props
	return &clone
}

func (r *renderer) paintScrollbar(bounds, clip image.Rectangle, axis string, contentSize int, offset image.Point) {
	const thickness = 5
	if axis == "vertical" {
		thumbSize := max(18, bounds.Dy()*bounds.Dy()/contentSize)
		travel := bounds.Dy() - thumbSize
		position := 0
		if contentSize > bounds.Dy() {
			position = offset.Y * travel / (contentSize - bounds.Dy())
		}
		r.paintRounded(image.Rect(bounds.Max.X-thickness-2, bounds.Min.Y+position, bounds.Max.X-2, bounds.Min.Y+position+thumbSize), clip, color.RGBA{R: 80, G: 88, B: 104, A: 130}, thickness/2)
		return
	}
	thumbSize := max(18, bounds.Dx()*bounds.Dx()/contentSize)
	travel := bounds.Dx() - thumbSize
	position := 0
	if contentSize > bounds.Dx() {
		position = offset.X * travel / (contentSize - bounds.Dx())
	}
	r.paintRounded(image.Rect(bounds.Min.X+position, bounds.Max.Y-thickness-2, bounds.Min.X+position+thumbSize, bounds.Max.Y-2), clip, color.RGBA{R: 80, G: 88, B: 104, A: 130}, thickness/2)
}

func (r *renderer) paintSurface(node *project.Node, bounds, clip image.Rectangle) {
	if shadow, ok := node.Props["shadow"].(map[string]any); ok {
		shadowBounds := bounds.Add(image.Pt(int(number(shadow["x"], 0)), int(number(shadow["y"], 0))))
		r.paintRect(shadowBounds, clip, colorValue(shadow["color"], color.Transparent), 1)
	}
	if gradient, ok := node.Props["background"].(map[string]any); ok {
		r.paintGradient(bounds, clip, gradient, radiusValue(node.Props["radius"]))
	} else {
		r.paintRounded(bounds, clip, colorValue(node.Props["background"], color.Transparent), radiusValue(node.Props["radius"]))
	}
	if border, ok := node.Props["border"].(map[string]any); ok {
		thickness := max(1, int(number(border["thickness"], 1)))
		value := colorValue(border["color"], color.RGBA{A: 255})
		r.paintRect(image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Min.Y+thickness), clip, value, 1)
		r.paintRect(image.Rect(bounds.Min.X, bounds.Max.Y-thickness, bounds.Max.X, bounds.Max.Y), clip, value, 1)
		r.paintRect(image.Rect(bounds.Min.X, bounds.Min.Y+thickness, bounds.Min.X+thickness, bounds.Max.Y-thickness), clip, value, 1)
		r.paintRect(image.Rect(bounds.Max.X-thickness, bounds.Min.Y+thickness, bounds.Max.X, bounds.Max.Y-thickness), clip, value, 1)
	}
}

func (r *renderer) paintGradient(bounds, clip image.Rectangle, gradient map[string]any, radius int) {
	stops := anySlice(gradient["stops"])
	if len(stops) < 2 {
		return
	}
	target := scaleRect(bounds.Intersect(clip), r.scale)
	if target.Empty() {
		return
	}
	angle := number(gradient["angle"], 0) * math.Pi / 180
	dx, dy := math.Cos(angle), math.Sin(angle)
	extent := math.Abs(dx)*float64(max(1, target.Dx()-1)) + math.Abs(dy)*float64(max(1, target.Dy()-1))
	centerX := float64(target.Min.X+target.Max.X-1) / 2
	centerY := float64(target.Min.Y+target.Max.Y-1) / 2
	for y := target.Min.Y; y < target.Max.Y; y++ {
		for x := target.Min.X; x < target.Max.X; x++ {
			if !insideRounded(image.Pt(x, y), scaleRect(bounds, r.scale), radius*r.scale) {
				continue
			}
			ratio := .5 + ((float64(x)-centerX)*dx+(float64(y)-centerY)*dy)/extent
			value := gradientColor(stops, clamp(ratio, 0, 1))
			value = withOpacity(value, r.opacity)
			r.result.Image.SetRGBA(x, y, composite(r.result.Image.RGBAAt(x, y), value))
		}
	}
}

func gradientColor(stops []any, ratio float64) color.RGBA {
	first, _ := stops[0].(map[string]any)
	fromOffset := number(first["offset"], 0)
	from := color.RGBAModel.Convert(colorValue(first["color"], color.Transparent)).(color.RGBA)
	for index := 1; index < len(stops); index++ {
		stop, _ := stops[index].(map[string]any)
		toOffset := number(stop["offset"], 1)
		to := color.RGBAModel.Convert(colorValue(stop["color"], color.Transparent)).(color.RGBA)
		if ratio <= toOffset || index == len(stops)-1 {
			local := 0.0
			if toOffset > fromOffset {
				local = clamp((ratio-fromOffset)/(toOffset-fromOffset), 0, 1)
			}
			return color.RGBA{
				R: uint8(float64(from.R)*(1-local) + float64(to.R)*local),
				G: uint8(float64(from.G)*(1-local) + float64(to.G)*local),
				B: uint8(float64(from.B)*(1-local) + float64(to.B)*local),
				A: uint8(float64(from.A)*(1-local) + float64(to.A)*local),
			}
		}
		fromOffset, from = toOffset, to
	}
	return from
}

func (r *renderer) paintRounded(bounds, clip image.Rectangle, value color.Color, radius int) {
	if radius <= 0 {
		r.paintRect(bounds, clip, value, 1)
		return
	}
	target := scaleRect(bounds.Intersect(clip), r.scale)
	full := scaleRect(bounds, r.scale)
	c := color.RGBAModel.Convert(value).(color.RGBA)
	c = withOpacity(c, r.opacity)
	for y := target.Min.Y; y < target.Max.Y; y++ {
		for x := target.Min.X; x < target.Max.X; x++ {
			if insideRounded(image.Pt(x, y), full, radius*r.scale) {
				r.result.Image.SetRGBA(x, y, composite(r.result.Image.RGBAAt(x, y), c))
			}
		}
	}
}

func insideRounded(point image.Point, bounds image.Rectangle, radius int) bool {
	radius = min(radius, min(bounds.Dx(), bounds.Dy())/2)
	if radius <= 0 {
		return true
	}
	centerX := point.X
	centerY := point.Y
	if point.X < bounds.Min.X+radius {
		centerX = bounds.Min.X + radius
	} else if point.X >= bounds.Max.X-radius {
		centerX = bounds.Max.X - radius - 1
	}
	if point.Y < bounds.Min.Y+radius {
		centerY = bounds.Min.Y + radius
	} else if point.Y >= bounds.Max.Y-radius {
		centerY = bounds.Max.Y - radius - 1
	}
	dx, dy := point.X-centerX, point.Y-centerY
	return dx*dx+dy*dy <= radius*radius
}

func radiusValue(value any) int {
	if radius := int(number(value, 0)); radius > 0 {
		return radius
	}
	values, _ := value.(map[string]any)
	radius := 0
	for _, key := range []string{"top_left", "top_right", "bottom_right", "bottom_left"} {
		radius = max(radius, int(number(values[key], 0)))
	}
	return radius
}

func composite(destination, source color.RGBA) color.RGBA {
	inverse := uint32(255 - source.A)
	return color.RGBA{
		R: uint8(min(255, int(uint32(source.R)+uint32(destination.R)*inverse/255))),
		G: uint8(min(255, int(uint32(source.G)+uint32(destination.G)*inverse/255))),
		B: uint8(min(255, int(uint32(source.B)+uint32(destination.B)*inverse/255))),
		A: uint8(min(255, int(uint32(source.A)+uint32(destination.A)*inverse/255))),
	}
}

func withOpacity(value color.RGBA, opacity float64) color.RGBA {
	opacity = clamp(opacity, 0, 1)
	value.R = uint8(math.Round(float64(value.R) * opacity))
	value.G = uint8(math.Round(float64(value.G) * opacity))
	value.B = uint8(math.Round(float64(value.B) * opacity))
	value.A = uint8(math.Round(float64(value.A) * opacity))
	return value
}

func scrollKey(node *project.Node) string {
	if node.Name != "" {
		return node.Name
	}
	return node.Handle
}

func (r *renderer) text(node *project.Node, bounds, clip image.Rectangle) {
	content := stringValue(node.Props["text"], stringValue(node.Props["content"], ""))
	if content == "" {
		return
	}
	bold := number(node.Props["weight"], 400) >= 600
	italic := boolValue(node.Props["italic"], false)
	parsed := fallbackRegular()
	switch {
	case bold && italic:
		parsed = fallbackBoldItalic()
	case bold:
		parsed = fallbackBold()
	case italic:
		parsed = fallbackItalic()
	}
	fontPath := stringValue(node.Props["font"], "")
	if fontToken, ok := node.Props["font"].(map[string]any); ok {
		fontPath = stringValue(fontToken["src"], "")
	}
	if fontPath != "" {
		path := fontPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(node.Source.File), path)
		}
		if loaded, err := loadFont(path); err == nil {
			parsed = loaded
		}
	}
	if parsed == nil {
		return
	}
	size := number(node.Props["size"], 16) * float64(r.scale)
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer face.Close()
	target := scaleRect(bounds.Intersect(clip), r.scale)
	mask := image.NewRGBA(r.result.Image.Bounds())
	textColor := color.RGBAModel.Convert(colorValue(node.Props["color"], color.RGBA{A: 255})).(color.RGBA)
	textColor = withOpacity(textColor, r.opacity)
	drawer := font.Drawer{Dst: mask, Src: image.NewUniform(textColor), Face: face}
	metrics := face.Metrics()
	y := target.Min.Y + metrics.Ascent.Ceil()
	lineHeight := int(math.Round(number(node.Props["line_height"], float64(metrics.Height.Ceil())/float64(r.scale)) * float64(r.scale)))
	lines := wrapText(face, content, target.Dx(), boolValue(node.Props["wrap"], true))
	maxLines := int(number(node.Props["max_lines"], 0))
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		if stringValue(node.Props["overflow"], "clip") == "ellipsis" {
			lines[len(lines)-1] = ellipsize(face, lines[len(lines)-1], target.Dx())
		}
	}
	for _, line := range lines {
		letterSpacing := int(math.Round(number(node.Props["letter_spacing"], 0) * float64(r.scale)))
		lineWidth := measuredLine(face, line, letterSpacing)
		x := target.Min.X
		switch stringValue(node.Props["alignment"], "start") {
		case "center":
			x += (target.Dx() - lineWidth) / 2
		case "end", "right":
			x += target.Dx() - lineWidth
		}
		drawer.Dot = fixed.P(x, y)
		drawSpaced(&drawer, line, letterSpacing)
		y += lineHeight
		if y > target.Max.Y {
			break
		}
	}
	draw.Draw(r.result.Image, target, mask, target.Min, draw.Over)
}

func measuredLine(face font.Face, value string, spacing int) int {
	width := font.MeasureString(face, value).Ceil()
	if count := len([]rune(value)); count > 1 {
		width += spacing * (count - 1)
	}
	return width
}

func drawSpaced(drawer *font.Drawer, value string, spacing int) {
	if spacing == 0 {
		drawer.DrawString(value)
		return
	}
	for _, character := range value {
		drawer.DrawString(string(character))
		drawer.Dot.X += fixed.I(spacing)
	}
}

func wrapText(face font.Face, content string, width int, enabled bool) []string {
	var lines []string
	for _, paragraph := range strings.Split(content, "\n") {
		if !enabled || font.MeasureString(face, paragraph).Ceil() <= width {
			lines = append(lines, paragraph)
			continue
		}
		words := strings.Fields(paragraph)
		current := ""
		for _, word := range words {
			candidate := word
			if current != "" {
				candidate = current + " " + word
			}
			if current != "" && font.MeasureString(face, candidate).Ceil() > width {
				lines = append(lines, current)
				current = word
			} else {
				current = candidate
			}
		}
		lines = append(lines, current)
	}
	return lines
}

func ellipsize(face font.Face, value string, width int) string {
	const suffix = "…"
	for value != "" && font.MeasureString(face, value+suffix).Ceil() > width {
		runes := []rune(value)
		value = string(runes[:len(runes)-1])
	}
	return value + suffix
}

func (r *renderer) image(node *project.Node, bounds, clip image.Rectangle) {
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
	target := scaleRect(bounds.Intersect(clip), r.scale)
	if target.Empty() {
		return
	}
	fit := stringValue(node.Props["fit"], "contain")
	destination := fittedRect(decoded.Bounds().Size(), target, fit, stringValue(node.Props["alignment"], "center"))
	layer := image.NewRGBA(r.result.Image.Bounds())
	xdraw.CatmullRom.Scale(layer, destination, decoded, decoded.Bounds(), draw.Src, nil)
	paintArea := destination.Intersect(target)
	if r.opacity >= 1 {
		draw.Draw(r.result.Image, paintArea, layer, paintArea.Min, draw.Over)
		return
	}
	mask := image.NewUniform(color.Alpha{A: uint8(255 * r.opacity)})
	draw.DrawMask(r.result.Image, paintArea, layer, paintArea.Min, mask, image.Point{}, draw.Over)
}

func loadImage(path string) (image.Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	assetCache.Lock()
	cached, ok := assetCache.images[path]
	assetCache.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.image, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	decoded, _, decodeErr := image.Decode(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return nil, decodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	assetCache.Lock()
	assetCache.images[path] = cachedImage{modified: info.ModTime(), size: info.Size(), image: decoded}
	assetCache.Unlock()
	return decoded, nil
}

func loadFont(path string) (*opentype.Font, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	assetCache.Lock()
	cached, ok := assetCache.fonts[path]
	assetCache.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.font, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return nil, err
	}
	assetCache.Lock()
	assetCache.fonts[path] = cachedFont{modified: info.ModTime(), size: info.Size(), font: parsed}
	assetCache.Unlock()
	return parsed, nil
}

func fittedRect(source image.Point, target image.Rectangle, fit, alignment string) image.Rectangle {
	if fit == "fill" || source.X <= 0 || source.Y <= 0 {
		return target
	}
	scaleX := float64(target.Dx()) / float64(source.X)
	scaleY := float64(target.Dy()) / float64(source.Y)
	scale := math.Min(scaleX, scaleY)
	if fit == "cover" {
		scale = math.Max(scaleX, scaleY)
	}
	width := int(math.Round(float64(source.X) * scale))
	height := int(math.Round(float64(source.Y) * scale))
	x := target.Min.X + (target.Dx()-width)/2
	y := target.Min.Y + (target.Dy()-height)/2
	switch alignment {
	case "top_left":
		x, y = target.Min.X, target.Min.Y
	case "top":
		y = target.Min.Y
	case "top_right":
		x, y = target.Max.X-width, target.Min.Y
	case "left":
		x = target.Min.X
	case "right":
		x = target.Max.X - width
	case "bottom_left":
		x, y = target.Min.X, target.Max.Y-height
	case "bottom":
		y = target.Max.Y - height
	case "bottom_right":
		x, y = target.Max.X-width, target.Max.Y-height
	}
	return image.Rect(x, y, x+width, y+height)
}

func (r *renderer) stack(node *project.Node, bounds, clip image.Rectangle) {
	for index, childBounds := range planStack(node, bounds, cpuIntrinsicSize) {
		r.layoutFinal(node.Children[index], childBounds, clip)
	}
}

func cpuIntrinsicSize(node *project.Node, limit image.Point) image.Point {
	return measureIntrinsic(node, limit, cpuIntrinsicLeaf)
}

func cpuIntrinsicLeaf(node *project.Node, limit image.Point) image.Point {
	if node.Type == "image" {
		source := stringValue(node.Props["src"], "")
		if source != "" {
			if !filepath.IsAbs(source) {
				source = filepath.Join(filepath.Dir(node.Source.File), source)
			}
			if decoded, err := loadImage(source); err == nil {
				return decoded.Bounds().Size()
			}
		}
	}
	return image.Pt(intrinsicMainSize(node, false), intrinsicMainSize(node, true))
}

func intrinsicMainSize(node *project.Node, vertical bool) int {
	if vertical {
		switch node.Type {
		case "text":
			lines := strings.Count(stringValue(node.Props["text"], stringValue(node.Props["content"], "")), "\n") + 1
			return int(math.Ceil(number(node.Props["line_height"], number(node.Props["size"], 16)*1.3))) * lines
		case "divider":
			if stringValue(node.Props["orientation"], "horizontal") == "horizontal" {
				return max(1, int(number(node.Props["thickness"], 1)))
			}
		}
	} else if node.Type == "text" {
		content := stringValue(node.Props["text"], stringValue(node.Props["content"], ""))
		return int(math.Ceil(float64(len([]rune(content))) * number(node.Props["size"], 16) * .62))
	}
	return 0
}

func (r *renderer) grid(node *project.Node, bounds, clip image.Rectangle) {
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
	rows := max(1, int(math.Ceil(float64(len(node.Children))/float64(columns))))
	for index, child := range node.Children {
		row := index / columns
		if value, ok := child.Place["row"]; ok {
			row = int(number(value, float64(row)))
		}
		span := max(1, int(number(child.Place["row_span"], 1)))
		rows = max(rows, row+span)
	}
	rowDefs := anySlice(node.Props["rows"])
	for len(rowDefs) < rows {
		rowDefs = append(rowDefs, "1fr")
	}
	columnSizes := tracks(columnDefs, bounds.Dx(), gap)
	rowSizes := tracks(rowDefs, bounds.Dy(), gap)
	for index, child := range node.Children {
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
		width := trackSpan(columnSizes, column, columnSpan, gap)
		height := trackSpan(rowSizes, row, rowSpan, gap)
		childBounds := image.Rect(
			x, y, x+width, y+height,
		)
		r.layout(child, childBounds, clip)
	}
}

func tracks(definitions []any, total, gap int) []int {
	sizes := make([]int, len(definitions))
	remaining := max(0, total-gap*max(0, len(definitions)-1))
	fractionTotal := 0.0
	for index, definition := range definitions {
		switch value := definition.(type) {
		case int64:
			sizes[index] = int(value)
			remaining -= sizes[index]
		case float64:
			sizes[index] = int(value)
			remaining -= sizes[index]
		case string:
			fractionTotal += fraction(value)
		default:
			fractionTotal++
		}
	}
	remaining = max(0, remaining)
	assigned := 0
	var flexible []int
	for index, definition := range definitions {
		if sizes[index] == 0 {
			flexible = append(flexible, index)
			_ = definition
		}
	}
	for position, index := range flexible {
		weight := fraction(definitions[index])
		if weight <= 0 {
			weight = 1
		}
		size := 0
		if position == len(flexible)-1 {
			size = remaining - assigned
		} else if fractionTotal > 0 {
			size = int(math.Ceil(float64(remaining) * weight / fractionTotal))
			assigned += size
		}
		sizes[index] = max(0, size)
	}
	return sizes
}

func fraction(value any) float64 {
	text, ok := value.(string)
	if !ok {
		return 1
	}
	if text == "auto" {
		return 1
	}
	if !strings.HasSuffix(text, "fr") {
		return 1
	}
	weight, err := strconv.ParseFloat(strings.TrimSuffix(text, "fr"), 64)
	if err != nil || weight <= 0 {
		return 1
	}
	return weight
}

func trackOffset(sizes []int, index, gap int) int {
	offset := 0
	for position := 0; position < index; position++ {
		offset += sizes[position] + gap
	}
	return offset
}

func trackSpan(sizes []int, index, span, gap int) int {
	total := gap * max(0, span-1)
	for position := index; position < index+span; position++ {
		total += sizes[position]
	}
	return total
}

func (r *renderer) paintRect(bounds, clip image.Rectangle, value color.Color, opacity float64) {
	target := scaleRect(bounds.Intersect(clip), r.scale)
	if target.Empty() {
		return
	}
	c := color.RGBAModel.Convert(value).(color.RGBA)
	c = withOpacity(c, clamp(opacity, 0, 1)*r.opacity)
	draw.Draw(r.result.Image, target, &image.Uniform{C: c}, image.Point{}, draw.Over)
}

func applySize(node *project.Node, bounds image.Rectangle) image.Rectangle {
	parentWidth, parentHeight := bounds.Dx(), bounds.Dy()
	width, widthDefinite := resolveDimension(node.Props["width"], parentWidth)
	height, heightDefinite := resolveDimension(node.Props["height"], parentHeight)
	if ratio, ok := aspectRatio(node.Props["aspect_ratio"]); ok {
		if widthDefinite && !heightDefinite {
			height, heightDefinite = rounded(float64(width)/ratio), true
		} else if heightDefinite && !widthDefinite {
			width, widthDefinite = rounded(float64(height)*ratio), true
		}
	}
	if widthDefinite {
		bounds.Max.X = bounds.Min.X + width
	}
	if heightDefinite {
		bounds.Max.Y = bounds.Min.Y + height
	}
	minWidth, _ := resolveDimension(node.Props["min_width"], parentWidth)
	maxWidth, hasMaxWidth := resolveDimension(node.Props["max_width"], parentWidth)
	minHeight, _ := resolveDimension(node.Props["min_height"], parentHeight)
	maxHeight, hasMaxHeight := resolveDimension(node.Props["max_height"], parentHeight)
	width, height = bounds.Dx(), bounds.Dy()
	width = clampDimension(width, minWidth, maxWidth, hasMaxWidth)
	height = clampDimension(height, minHeight, maxHeight, hasMaxHeight)
	bounds.Max = bounds.Min.Add(image.Pt(width, height))
	return bounds
}

func place(node *project.Node, parent image.Rectangle) image.Rectangle {
	out := parent
	x := int(number(node.Place["x"], 0))
	y := int(number(node.Place["y"], 0))
	out = out.Add(image.Pt(x, y))
	if width := int(number(node.Props["width"], 0)); width > 0 {
		out.Max.X = out.Min.X + width
	}
	if height := int(number(node.Props["height"], 0)); height > 0 {
		out.Max.Y = out.Min.Y + height
	}
	return out
}

func overlayPlace(node *project.Node, parent image.Rectangle) image.Rectangle {
	width := int(number(node.Props["width"], float64(parent.Dx())))
	height := int(number(node.Props["height"], float64(parent.Dy())))
	x, y := parent.Min.X, parent.Min.Y
	switch stringValue(node.Place["alignment"], "top_left") {
	case "top":
		x += (parent.Dx() - width) / 2
	case "top_right":
		x += parent.Dx() - width
	case "left":
		y += (parent.Dy() - height) / 2
	case "center":
		x += (parent.Dx() - width) / 2
		y += (parent.Dy() - height) / 2
	case "right":
		x += parent.Dx() - width
		y += (parent.Dy() - height) / 2
	case "bottom_left":
		y += parent.Dy() - height
	case "bottom":
		x += (parent.Dx() - width) / 2
		y += parent.Dy() - height
	case "bottom_right":
		x += parent.Dx() - width
		y += parent.Dy() - height
	}
	x += int(number(node.Place["x"], number(node.Place["offset_x"], 0)))
	y += int(number(node.Place["y"], number(node.Place["offset_y"], 0)))
	return image.Rect(x, y, x+width, y+height)
}

type edges struct{ top, right, bottom, left int }

func insets(value any) edges {
	m, _ := value.(map[string]any)
	return edges{
		top: int(number(m["top"], 0)), right: int(number(m["right"], 0)),
		bottom: int(number(m["bottom"], 0)), left: int(number(m["left"], 0)),
	}
}

func inset(rect image.Rectangle, e edges) image.Rectangle {
	rect.Min.X += e.left
	rect.Min.Y += e.top
	rect.Max.X -= e.right
	rect.Max.Y -= e.bottom
	return rect
}

func chooseClip(node *project.Node, current, bounds image.Rectangle) image.Rectangle {
	if clipped, _ := node.Props["clip"].(bool); clipped {
		return current.Intersect(bounds)
	}
	return current
}

func colorValue(value any, fallback color.Color) color.Color {
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	if text == "transparent" {
		return color.Transparent
	}
	if !strings.HasPrefix(text, "#") || (len(text) != 7 && len(text) != 9) {
		return fallback
	}
	parsed, err := strconv.ParseUint(text[1:], 16, 32)
	if err != nil {
		return fallback
	}
	if len(text) == 7 {
		return color.NRGBA{R: uint8(parsed >> 16), G: uint8(parsed >> 8), B: uint8(parsed), A: 255}
	}
	return color.NRGBA{R: uint8(parsed >> 24), G: uint8(parsed >> 16), B: uint8(parsed >> 8), A: uint8(parsed)}
}

func number(value any, fallback float64) float64 {
	switch value := value.(type) {
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case float64:
		return value
	case float32:
		return float64(value)
	default:
		return fallback
	}
}

func stringValue(value any, fallback string) string {
	if value, ok := value.(string); ok {
		return value
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	if value, ok := value.(bool); ok {
		return value
	}
	return fallback
}

func anySlice(value any) []any {
	valueSlice, _ := value.([]any)
	return valueSlice
}

func scaleRect(rect image.Rectangle, scale int) image.Rectangle {
	return image.Rect(rect.Min.X*scale, rect.Min.Y*scale, rect.Max.X*scale, rect.Max.Y*scale)
}

func clamp(value, low, high float64) float64 {
	return math.Max(low, math.Min(high, value))
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func ValidateOutput(path string) error {
	if !strings.EqualFold(filepathExt(path), ".png") {
		return fmt.Errorf("output must use the .png extension")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("output already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func filepathExt(path string) string {
	index := strings.LastIndexByte(path, '.')
	if index < 0 {
		return ""
	}
	return path[index:]
}
