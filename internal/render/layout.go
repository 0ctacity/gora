package render

import (
	"image"
	"io"
	"math"
	"path/filepath"
	"sort"
	"sync"

	giofont "gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
	"gioui.org/text"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"gora/internal/project"
)

type intrinsicMeasure func(*project.Node, image.Point) image.Point

// paintOrderedChildren returns the immediate paint participants for one
// stacking context in their final order. It is intentionally kept as the
// small, direct-child primitive used by tests and by the context flattener
// below. Flow content remains in authored order; positioned contexts are
// grouped around it and sorted by z-index.
func paintOrderedChildren(children []*project.Node) []*project.Node {
	ordered := append([]*project.Node(nil), children...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftClass, leftZ := paintParticipantKey(ordered[i])
		rightClass, rightZ := paintParticipantKey(ordered[j])
		if leftClass != rightClass {
			return leftClass < rightClass
		}
		if leftClass == paintPositiveContext || leftClass == paintNegativeContext {
			return leftZ < rightZ
		}
		return false
	})
	return ordered
}

// paintContextChildren expands only through ordinary flow wrappers. Such
// wrappers do not establish a stacking context, so a sticky/fixed descendant
// belongs to the nearest real context owner rather than being trapped by the
// wrapper. Once a positioned context is encountered it is emitted atomically
// and its descendants are left for that context's own expansion.
func paintContextChildren(children []*project.Node) []*project.Node {
	participants := make([]*project.Node, 0, len(children))
	var collectPositioned func([]*project.Node)
	collectPositioned = func(nodes []*project.Node) {
		for _, child := range nodes {
			if child == nil || child.Hidden {
				continue
			}
			if isPositionedContext(child) {
				participants = append(participants, child)
				continue
			}
			collectPositioned(child.Children)
		}
	}
	for _, child := range children {
		if child == nil || child.Hidden {
			continue
		}
		if isPositionedContext(child) {
			participants = append(participants, child)
			continue
		}
		participants = append(participants, child)
		collectPositioned(child.Children)
	}
	return paintOrderedChildren(participants)
}

// paintChildrenForNode preserves ordinary source-order recursion inside a
// flow node, while expanding the nearest actual context owner (the root or a
// sticky/fixed node) through its non-positioned wrappers.
func paintChildrenForNode(node *project.Node, rootHandle string) []*project.Node {
	if node == nil {
		return nil
	}
	if node.Handle == rootHandle || isPositionedContext(node) {
		return paintContextChildren(node.Children)
	}
	children := make([]*project.Node, 0, len(node.Children))
	for _, child := range node.Children {
		if child == nil || child.Hidden || isPositionedContext(child) {
			continue
		}
		children = append(children, child)
	}
	return children
}

const (
	paintNegativeContext = iota
	paintFlowContent
	paintZeroContext
	paintPositiveContext
)

func paintParticipantKey(node *project.Node) (int, float64) {
	if node == nil || !isPositionedContext(node) {
		return paintFlowContent, 0
	}
	z := number(node.Place["z_index"], 0)
	if z < 0 {
		return paintNegativeContext, z
	}
	if z > 0 {
		return paintPositiveContext, z
	}
	return paintZeroContext, z
}

func isPositionedContext(node *project.Node) bool {
	return isStickyPositioned(node) || isFixedPositioned(node)
}

// sourceOrderRanks is independent of paint ordering. It preserves expanded
// authored order for inspection and focus metadata while renderers traverse
// immediate participants in final paint order.
func sourceOrderRanks(root *project.Node) map[string]int {
	ranks := make(map[string]int)
	order := 0
	var visit func(*project.Node)
	visit = func(node *project.Node) {
		if node == nil {
			return
		}
		if node.Handle != "" {
			ranks[node.Handle] = order
			order++
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root)
	return ranks
}

// LayoutRecord is the renderer-neutral positioning foundation. Normal is the
// immutable flow rectangle; Final is the rectangle used for the current frame.
// ParentInner and ScrollAncestors retain the containing context needed by
// sticky and fixed positioning while keeping normal and final rectangles
// separate.
type LayoutRecord struct {
	Normal             image.Rectangle
	Final              image.Rectangle
	ParentInner        image.Rectangle
	ScrollAncestors    []string
	SourceOrder        int
	ContainingViewport image.Rectangle
}

func measureIntrinsic(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	if node == nil || node.Hidden || isFixedPositioned(node) {
		return image.Point{}
	}
	padding := insets(node.Props["padding"])
	innerLimit := image.Pt(max(0, limit.X-padding.left-padding.right), max(0, limit.Y-padding.top-padding.bottom))
	var preferred image.Point
	switch node.Type {
	case "form", "surface", "button", "link", "toggle", "checkbox", "radio", "tab", "tab_panel", "option", "select_trigger", "field_support",
		"slider_track", "slider_fill", "slider_thumb", "stepper_decrement", "stepper_value", "stepper_increment", "_viewport":
		if len(node.Children) == 1 {
			preferred = measureIntrinsic(node.Children[0], innerLimit, leaf)
		}
		preferred.X += padding.left + padding.right
		preferred.Y += padding.top + padding.bottom
	case "stack", "radio_group", "select_popup", "stepper", "text_field", "text_area":
		if node.Type == "stepper" {
			clone := *node
			clone.Props = cloneMap(node.Props)
			clone.Props["direction"] = "horizontal"
			preferred = measureStackIntrinsic(&clone, innerLimit, leaf)
		} else if node.Type == "select_popup" {
			clone := *node
			clone.Props = cloneMap(node.Props)
			clone.Props["direction"] = "vertical"
			preferred = measureStackIntrinsic(&clone, innerLimit, leaf)
		} else if node.Type == "text_field" || node.Type == "text_area" {
			clone := *node
			clone.Props = cloneMap(node.Props)
			clone.Props["direction"] = "vertical"
			preferred = measureStackIntrinsic(&clone, innerLimit, leaf)
			preferred.Y += fieldLabelHeight(node)
		} else {
			preferred = measureStackIntrinsic(node, innerLimit, leaf)
		}
		preferred.X += padding.left + padding.right
		preferred.Y += padding.top + padding.bottom
	case "slider":
		preferred = image.Pt(minPositive(limit.X, 160), minPositive(limit.Y, 24))
	case "tabs":
		preferred = measureTabsIntrinsic(node, innerLimit, leaf)
	case "overlay":
		for _, child := range node.Children {
			if !isFlowPositioned(child) {
				continue
			}
			size := measureIntrinsic(child, innerLimit, leaf)
			x := int(number(child.Place["x"], number(child.Place["offset_x"], 0)))
			y := int(number(child.Place["y"], number(child.Place["offset_y"], 0)))
			preferred.X = max(preferred.X, x+size.X)
			preferred.Y = max(preferred.Y, y+size.Y)
		}
	case "grid":
		preferred = measureGridIntrinsic(node, innerLimit, leaf)
	case "scroll":
		if len(node.Children) == 1 {
			childLimit := innerLimit
			axis := stringValue(node.Props["axis"], "vertical")
			if axis == "vertical" {
				childLimit.Y = 1 << 20
			} else {
				childLimit.X = 1 << 20
				if axis == "both" {
					childLimit.Y = 1 << 20
				}
			}
			preferred = measureIntrinsic(node.Children[0], childLimit, leaf)
			preferred.X = minPositive(preferred.X, innerLimit.X)
			preferred.Y = minPositive(preferred.Y, innerLimit.Y)
			preferred.X += padding.left + padding.right
			preferred.Y += padding.top + padding.bottom
		}
	case "field_box":
		text := *node
		text.Type = "text"
		preferred = leaf(&text, innerLimit)
		preferred.X += padding.left + padding.right
		if boolValue(node.Props["field_multiline"], false) {
			geometry := newFieldTextGeometry(node, image.Rect(0, 0, max(1, limit.X), 1<<20))
			lines := max(1, len(geometry.lineWidths))
			lines = max(lines, int(number(node.Props["field_min_lines"], 1)))
			if maximum := int(number(node.Props["field_max_lines"], 0)); maximum > 0 {
				lines = min(lines, maximum)
			}
			preferred.Y = lines*geometry.LineHeight + padding.top + padding.bottom
		} else {
			preferred.Y += padding.top + padding.bottom
		}
	default:
		preferred = leaf(node, innerLimit)
	}
	return constrainIntrinsic(node, preferred, limit)
}

func fieldLabelHeight(node *project.Node) int {
	if stringValue(node.Props["label"], "") == "" {
		return 0
	}
	return 20 + int(number(node.Props["gap"], 6))
}

func fieldContentBounds(node *project.Node, bounds image.Rectangle) (image.Rectangle, image.Rectangle) {
	height := fieldLabelHeight(node)
	if height == 0 {
		return image.Rectangle{}, bounds
	}
	label := image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, min(bounds.Max.Y, bounds.Min.Y+20))
	content := bounds
	content.Min.Y = min(bounds.Max.Y, bounds.Min.Y+height)
	return label, content
}

func selectPopupBounds(node *project.Node, triggerBounds, viewport image.Rectangle, leaf intrinsicMeasure) (*project.Node, *project.Node, image.Rectangle) {
	var trigger, popup *project.Node
	for _, child := range node.Children {
		if child == nil || child.Hidden {
			continue
		}
		switch child.Type {
		case "select_trigger":
			trigger = child
		case "select_popup":
			popup = child
		}
	}
	if popup == nil {
		return trigger, nil, image.Rectangle{}
	}
	size := measureIntrinsic(popup, viewport.Size(), leaf)
	maxHeight := int(number(popup.Props["max_height"], 320))
	if maxHeight > 0 {
		size.Y = min(size.Y, maxHeight)
	}
	if boolValue(popup.Props["match_trigger_width"], true) {
		size.X = triggerBounds.Dx()
	}
	size.X = min(max(1, size.X), viewport.Dx())
	size.Y = min(max(1, size.Y), viewport.Dy())
	x := min(max(viewport.Min.X, triggerBounds.Min.X), viewport.Max.X-size.X)
	y := triggerBounds.Max.Y
	if y+size.Y > viewport.Max.Y {
		y = triggerBounds.Min.Y - size.Y
	}
	y = min(max(viewport.Min.Y, y), viewport.Max.Y-size.Y)
	return trigger, popup, image.Rect(x, y, x+size.X, y+size.Y)
}

type fieldTextPosition struct {
	line int
	x    int
}

type fieldRuneSegment struct {
	runeIndex int
	line      int
	startX    int
	endX      int
}

type fieldTextGeometry struct {
	Inner      image.Rectangle
	Advance    int
	LineHeight int
	OffsetX    int
	OffsetY    int
	positions  []fieldTextPosition
	lineWidths []int
	lineStarts []int
	lineEnds   []int
	segments   []fieldRuneSegment
	selection  image.Point
}

var fieldShapeState = struct {
	sync.Mutex
	fallback *text.Shaper
}{fallback: text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Collection()))}

func fieldNodeWithViewport(node *project.Node, bounds image.Rectangle) (*project.Node, fieldTextGeometry) {
	geometry := newFieldTextGeometry(node, bounds)
	clone := *node
	clone.Props = cloneMap(node.Props)
	clone.Props["internal_viewport_x"] = float64(geometry.OffsetX)
	clone.Props["internal_viewport_y"] = float64(geometry.OffsetY * geometry.LineHeight)
	clone.Props["internal_viewport_width"] = float64(geometry.Inner.Dx())
	clone.Props["internal_viewport_height"] = float64(geometry.Inner.Dy())
	return &clone, geometry
}

func newFieldTextGeometry(node *project.Node, bounds image.Rectangle) fieldTextGeometry {
	inner := inset(bounds, insets(node.Props["padding"]))
	size := number(node.Props["size"], 16)
	face := fieldGeometryFace(node, size)
	if closer, ok := face.(io.Closer); ok {
		defer closer.Close()
	}
	advance := max(1, font.MeasureString(face, "n").Ceil())
	lineHeight := max(1, int(math.Ceil(number(node.Props["line_height"], float64(face.Metrics().Height.Ceil())))))
	runes := []rune(stringValue(node.Props["text"], ""))
	multiline := boolValue(node.Props["field_multiline"], false)
	positions, lineWidths, lineStarts, lineEnds, segments := shapeFieldText(node, inner.Dx(), lineHeight, multiline, len(runes))
	start := min(max(0, int(number(node.Props["selection_start"], 0))), len(runes))
	end := min(max(0, int(number(node.Props["selection_end"], float64(start)))), len(runes))
	caret := positions[end]
	offsetX := max(0, int(number(node.Props["internal_offset_x"], 0)))
	offsetY := max(0, int(number(node.Props["internal_offset"], 0)))
	if !multiline {
		if caret.x < offsetX {
			offsetX = caret.x
		} else if caret.x >= offsetX+inner.Dx() {
			offsetX = caret.x - inner.Dx() + 1
		}
		offsetY = 0
	} else {
		visibleLines := max(1, inner.Dy()/lineHeight)
		offsetY = min(offsetY, max(0, len(lineWidths)-visibleLines))
		if !boolValue(node.Props["manual_internal_scroll"], false) {
			if caret.line < offsetY {
				offsetY = caret.line
			} else if caret.line >= offsetY+visibleLines {
				offsetY = caret.line - visibleLines + 1
			}
		}
		offsetX = 0
	}
	return fieldTextGeometry{
		Inner: inner, Advance: advance, LineHeight: lineHeight, OffsetX: offsetX, OffsetY: offsetY,
		positions: positions, lineWidths: lineWidths, lineStarts: lineStarts, lineEnds: lineEnds, segments: segments, selection: image.Pt(start, end),
	}
}

func shapeFieldText(node *project.Node, width, lineHeight int, multiline bool, runeCount int) ([]fieldTextPosition, []int, []int, []int, []fieldRuneSegment) {
	shaper, descriptor := fieldTextShaper(node)
	maxWidth := 1 << 20
	if multiline {
		maxWidth = max(1, width)
	}
	params := text.Parameters{
		Font: descriptor, PxPerEm: fixed.I(max(1, int(math.Round(number(node.Props["size"], 16))))),
		MaxWidth: maxWidth, WrapPolicy: text.WrapHeuristically, Locale: system.Locale{Direction: system.LTR},
		LineHeight: fixed.I(lineHeight), LineHeightScale: 1, DisableSpaceTrim: true,
	}
	fieldShapeState.Lock()
	shaper.LayoutString(params, stringValue(node.Props["text"], ""))
	index := newFieldGlyphIndex(runeCount)
	for glyph, ok := shaper.NextGlyph(); ok; glyph, ok = shaper.NextGlyph() {
		index.add(glyph)
	}
	fieldShapeState.Unlock()
	return index.finish()
}

func fieldTextShaper(node *project.Node) (*text.Shaper, giofont.Font) {
	descriptor := giofont.Font{}
	if number(node.Props["weight"], 400) >= 600 {
		descriptor.Weight = giofont.Bold
	} else if number(node.Props["weight"], 400) >= 500 {
		descriptor.Weight = giofont.Medium
	}
	if boolValue(node.Props["italic"], false) {
		descriptor.Style = giofont.Italic
	}
	fontPath := stringValue(node.Props["font"], "")
	if token, ok := node.Props["font"].(map[string]any); ok {
		fontPath = stringValue(token["src"], "")
	}
	if fontPath == "" {
		return fieldShapeState.fallback, descriptor
	}
	if !filepath.IsAbs(fontPath) {
		fontPath = filepath.Join(filepath.Dir(node.Source.File), fontPath)
	}
	shaper, typeface, err := loadNativeFont(nil, fontPath)
	if err != nil {
		return fieldShapeState.fallback, descriptor
	}
	descriptor.Typeface = typeface
	return shaper, descriptor
}

type fieldGlyphCaret struct {
	runeIndex    int
	line         int
	x            fixed.Int26_6
	runIndex     int
	towardOrigin bool
}

type fieldGlyphIndex struct {
	runeCount      int
	carets         []fieldGlyphCaret
	segments       []fieldRuneSegment
	lineWidths     []int
	lineStarts     []int
	lineEnds       []int
	line           int
	lineStart      int
	lineMin        fixed.Int26_6
	lineMax        fixed.Int26_6
	caret          fieldGlyphCaret
	runIndex       int
	clusterAdvance fixed.Int26_6
	midCluster     bool
}

func newFieldGlyphIndex(runeCount int) *fieldGlyphIndex {
	return &fieldGlyphIndex{runeCount: runeCount, lineMin: fixed.Int26_6(math.MaxInt32)}
}

func (index *fieldGlyphIndex) insertCaret(caret fieldGlyphCaret) {
	if len(index.carets) > 0 {
		last := len(index.carets) - 1
		previous := index.carets[last]
		if previous.runeIndex == caret.runeIndex && (previous.line != caret.line || previous.x == caret.x) {
			index.carets[last] = caret
			return
		}
	}
	index.carets = append(index.carets, caret)
}

func (index *fieldGlyphIndex) add(glyph text.Glyph) {
	if glyph.X < index.lineMin {
		index.lineMin = glyph.X
	}
	if end := glyph.X + glyph.Advance; end > index.lineMax {
		index.lineMax = end
	}
	clusterBreak := glyph.Flags&text.FlagClusterBreak != 0
	paragraphBreak := glyph.Flags&text.FlagParagraphBreak != 0
	towardOrigin := glyph.Flags&text.FlagTowardOrigin != 0
	if !index.midCluster {
		index.caret.line = index.line
		index.caret.x = glyph.X
		index.caret.runIndex = index.runIndex
		index.caret.towardOrigin = towardOrigin
		if towardOrigin {
			index.caret.x += glyph.Advance
		}
		index.insertCaret(index.caret)
	}
	index.midCluster = !clusterBreak
	if paragraphBreak {
		index.clusterAdvance = 0
		index.caret.runeIndex += int(glyph.Runes)
	}
	index.clusterAdvance += glyph.Advance
	if clusterBreak && !paragraphBreak && glyph.Runes > 0 {
		count := int(glyph.Runes)
		step := index.clusterAdvance / fixed.Int26_6(count)
		adjust := fixed.Int26_6(0)
		if towardOrigin {
			adjust = index.clusterAdvance
			step = -step
		}
		for position := 1; position <= count; position++ {
			startX := index.caret.x
			index.caret.x = glyph.X + adjust + step*fixed.Int26_6(position)
			index.segments = append(index.segments, fieldRuneSegment{
				runeIndex: index.caret.runeIndex, line: index.line, startX: startX.Round(), endX: index.caret.x.Round(),
			})
			index.caret.runeIndex++
			index.insertCaret(index.caret)
		}
		index.clusterAdvance = 0
	}
	if glyph.Flags&text.FlagRunBreak != 0 {
		index.runIndex++
		index.caret.runIndex = index.runIndex
	}
	if glyph.Flags&text.FlagLineBreak != 0 {
		lineEnd := index.caret.runeIndex
		if paragraphBreak {
			lineEnd -= int(glyph.Runes)
		}
		index.lineStarts = append(index.lineStarts, index.lineStart)
		index.lineEnds = append(index.lineEnds, max(index.lineStart, lineEnd))
		if index.lineMin == fixed.Int26_6(math.MaxInt32) {
			index.lineMin = 0
		}
		index.lineWidths = append(index.lineWidths, max(0, (index.lineMax-index.lineMin).Ceil()))
		index.line++
		index.lineStart = index.caret.runeIndex
		index.lineMin = fixed.Int26_6(math.MaxInt32)
		index.lineMax = 0
		index.runIndex = 0
		index.caret.runIndex = 0
	}
}

func (index *fieldGlyphIndex) finish() ([]fieldTextPosition, []int, []int, []int, []fieldRuneSegment) {
	if len(index.lineWidths) == 0 {
		index.lineWidths = append(index.lineWidths, 0)
		index.lineStarts = append(index.lineStarts, 0)
		index.lineEnds = append(index.lineEnds, index.runeCount)
	}
	positions := make([]fieldTextPosition, index.runeCount+1)
	seen := make([]bool, index.runeCount+1)
	for _, caret := range index.carets {
		if caret.runeIndex < 0 || caret.runeIndex > index.runeCount {
			continue
		}
		positions[caret.runeIndex] = fieldTextPosition{line: min(caret.line, len(index.lineWidths)-1), x: caret.x.Round()}
		seen[caret.runeIndex] = true
	}
	for runeIndex := 1; runeIndex <= index.runeCount; runeIndex++ {
		if !seen[runeIndex] {
			positions[runeIndex] = positions[runeIndex-1]
		}
	}
	return positions, index.lineWidths, index.lineStarts, index.lineEnds, index.segments
}

func fieldGeometryFace(node *project.Node, size float64) font.Face {
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
	if token, ok := node.Props["font"].(map[string]any); ok {
		fontPath = stringValue(token["src"], "")
	}
	if fontPath != "" {
		if !filepath.IsAbs(fontPath) {
			fontPath = filepath.Join(filepath.Dir(node.Source.File), fontPath)
		}
		if loaded, err := loadFont(fontPath); err == nil {
			parsed = loaded
		}
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err == nil {
		return face
	}
	fallback, _ := opentype.NewFace(fallbackRegular(), &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	return fallback
}

func (geometry fieldTextGeometry) RuneAt(point image.Point) int {
	if len(geometry.positions) == 0 {
		return 0
	}
	line := max(0, (point.Y-geometry.Inner.Min.Y)/geometry.LineHeight+geometry.OffsetY)
	x := max(0, point.X-geometry.Inner.Min.X+geometry.OffsetX)
	best, distance := 0, int(^uint(0)>>1)
	for index, position := range geometry.positions {
		if position.line != line {
			continue
		}
		candidate := fieldAbsInt(position.x - x)
		if candidate < distance || candidate == distance && index > best {
			best, distance = index, candidate
		}
	}
	if distance != int(^uint(0)>>1) {
		return best
	}
	if line <= geometry.positions[0].line {
		return 0
	}
	return len(geometry.positions) - 1
}

func (geometry fieldTextGeometry) LineRange(position int) (int, int) {
	position = min(max(0, position), len(geometry.positions)-1)
	line := geometry.positions[position].line
	return geometry.lineStarts[line], geometry.lineEnds[line]
}

func (geometry fieldTextGeometry) Decorations() ([]image.Rectangle, image.Rectangle) {
	start, end := geometry.selection.X, geometry.selection.Y
	if start > end {
		start, end = end, start
	}
	var selection []image.Rectangle
	if start != end {
		selection = geometry.selectionRectangles(start, end)
	}
	caretPosition := geometry.positions[geometry.selection.Y]
	caretX := geometry.Inner.Min.X + caretPosition.x - geometry.OffsetX
	caretY := geometry.Inner.Min.Y + (caretPosition.line-geometry.OffsetY)*geometry.LineHeight
	caret := image.Rect(caretX, caretY, caretX+1, caretY+geometry.LineHeight).Intersect(geometry.Inner)
	return selection, caret
}

func (geometry fieldTextGeometry) selectionRectangles(start, end int) []image.Rectangle {
	segments := make([]fieldRuneSegment, 0, end-start)
	for _, segment := range geometry.segments {
		if segment.runeIndex >= start && segment.runeIndex < end {
			segments = append(segments, segment)
		}
	}
	sort.SliceStable(segments, func(left, right int) bool {
		if segments[left].line != segments[right].line {
			return segments[left].line < segments[right].line
		}
		leftX := min(segments[left].startX, segments[left].endX)
		rightX := min(segments[right].startX, segments[right].endX)
		return leftX < rightX
	})
	var rectangles []image.Rectangle
	for _, segment := range segments {
		startX, endX := min(segment.startX, segment.endX), max(segment.startX, segment.endX)
		rectangle := image.Rect(
			geometry.Inner.Min.X+startX-geometry.OffsetX,
			geometry.Inner.Min.Y+(segment.line-geometry.OffsetY)*geometry.LineHeight,
			geometry.Inner.Min.X+endX-geometry.OffsetX,
			geometry.Inner.Min.Y+(segment.line-geometry.OffsetY+1)*geometry.LineHeight,
		).Intersect(geometry.Inner)
		if rectangle.Empty() {
			continue
		}
		last := len(rectangles) - 1
		if last >= 0 && rectangles[last].Min.Y == rectangle.Min.Y && rectangle.Min.X <= rectangles[last].Max.X {
			rectangles[last] = rectangles[last].Union(rectangle)
			continue
		}
		rectangles = append(rectangles, rectangle)
	}
	return rectangles
}

func fieldDecorationRects(node *project.Node, bounds image.Rectangle) ([]image.Rectangle, image.Rectangle) {
	return newFieldTextGeometry(node, bounds).Decorations()
}

func fieldCompositionUnderlines(node *project.Node, bounds image.Rectangle) []image.Rectangle {
	if !boolValue(node.Props["composing"], false) {
		return nil
	}
	clone := *node
	clone.Props = cloneMap(node.Props)
	clone.Props["selection_start"] = node.Props["composition_start"]
	clone.Props["selection_end"] = node.Props["composition_end"]
	selection, _ := newFieldTextGeometry(&clone, bounds).Decorations()
	underlines := make([]image.Rectangle, 0, len(selection))
	for _, rectangle := range selection {
		if !rectangle.Empty() {
			underlines = append(underlines, image.Rect(rectangle.Min.X, rectangle.Max.Y-1, rectangle.Max.X, rectangle.Max.Y))
		}
	}
	return underlines
}

// FieldRuneAtPoint maps a pointer to the closest rune boundary using the same
// logical field geometry as CPU and Gio painting.
func FieldRuneAtPoint(props map[string]any, bounds image.Rectangle, point image.Point) int {
	return newFieldTextGeometry(&project.Node{Props: props}, bounds).RuneAt(point)
}

// FieldLineRange returns the visual-line rune range containing position.
func FieldLineRange(props map[string]any, bounds image.Rectangle, position int) (int, int) {
	return newFieldTextGeometry(&project.Node{Props: props}, bounds).LineRange(position)
}

// FieldVisibleColumns returns the deterministic visual-column capacity used
// for wrapped keyboard movement.
func FieldVisibleColumns(props map[string]any, bounds image.Rectangle) int {
	geometry := newFieldTextGeometry(&project.Node{Props: props}, bounds)
	return max(1, geometry.Inner.Dx()/geometry.Advance)
}

// FieldCaretRect returns the visible logical caret rectangle for focus reveal.
func FieldCaretRect(props map[string]any, bounds image.Rectangle) image.Rectangle {
	_, caret := newFieldTextGeometry(&project.Node{Props: props}, bounds).Decorations()
	return caret
}

func fieldAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func measureTabsIntrinsic(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	orientation := stringValue(node.Props["orientation"], "horizontal")
	gap := int(number(node.Props["gap"], 0))
	panelGap := int(number(node.Props["panel_gap"], 0))
	listMain, listCross := 0, 0
	panel := image.Point{}
	count := 0
	for _, child := range node.Children {
		if child == nil || child.Hidden {
			continue
		}
		size := measureIntrinsic(child, limit, leaf)
		switch child.Type {
		case "tab":
			if count > 0 {
				listMain += gap
			}
			if orientation == "vertical" {
				listMain += size.Y
				listCross = max(listCross, size.X)
			} else {
				listMain += size.X
				listCross = max(listCross, size.Y)
			}
			count++
		case "tab_panel":
			panel = size
		}
	}
	if orientation == "vertical" {
		return image.Pt(listCross+panelGap+panel.X, max(listMain, panel.Y))
	}
	return image.Pt(max(listMain, panel.X), listCross+panelGap+panel.Y)
}

func tabsParts(node *project.Node, bounds image.Rectangle, leaf intrinsicMeasure) map[string]image.Rectangle {
	result := make(map[string]image.Rectangle, len(node.Children))
	orientation := stringValue(node.Props["orientation"], "horizontal")
	gap := int(number(node.Props["gap"], 0))
	panelGap := int(number(node.Props["panel_gap"], 0))
	tabs := make([]*project.Node, 0, len(node.Children))
	var panel *project.Node
	listCross := 0
	for _, child := range node.Children {
		if child == nil || child.Hidden {
			continue
		}
		if child.Type == "tab" {
			tabs = append(tabs, child)
			size := measureIntrinsic(child, bounds.Size(), leaf)
			if orientation == "vertical" {
				listCross = max(listCross, size.X)
			} else {
				listCross = max(listCross, size.Y)
			}
		} else if child.Type == "tab_panel" {
			panel = child
		}
	}
	cursor := 0
	for index, tab := range tabs {
		size := measureIntrinsic(tab, bounds.Size(), leaf)
		if orientation == "vertical" {
			result[tab.Handle] = image.Rect(bounds.Min.X, bounds.Min.Y+cursor, bounds.Min.X+listCross, bounds.Min.Y+cursor+size.Y)
			cursor += size.Y
		} else {
			result[tab.Handle] = image.Rect(bounds.Min.X+cursor, bounds.Min.Y, bounds.Min.X+cursor+size.X, bounds.Min.Y+listCross)
			cursor += size.X
		}
		if index < len(tabs)-1 {
			cursor += gap
		}
	}
	if panel != nil {
		if orientation == "vertical" {
			result[panel.Handle] = image.Rect(bounds.Min.X+listCross+panelGap, bounds.Min.Y, bounds.Max.X, bounds.Max.Y)
		} else {
			result[panel.Handle] = image.Rect(bounds.Min.X, bounds.Min.Y+listCross+panelGap, bounds.Max.X, bounds.Max.Y)
		}
	}
	return result
}

func sliderParts(node *project.Node, bounds image.Rectangle) map[string]image.Rectangle {
	result := make(map[string]image.Rectangle, len(node.Children))
	if node == nil || bounds.Empty() {
		return result
	}
	minimum, maximum, step := 0.0, 100.0, 1.0
	if node.BindingState != nil {
		if node.BindingState.Min != nil {
			minimum = *node.BindingState.Min
		}
		if node.BindingState.Max != nil {
			maximum = *node.BindingState.Max
		}
		if node.BindingState.Step != nil {
			step = *node.BindingState.Step
		}
	}
	_ = step
	value := number(node.Props["value"], minimum)
	ratio := 0.0
	if maximum > minimum {
		ratio = clamp((value-minimum)/(maximum-minimum), 0, 1)
	}
	var track, fill, thumb *project.Node
	for _, child := range node.Children {
		switch child.Type {
		case "slider_track":
			track = child
		case "slider_fill":
			fill = child
		case "slider_thumb":
			thumb = child
		}
	}
	orientation := stringValue(node.Props["orientation"], "horizontal")
	if orientation == "vertical" {
		trackWidth := max(1, int(number(prop(track, "width"), 4)))
		thumbWidth := max(1, int(number(prop(thumb, "width"), 16)))
		thumbHeight := max(1, int(number(prop(thumb, "height"), 16)))
		centerX := bounds.Min.X + bounds.Dx()/2
		centerY := bounds.Max.Y - int(math.Round(ratio*float64(bounds.Dy())))
		if track != nil {
			result[track.Handle] = image.Rect(centerX-trackWidth/2, bounds.Min.Y, centerX+(trackWidth+1)/2, bounds.Max.Y)
		}
		if fill != nil {
			fillWidth := max(1, int(number(prop(fill, "width"), float64(trackWidth))))
			result[fill.Handle] = image.Rect(centerX-fillWidth/2, centerY, centerX+(fillWidth+1)/2, bounds.Max.Y)
		}
		if thumb != nil {
			result[thumb.Handle] = image.Rect(centerX-thumbWidth/2, centerY-thumbHeight/2, centerX+(thumbWidth+1)/2, centerY+(thumbHeight+1)/2)
		}
		return result
	}
	trackHeight := max(1, int(number(prop(track, "height"), 4)))
	thumbWidth := max(1, int(number(prop(thumb, "width"), 16)))
	thumbHeight := max(1, int(number(prop(thumb, "height"), 16)))
	centerX := bounds.Min.X + int(math.Round(ratio*float64(bounds.Dx())))
	centerY := bounds.Min.Y + bounds.Dy()/2
	if track != nil {
		result[track.Handle] = image.Rect(bounds.Min.X, centerY-trackHeight/2, bounds.Max.X, centerY+(trackHeight+1)/2)
	}
	if fill != nil {
		fillHeight := max(1, int(number(prop(fill, "height"), float64(trackHeight))))
		result[fill.Handle] = image.Rect(bounds.Min.X, centerY-fillHeight/2, centerX, centerY+(fillHeight+1)/2)
	}
	if thumb != nil {
		result[thumb.Handle] = image.Rect(centerX-thumbWidth/2, centerY-thumbHeight/2, centerX+(thumbWidth+1)/2, centerY+(thumbHeight+1)/2)
	}
	return result
}

func prop(node *project.Node, key string) any {
	if node == nil {
		return nil
	}
	return node.Props[key]
}

func measureStackIntrinsic(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	vertical := stringValue(node.Props["direction"], "vertical") != "horizontal"
	mainLimit := limit.X
	if vertical {
		mainLimit = limit.Y
	}
	mainGap, crossGap := stackGaps(node, vertical)
	wrap := boolValue(node.Props["wrap"], false)
	lineMain, lineCross := 0, 0
	maxMain, totalCross, lineCount := 0, 0, 0
	flush := func() {
		if lineCount == 0 {
			return
		}
		maxMain = max(maxMain, lineMain)
		if totalCross > 0 {
			totalCross += crossGap
		}
		totalCross += lineCross
		lineMain, lineCross, lineCount = 0, 0, 0
	}
	for _, child := range visibleLayoutChildren(node.Children) {
		size := measureIntrinsic(child, limit, leaf)
		main, cross := size.X, size.Y
		mainKey := "width"
		if vertical {
			main, cross = size.Y, size.X
			mainKey = "height"
		}
		if basis, ok := resolveDimension(child.Place["basis"], mainLimit); ok {
			main = basis
		} else if authored, ok := resolveDimension(child.Props[mainKey], mainLimit); ok {
			main = authored
		}
		needed := main
		if lineCount > 0 {
			needed += mainGap
		}
		if wrap && lineCount > 0 && lineMain+needed > mainLimit {
			flush()
			needed = main
		}
		lineMain += needed
		lineCross = max(lineCross, cross)
		lineCount++
	}
	flush()
	if vertical {
		return image.Pt(totalCross, maxMain)
	}
	return image.Pt(maxMain, totalCross)
}

func measureGridIntrinsic(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	columns := len(anySlice(node.Props["columns"]))
	if columns == 0 {
		columns = max(1, int(number(node.Props["columns"], 1)))
	}
	columnWidths := make([]int, columns)
	children := visibleLayoutChildren(node.Children)
	rowHeights := make([]int, (len(children)+columns-1)/columns)
	for index, child := range children {
		size := measureIntrinsic(child, limit, leaf)
		columnWidths[index%columns] = max(columnWidths[index%columns], size.X)
		rowHeights[index/columns] = max(rowHeights[index/columns], size.Y)
	}
	gap := int(number(node.Props["gap"], 0))
	return image.Pt(sumInts(columnWidths)+gap*max(0, len(columnWidths)-1), sumInts(rowHeights)+gap*max(0, len(rowHeights)-1))
}

func visibleLayoutChildren(children []*project.Node) []*project.Node {
	result := make([]*project.Node, 0, len(children))
	for _, child := range children {
		if child != nil && !child.Hidden && isFlowPositioned(child) {
			result = append(result, child)
		}
	}
	return result
}

// isFlowPositioned is the single renderer-side classification used by
// intrinsic measurement and flow planners. Fixed nodes remain in the source
// hierarchy but are withheld from normal-flow contribution while their final
// viewport geometry is laid out separately.
func isFlowPositioned(node *project.Node) bool {
	return node != nil && !isFixedPositioned(node)
}

func isFixedPositioned(node *project.Node) bool {
	if node == nil {
		return false
	}
	return stringValue(node.Place["position"], "flow") == "fixed"
}

func isStickyPositioned(node *project.Node) bool {
	if node == nil {
		return false
	}
	return stringValue(node.Place["position"], "flow") == "sticky"
}

type fixedInsetValue struct {
	value int
	set   bool
}

func resolveFixedInset(value any, basis int) fixedInsetValue {
	if value == nil {
		return fixedInsetValue{}
	}
	if raw, ok := numeric(value); ok {
		return fixedInsetValue{value: rounded(raw), set: true}
	}
	if mapping, ok := value.(map[string]any); ok && len(mapping) == 1 {
		if percent, ok := numeric(mapping["percent"]); ok {
			return fixedInsetValue{value: rounded(float64(basis) * percent / 100), set: true}
		}
	}
	return fixedInsetValue{}
}

// planFixedViewport computes a fixed node's logical viewport rectangle. It is
// pure so CPU, Gio, and retained replay can share the same sizing decision.
// At least one inset is required on each axis so an authored fixed node has an
// unambiguous viewport anchor.
func planFixedViewport(node *project.Node, viewport image.Rectangle, intrinsic image.Point) (image.Rectangle, bool) {
	if node == nil || viewport.Empty() || !isFixedPositioned(node) {
		return image.Rectangle{}, false
	}
	insetMap, ok := node.Place["inset"].(map[string]any)
	if !ok {
		return image.Rectangle{}, false
	}
	top := resolveFixedInset(insetMap["top"], viewport.Dy())
	right := resolveFixedInset(insetMap["right"], viewport.Dx())
	bottom := resolveFixedInset(insetMap["bottom"], viewport.Dy())
	left := resolveFixedInset(insetMap["left"], viewport.Dx())
	if !top.set && !bottom.set || !left.set && !right.set {
		return image.Rectangle{}, false
	}

	width, widthDefinite := resolveDimension(node.Props["width"], viewport.Dx())
	height, heightDefinite := resolveDimension(node.Props["height"], viewport.Dy())
	// Opposing insets make an automatic axis definite before aspect-ratio
	// derivation. When both axes stretch, the insets win on both axes.
	if !widthDefinite && left.set && right.set {
		width = max(0, viewport.Dx()-left.value-right.value)
		widthDefinite = true
	}
	if !heightDefinite && top.set && bottom.set {
		height = max(0, viewport.Dy()-top.value-bottom.value)
		heightDefinite = true
	}
	if ratio, ok := aspectRatio(node.Props["aspect_ratio"]); ok {
		if widthDefinite && !heightDefinite {
			height, heightDefinite = rounded(float64(width)/ratio), true
		} else if heightDefinite && !widthDefinite {
			width, widthDefinite = rounded(float64(height)*ratio), true
		}
	}
	if !widthDefinite {
		width = max(0, intrinsic.X)
	}
	if !heightDefinite {
		height = max(0, intrinsic.Y)
	}
	minWidth, _ := resolveDimension(node.Props["min_width"], viewport.Dx())
	maxWidth, hasMaxWidth := resolveDimension(node.Props["max_width"], viewport.Dx())
	minHeight, _ := resolveDimension(node.Props["min_height"], viewport.Dy())
	maxHeight, hasMaxHeight := resolveDimension(node.Props["max_height"], viewport.Dy())
	width = clampDimension(width, minWidth, maxWidth, hasMaxWidth)
	height = clampDimension(height, minHeight, maxHeight, hasMaxHeight)

	x := viewport.Min.X
	if left.set {
		x += left.value
	} else if right.set {
		x = viewport.Max.X - right.value - width
	}
	y := viewport.Min.Y
	if top.set {
		y += top.value
	} else if bottom.set {
		y = viewport.Max.Y - bottom.value - height
	}
	return image.Rect(x, y, x+width, y+height), true
}

// fixedIntrinsicSize measures a fixed node's authored content without letting
// the positioning classification short-circuit the intrinsic pass.
func fixedIntrinsicSize(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	if node == nil || leaf == nil {
		return image.Point{}
	}
	clone := *node
	clone.Place = cloneMap(node.Place)
	if clone.Place == nil {
		clone.Place = make(map[string]any)
	}
	clone.Place["position"] = "flow"
	return measureIntrinsic(&clone, limit, leaf)
}

func stickyAxisPosition(base, size, viewportStart, viewportEnd, parentStart, parentEnd int, startInset, endInset fixedInsetValue) int {
	if !startInset.set && !endInset.set {
		return base
	}
	minimum, maximum := base, base
	if startInset.set {
		minimum = viewportStart + startInset.value
	}
	if endInset.set {
		maximum = viewportEnd - endInset.value - size
	}
	if startInset.set && endInset.set && minimum > maximum {
		// Over-constrained opposing insets resolve deterministically from the
		// start edge; this is the same top/left-wins rule for both axes.
		maximum = minimum
	}
	position := base
	if startInset.set {
		position = max(position, minimum)
	}
	if endInset.set {
		position = min(position, maximum)
	}
	if parentEnd > parentStart {
		parentMaximum := parentEnd - size
		if parentMaximum < parentStart {
			position = max(position, parentStart)
		} else {
			position = min(max(position, parentStart), parentMaximum)
		}
	}
	return position
}

// planStickyRect applies the ancestor-translated base rectangle to one
// nearest containing scrollport (or the view viewport), then applies parent
// containment. It never mutates the authored node or normal rectangle.
func planStickyRect(node *project.Node, base, parentInner, viewport image.Rectangle) (image.Rectangle, image.Point) {
	if node == nil || !isStickyPositioned(node) || base.Empty() || viewport.Empty() {
		return base, image.Point{}
	}
	insetMap, _ := node.Place["inset"].(map[string]any)
	top := resolveFixedInset(insetMap["top"], viewport.Dy())
	right := resolveFixedInset(insetMap["right"], viewport.Dx())
	bottom := resolveFixedInset(insetMap["bottom"], viewport.Dy())
	left := resolveFixedInset(insetMap["left"], viewport.Dx())
	x := stickyAxisPosition(base.Min.X, base.Dx(), viewport.Min.X, viewport.Max.X, parentInner.Min.X, parentInner.Max.X, left, right)
	y := stickyAxisPosition(base.Min.Y, base.Dy(), viewport.Min.Y, viewport.Max.Y, parentInner.Min.Y, parentInner.Max.Y, top, bottom)
	result := image.Rect(x, y, x+base.Dx(), y+base.Dy())
	return result, image.Pt(x-base.Min.X, y-base.Min.Y)
}

func constrainIntrinsic(node *project.Node, preferred, limit image.Point) image.Point {
	width, widthDefinite := resolveDimension(node.Props["width"], limit.X)
	height, heightDefinite := resolveDimension(node.Props["height"], limit.Y)
	if ratio, ok := aspectRatio(node.Props["aspect_ratio"]); ok {
		if widthDefinite && !heightDefinite {
			height, heightDefinite = rounded(float64(width)/ratio), true
		} else if heightDefinite && !widthDefinite {
			width, widthDefinite = rounded(float64(height)*ratio), true
		}
	}
	if widthDefinite {
		preferred.X = width
	}
	if heightDefinite {
		preferred.Y = height
	}
	minWidth, _ := resolveDimension(node.Props["min_width"], limit.X)
	maxWidth, hasMaxWidth := resolveDimension(node.Props["max_width"], limit.X)
	minHeight, _ := resolveDimension(node.Props["min_height"], limit.Y)
	maxHeight, hasMaxHeight := resolveDimension(node.Props["max_height"], limit.Y)
	preferred.X = clampDimension(preferred.X, minWidth, maxWidth, hasMaxWidth)
	preferred.Y = clampDimension(preferred.Y, minHeight, maxHeight, hasMaxHeight)
	return preferred
}

type stackItem struct {
	index        int
	base         int
	main         int
	cross        int
	minMain      int
	maxMain      int
	grow         float64
	shrink       float64
	implicitGrow bool
}

type stackLine struct {
	items []stackItem
	cross int
}

func planStack(node *project.Node, bounds image.Rectangle, measure intrinsicMeasure) []image.Rectangle {
	inner := inset(bounds, insets(node.Props["padding"]))
	vertical := stringValue(node.Props["direction"], "vertical") != "horizontal"
	mainSize, crossSize := inner.Dx(), inner.Dy()
	if vertical {
		mainSize, crossSize = inner.Dy(), inner.Dx()
	}
	mainGap, crossGap := stackGaps(node, vertical)
	wrap := boolValue(node.Props["wrap"], false)

	items := make([]stackItem, 0, len(node.Children))
	for index, child := range node.Children {
		if !isFlowPositioned(child) {
			continue
		}
		intrinsic := measure(child, inner.Size())
		intrinsicMain, intrinsicCross := intrinsic.X, intrinsic.Y
		mainKey, crossKey := "width", "height"
		minMainKey, maxMainKey := "min_width", "max_width"
		minCrossKey, maxCrossKey := "min_height", "max_height"
		if vertical {
			intrinsicMain, intrinsicCross = intrinsic.Y, intrinsic.X
			mainKey, crossKey = "height", "width"
			minMainKey, maxMainKey = "min_height", "max_height"
			minCrossKey, maxCrossKey = "min_width", "max_width"
		}

		base, definite := resolveDimension(child.Place["basis"], mainSize)
		if !definite {
			base, definite = resolveDimension(child.Props[mainKey], mainSize)
		}
		if !definite {
			base = intrinsicMain
		}
		minMain, _ := resolveDimension(child.Props[minMainKey], mainSize)
		maxMain, hasMaxMain := resolveDimension(child.Props[maxMainKey], mainSize)
		base = clampDimension(base, minMain, maxMain, hasMaxMain)

		cross, crossDefinite := resolveDimension(child.Props[crossKey], crossSize)
		if !crossDefinite {
			cross = intrinsicCross
		}
		if ratio, ok := aspectRatio(child.Props["aspect_ratio"]); ok {
			if !crossDefinite && base > 0 {
				if vertical {
					cross = rounded(float64(base) * ratio)
				} else {
					cross = rounded(float64(base) / ratio)
				}
			}
		}
		minCross, _ := resolveDimension(child.Props[minCrossKey], crossSize)
		maxCross, hasMaxCross := resolveDimension(child.Props[maxCrossKey], crossSize)
		cross = clampDimension(cross, minCross, maxCross, hasMaxCross)

		grow, hasGrow := numeric(child.Place["grow"])
		implicitGrow := !definite && intrinsicMain == 0
		if value, ok := child.Props[mainKey].(string); ok && value == "fill" {
			implicitGrow = true
		}
		if !hasGrow && implicitGrow {
			grow = 1
		}
		shrink, _ := numeric(child.Place["shrink"])
		items = append(items, stackItem{
			index: index, base: base, main: base, cross: cross,
			minMain: minMain, maxMain: maxMain,
			grow: math.Max(0, grow), shrink: math.Max(0, shrink), implicitGrow: implicitGrow,
		})
	}

	lines := packStackLines(items, mainSize, mainGap, wrap)
	for index := range lines {
		allocateStackLine(&lines[index], mainSize, mainGap)
		for _, item := range lines[index].items {
			lines[index].cross = max(lines[index].cross, item.cross)
		}
		if !wrap {
			lines[index].cross = crossSize
		}
	}

	result := make([]image.Rectangle, len(node.Children))
	crossCursor := 0
	containerAlignment := stringValue(node.Props["alignment"], "stretch")
	for _, line := range lines {
		used := mainGap * max(0, len(line.items)-1)
		for _, item := range line.items {
			used += item.main
		}
		mainCursor, distributedGap := distribution(stringValue(node.Props["distribution"], "start"), mainSize-used, mainGap, len(line.items))
		for _, item := range line.items {
			child := node.Children[item.index]
			alignment := stringValue(child.Place["alignment"], containerAlignment)
			itemCross := item.cross
			if alignment == "stretch" {
				itemCross = line.cross
			}
			crossOffset := 0
			switch alignment {
			case "center":
				crossOffset = (line.cross - itemCross) / 2
			case "end":
				crossOffset = line.cross - itemCross
			}
			var rect image.Rectangle
			if vertical {
				rect = image.Rect(inner.Min.X+crossCursor+crossOffset, inner.Min.Y+mainCursor, inner.Min.X+crossCursor+crossOffset+itemCross, inner.Min.Y+mainCursor+item.main)
			} else {
				rect = image.Rect(inner.Min.X+mainCursor, inner.Min.Y+crossCursor+crossOffset, inner.Min.X+mainCursor+item.main, inner.Min.Y+crossCursor+crossOffset+itemCross)
			}
			result[item.index] = rect
			mainCursor += item.main + distributedGap
		}
		crossCursor += line.cross + crossGap
	}
	return result
}

func packStackLines(items []stackItem, mainSize, gap int, wrap bool) []stackLine {
	if len(items) == 0 {
		return nil
	}
	lines := []stackLine{{}}
	used := 0
	for _, item := range items {
		needed := item.base
		if len(lines[len(lines)-1].items) > 0 {
			needed += gap
		}
		if wrap && len(lines[len(lines)-1].items) > 0 && used+needed > mainSize {
			lines = append(lines, stackLine{})
			used = 0
			needed = item.base
		}
		last := &lines[len(lines)-1]
		last.items = append(last.items, item)
		used += needed
	}
	return lines
}

func allocateStackLine(line *stackLine, mainSize, gap int) {
	available := mainSize - gap*max(0, len(line.items)-1)
	total := 0
	for _, item := range line.items {
		total += item.main
	}
	if free := available - total; free > 0 {
		distributeGrow(line.items, free)
	} else if free < 0 {
		distributeShrink(line.items, -free)
	}
}

func distributeGrow(items []stackItem, free int) {
	active := make([]bool, len(items))
	for index := range items {
		active[index] = items[index].grow > 0 && (items[index].maxMain <= 0 || items[index].main < items[index].maxMain)
	}
	for free > 0 {
		total, last := 0.0, -1
		for index := range items {
			if active[index] {
				total += items[index].grow
				last = index
			}
		}
		if total == 0 || last < 0 {
			return
		}
		startFree := free
		changed := false
		for index := range items {
			if !active[index] {
				continue
			}
			add := free
			if index != last {
				add = int(math.Floor(float64(startFree) * items[index].grow / total))
			}
			if items[index].maxMain > 0 {
				add = min(add, max(0, items[index].maxMain-items[index].main))
			}
			if add > 0 {
				items[index].main += add
				free -= add
				changed = true
			}
			if items[index].maxMain > 0 && items[index].main >= items[index].maxMain {
				active[index] = false
			}
		}
		if !changed {
			return
		}
	}
}

func distributeShrink(items []stackItem, deficit int) {
	active := make([]bool, len(items))
	for index := range items {
		active[index] = items[index].shrink > 0 && items[index].main > items[index].minMain
	}
	for deficit > 0 {
		total, last := 0.0, -1
		for index := range items {
			if active[index] {
				total += float64(items[index].base) * items[index].shrink
				last = index
			}
		}
		if total == 0 || last < 0 {
			return
		}
		startDeficit := deficit
		changed := false
		for index := range items {
			if !active[index] {
				continue
			}
			reduce := deficit
			if index != last {
				reduce = int(math.Floor(float64(startDeficit) * float64(items[index].base) * items[index].shrink / total))
			}
			reduce = min(reduce, items[index].main-items[index].minMain)
			if reduce > 0 {
				items[index].main -= reduce
				deficit -= reduce
				changed = true
			}
			if items[index].main <= items[index].minMain {
				active[index] = false
			}
		}
		if !changed {
			return
		}
	}
}

func distribution(kind string, spare, baseGap, count int) (int, int) {
	spare = max(0, spare)
	switch kind {
	case "center":
		return spare / 2, baseGap
	case "end":
		return spare, baseGap
	case "space_between":
		if count > 1 {
			return 0, baseGap + spare/(count-1)
		}
	case "space_around":
		if count > 0 {
			extra := spare / count
			return extra / 2, baseGap + extra
		}
	}
	return 0, baseGap
}

func stackGaps(node *project.Node, vertical bool) (int, int) {
	gap := int(number(node.Props["gap"], 0))
	rowGap := int(number(node.Props["row_gap"], float64(gap)))
	columnGap := int(number(node.Props["column_gap"], float64(gap)))
	if vertical {
		return rowGap, columnGap
	}
	return columnGap, rowGap
}

func resolveDimension(value any, parent int) (int, bool) {
	if number, ok := numeric(value); ok {
		return max(0, rounded(number)), true
	}
	percent, ok := value.(map[string]any)
	if !ok || len(percent) != 1 {
		return 0, false
	}
	percentValue, ok := numeric(percent["percent"])
	if !ok {
		return 0, false
	}
	return max(0, rounded(float64(parent)*percentValue/100)), true
}

func aspectRatio(value any) (float64, bool) {
	mapping, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	width, widthOK := numeric(mapping["width"])
	height, heightOK := numeric(mapping["height"])
	if !widthOK || !heightOK || width <= 0 || height <= 0 {
		return 0, false
	}
	return width / height, true
}

func numeric(value any) (float64, bool) {
	switch value := value.(type) {
	case int64:
		return float64(value), true
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	default:
		return 0, false
	}
}

func clampDimension(value, minimum, maximum int, hasMaximum bool) int {
	value = max(value, minimum)
	if hasMaximum {
		value = min(value, maximum)
	}
	return value
}

func rounded(value float64) int {
	return int(math.Floor(value + .5))
}

func minPositive(value, limit int) int {
	if limit <= 0 {
		return value
	}
	return min(value, limit)
}

type scrollPlan struct {
	Viewport    image.Rectangle
	ContentSize image.Point
	ContentRect image.Rectangle
	Maximum     image.Point
	Clip        image.Rectangle
	EnabledX    bool
	EnabledY    bool
}

// ScrollMetrics describes the final logical geometry and independent extents
// of one rendered scrollport.
type ScrollMetrics struct {
	Viewport    image.Rectangle
	ContentSize image.Point
	Maximum     image.Point
	EnabledX    bool
	EnabledY    bool
}

// scrollbarPlan is the renderer-neutral geometry for one derived scrollbar
// axis. Track, thumb, and the optional shared corner are all expressed in
// logical coordinates; CPU, Gio, and retained replay consume this same plan.
type scrollbarPlan struct {
	Axis     string
	Policy   string
	Track    image.Rectangle
	Thumb    image.Rectangle
	Corner   image.Rectangle
	Offset   int
	Maximum  int
	Viewport int
	Content  int
	Enabled  bool
}

const (
	scrollbarThickness = 8
	scrollbarInset     = 2
)

func planScrollbars(node *project.Node, plan scrollPlan, offset image.Point) []scrollbarPlan {
	if node == nil || plan.Viewport.Empty() {
		return nil
	}
	axisX := plan.EnabledX
	axisY := plan.EnabledY
	policyX := scrollbarPolicy(node, "horizontal", axisX)
	policyY := scrollbarPolicy(node, "vertical", axisY)
	visibleX := scrollbarVisible(policyX, plan.Maximum.X)
	visibleY := scrollbarVisible(policyY, plan.Maximum.Y)
	if !visibleX && !visibleY {
		return nil
	}
	viewport := plan.Viewport
	inner := image.Rect(
		viewport.Min.X+scrollbarInset,
		viewport.Min.Y+scrollbarInset,
		max(viewport.Min.X+scrollbarInset, viewport.Max.X-scrollbarInset),
		max(viewport.Min.Y+scrollbarInset, viewport.Max.Y-scrollbarInset),
	)
	result := make([]scrollbarPlan, 0, 2)
	if visibleX {
		endX := inner.Max.X
		if visibleY {
			endX = max(inner.Min.X, endX-scrollbarThickness)
		}
		track := image.Rect(inner.Min.X, inner.Max.Y-scrollbarThickness, endX, inner.Max.Y)
		result = append(result, makeScrollbarPlan("horizontal", policyX, track, offset.X, plan.Maximum.X, plan.Viewport.Dx(), plan.ContentSize.X))
	}
	if visibleY {
		endY := inner.Max.Y
		if visibleX {
			endY = max(inner.Min.Y, endY-scrollbarThickness)
		}
		track := image.Rect(inner.Max.X-scrollbarThickness, inner.Min.Y, inner.Max.X, endY)
		result = append(result, makeScrollbarPlan("vertical", policyY, track, offset.Y, plan.Maximum.Y, plan.Viewport.Dy(), plan.ContentSize.Y))
	}
	if visibleX && visibleY && len(result) > 0 {
		result[0].Corner = image.Rect(inner.Max.X-scrollbarThickness, inner.Max.Y-scrollbarThickness, inner.Max.X, inner.Max.Y)
	}
	return result
}

func scrollbarPolicy(node *project.Node, axis string, enabled bool) string {
	if !enabled {
		return "hidden"
	}
	if node != nil {
		key := "scrollbar_x"
		if axis == "vertical" {
			key = "scrollbar_y"
		}
		if value, ok := node.Props[key].(string); ok {
			return value
		}
		if legacy, ok := node.Props["scrollbar"].(bool); ok {
			if legacy {
				return "auto"
			}
			return "hidden"
		}
	}
	// Resolved project nodes carry explicit per-axis policies. Keeping an
	// unnormalized node hidden preserves renderer compatibility for direct
	// reference callers that construct project trees in tests.
	return "hidden"
}

func scrollbarVisible(policy string, maximum int) bool {
	switch policy {
	case "always":
		return true
	case "auto":
		return maximum > 0
	default:
		return false
	}
}

func makeScrollbarPlan(axis, policy string, track image.Rectangle, offset, maximum, viewport, content int) scrollbarPlan {
	maximum = max(0, maximum)
	offset = min(max(0, offset), maximum)
	trackLength := track.Dx()
	if axis == "vertical" {
		trackLength = track.Dy()
	}
	thumbLength := max(0, trackLength)
	enabled := maximum > 0
	if enabled && content > 0 {
		thumbLength = max(scrollbarThickness*3, rounded(float64(trackLength)*float64(viewport)/float64(content)))
		thumbLength = min(trackLength, thumbLength)
	}
	travel := max(0, trackLength-thumbLength)
	position := 0
	if maximum > 0 {
		position = min(travel, max(0, rounded(float64(offset)*float64(travel)/float64(maximum))))
	}
	thumb := track
	if axis == "vertical" {
		thumb = image.Rect(track.Min.X, track.Min.Y+position, track.Max.X, track.Min.Y+position+thumbLength)
	} else {
		thumb = image.Rect(track.Min.X+position, track.Min.Y, track.Min.X+position+thumbLength, track.Max.Y)
	}
	return scrollbarPlan{Axis: axis, Policy: policy, Track: track, Thumb: thumb, Offset: offset, Maximum: maximum, Viewport: viewport, Content: content, Enabled: enabled}
}

const scrollUnboundedLimit = 1 << 20

func planScroll(node *project.Node, bounds image.Rectangle, measure intrinsicMeasure) scrollPlan {
	viewport := bounds
	if node != nil {
		viewport = inset(bounds, insets(node.Props["padding"]))
		if viewport.Max.X < viewport.Min.X {
			viewport.Max.X = viewport.Min.X
		}
		if viewport.Max.Y < viewport.Min.Y {
			viewport.Max.Y = viewport.Min.Y
		}
	}
	axis := "vertical"
	if node != nil {
		axis = stringValue(node.Props["axis"], axis)
	}
	enabledX := axis == "horizontal" || axis == "both"
	enabledY := axis == "vertical" || axis == "both"
	plan := scrollPlan{
		Viewport: viewport, ContentSize: viewport.Size(), Maximum: image.Point{},
		EnabledX: enabledX, EnabledY: enabledY,
	}
	if node == nil || len(node.Children) != 1 || measure == nil || viewport.Empty() {
		plan.ContentRect = image.Rect(viewport.Min.X, viewport.Min.Y, viewport.Max.X, viewport.Max.Y)
		plan.Clip = viewport
		return plan
	}
	basis := viewport.Size()
	limit := basis
	if enabledX {
		limit.X = scrollUnboundedLimit
	}
	if enabledY {
		limit.Y = scrollUnboundedLimit
	}
	child := materializeScrollDimensions(node.Children[0], basis)
	preferred := measure(child, limit)
	contentSize := image.Pt(max(basis.X, preferred.X), max(basis.Y, preferred.Y))
	if !enabledX {
		contentSize.X = max(basis.X, preferred.X)
	}
	if !enabledY {
		contentSize.Y = max(basis.Y, preferred.Y)
	}
	plan.ContentSize = contentSize
	if enabledX {
		plan.Maximum.X = max(0, contentSize.X-basis.X)
	}
	if enabledY {
		plan.Maximum.Y = max(0, contentSize.Y-basis.Y)
	}
	plan.ContentRect = image.Rect(viewport.Min.X, viewport.Min.Y, viewport.Min.X+contentSize.X, viewport.Min.Y+contentSize.Y)
	plan.Clip = plan.ContentRect.Intersect(viewport)
	return plan
}

func scrollMetrics(plan scrollPlan) ScrollMetrics {
	return ScrollMetrics{
		Viewport: plan.Viewport, ContentSize: plan.ContentSize, Maximum: plan.Maximum,
		EnabledX: plan.EnabledX, EnabledY: plan.EnabledY,
	}
}

func clampScrollOffset(offset image.Point, plan scrollPlan) image.Point {
	if plan.EnabledX {
		offset.X = min(max(0, offset.X), plan.Maximum.X)
	} else {
		offset.X = 0
	}
	if plan.EnabledY {
		offset.Y = min(max(0, offset.Y), plan.Maximum.Y)
	} else {
		offset.Y = 0
	}
	return offset
}

func materializeScrollDimensions(node *project.Node, basis image.Point) *project.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Props = cloneMap(node.Props)
	for _, key := range []string{"width", "min_width", "max_width"} {
		if value, exists := clone.Props[key]; exists {
			clone.Props[key] = materializeScrollDimension(value, basis.X)
		}
	}
	for _, key := range []string{"height", "min_height", "max_height"} {
		if value, exists := clone.Props[key]; exists {
			clone.Props[key] = materializeScrollDimension(value, basis.Y)
		}
	}
	clone.Place = cloneMap(node.Place)
	if value, exists := clone.Place["basis"]; exists {
		clone.Place["basis"] = materializeScrollDimension(value, basis.X)
	}
	clone.Children = append([]*project.Node(nil), node.Children...)
	return &clone
}

func materializeScrollDimension(value any, basis int) any {
	if text, ok := value.(string); ok && text == "fill" {
		return int64(basis)
	}
	percent, ok := value.(map[string]any)
	if !ok || len(percent) != 1 {
		return value
	}
	percentValue, ok := numeric(percent["percent"])
	if !ok {
		return value
	}
	return int64(rounded(float64(basis) * percentValue / 100))
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
