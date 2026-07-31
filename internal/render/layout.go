package render

import (
	"image"
	"math"

	"gora/internal/project"
)

type intrinsicMeasure func(*project.Node, image.Point) image.Point

func measureIntrinsic(node *project.Node, limit image.Point, leaf intrinsicMeasure) image.Point {
	if node == nil {
		return image.Point{}
	}
	padding := insets(node.Props["padding"])
	innerLimit := image.Pt(max(0, limit.X-padding.left-padding.right), max(0, limit.Y-padding.top-padding.bottom))
	var preferred image.Point
	switch node.Type {
	case "surface", "button", "_viewport":
		if len(node.Children) == 1 {
			preferred = measureIntrinsic(node.Children[0], innerLimit, leaf)
		}
		preferred.X += padding.left + padding.right
		preferred.Y += padding.top + padding.bottom
	case "stack":
		preferred = measureStackIntrinsic(node, innerLimit, leaf)
		preferred.X += padding.left + padding.right
		preferred.Y += padding.top + padding.bottom
	case "overlay":
		for _, child := range node.Children {
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
			if stringValue(node.Props["axis"], "vertical") == "vertical" {
				childLimit.Y = 1 << 20
			} else {
				childLimit.X = 1 << 20
			}
			preferred = measureIntrinsic(node.Children[0], childLimit, leaf)
			preferred.X = minPositive(preferred.X, innerLimit.X)
			preferred.Y = minPositive(preferred.Y, innerLimit.Y)
		}
	default:
		preferred = leaf(node, innerLimit)
	}
	return constrainIntrinsic(node, preferred, limit)
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
	for _, child := range node.Children {
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
	rowHeights := make([]int, (len(node.Children)+columns-1)/columns)
	for index, child := range node.Children {
		size := measureIntrinsic(child, limit, leaf)
		columnWidths[index%columns] = max(columnWidths[index%columns], size.X)
		rowHeights[index/columns] = max(rowHeights[index/columns], size.Y)
	}
	gap := int(number(node.Props["gap"], 0))
	return image.Pt(sumInts(columnWidths)+gap*max(0, len(columnWidths)-1), sumInts(rowHeights)+gap*max(0, len(rowHeights)-1))
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

	items := make([]stackItem, len(node.Children))
	for index, child := range node.Children {
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
		items[index] = stackItem{
			index: index, base: base, main: base, cross: cross,
			minMain: minMain, maxMain: maxMain,
			grow: math.Max(0, grow), shrink: math.Max(0, shrink), implicitGrow: implicitGrow,
		}
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

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func scrollContentSize(child *project.Node, bounds image.Rectangle, axis string, measure intrinsicMeasure) int {
	limit := bounds.Size()
	viewport := bounds.Dx()
	if axis == "vertical" {
		limit.Y = 1 << 20
		viewport = bounds.Dy()
	} else {
		limit.X = 1 << 20
	}
	preferred := measure(child, limit)
	size := preferred.X
	if axis == "vertical" {
		size = preferred.Y
	}
	return max(viewport, size)
}
