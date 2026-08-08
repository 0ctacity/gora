package interaction

import (
	"image"
	"math"
	"sort"

	"gora/internal/document"
	"gora/internal/semantic"
)

type Activation struct {
	Scope        string
	Actions      []document.Action
	OpenSelect   string
	ActiveOption string
	CloseSelect  bool
}

type ControlValueChange struct {
	ID    string
	Value any
}

// ScrollChange is a normalized logical scrollbar operation. Only the axis
// matching the derived scrollbar is populated for axis-scoped changes.
type ScrollChange struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
	X    int    `json:"x,omitempty"`
	Y    int    `json:"y,omitempty"`
}

type scrollbarCapture struct {
	axis       *semantic.Node
	track      image.Rectangle
	thumb      image.Rectangle
	grabOffset int
	drag       bool
}

type PointerCaptureSnapshot struct {
	OwnerID   string      `json:"owner_id"`
	PointerID int         `json:"pointer_id"`
	Source    string      `json:"source,omitempty"`
	Buttons   int         `json:"buttons,omitempty"`
	Point     image.Point `json:"point"`
}

type KeyboardPressSnapshot struct {
	OwnerID string `json:"owner_id"`
	Key     string `json:"key"`
}

type RouterQueueSizes struct {
	ValueChanges  int `json:"value_changes"`
	ScrollChanges int `json:"scroll_changes"`
}

// RouterSnapshot is an immutable, public view of transient interaction
// metadata. It intentionally contains no handles or pointers and never drains
// pending queues.
type RouterSnapshot struct {
	FocusedID             string                  `json:"focused_id,omitempty"`
	HoveredIDs            []string                `json:"hovered_ids"`
	PressedIDs            []string                `json:"pressed_ids"`
	ActiveIDs             []string                `json:"active_ids"`
	OpenSelectID          string                  `json:"open_select_id,omitempty"`
	DisabledIDs           []string                `json:"disabled_ids"`
	PointerCapture        *PointerCaptureSnapshot `json:"pointer_capture,omitempty"`
	KeyboardPress         *KeyboardPressSnapshot  `json:"keyboard_press,omitempty"`
	ScrollbarGestureOwner string                  `json:"scrollbar_gesture_owner,omitempty"`
	SliderGestureOwner    string                  `json:"slider_gesture_owner,omitempty"`
	QueueSizes            RouterQueueSizes        `json:"queue_sizes"`
	Inspecting            bool                    `json:"inspecting,omitempty"`
}

// Router applies deterministic hit testing, pointer capture, focus, and keyboard activation.
type Router struct {
	tree            *semantic.Node
	regions         []*semantic.Node
	transient       Transient
	captureID       int
	captureHandle   string
	keyboardPress   string
	keyboardKey     string
	valueChange     *ControlValueChange
	scrollChange    *ScrollChange
	scrollCapture   *scrollbarCapture
	pointerSource   string
	pointerButtons  int
	pointerPoint    image.Point
	pointerPointSet bool
	inspecting      bool
}

func NewRouter() *Router { return &Router{captureID: -1} }

func (r *Router) Update(tree *semantic.Node) {
	r.tree = tree
	r.regions = r.regions[:0]
	for _, node := range semantic.Flatten(tree) {
		if interactiveRole(node.Role) || scrollbarPart(node) {
			r.regions = append(r.regions, node)
		}
	}
	if r.transient.Focused != "" && r.enabledRegion(r.transient.Focused) == nil {
		r.transient.Focused = ""
	}
	if r.transient.Hovered != "" && r.enabledRegion(r.transient.Hovered) == nil {
		r.transient.Hovered = ""
	}
	if r.transient.Pressed != "" && r.enabledRegion(r.transient.Pressed) == nil {
		hadScrollbarCapture := r.scrollCapture != nil
		r.cancelPress()
		if hadScrollbarCapture {
			r.scrollChange = nil
		}
	}
	if r.scrollCapture != nil && (r.scrollCapture.axis == nil || r.enabledRegion(r.scrollCapture.axis.Handle) == nil) {
		r.cancelPress()
		// A queued drag belongs to the old frame/tree and must not be applied
		// after a reload, selection change, or visibility update.
		r.scrollChange = nil
	}
}

func (r *Router) SetInspecting(enabled bool) {
	r.inspecting = enabled
	if enabled {
		r.transient = Transient{}
		r.cancelPress()
	}
}

func (r *Router) Move(point image.Point, touch bool) {
	r.move(-1, point, touch)
}

// MovePointer routes a pointer move with its stable pointer ID. Hosts should
// use this form so a second pointer cannot steer an existing scrollbar-thumb
// capture. Move remains as a compatibility wrapper for callers that do not
// have pointer IDs (tests and non-pointer host paths).
func (r *Router) MovePointer(pointerID int, point image.Point, touch bool) {
	r.move(pointerID, point, touch)
}

func (r *Router) move(pointerID int, point image.Point, touch bool) {
	if r.inspecting {
		return
	}
	if r.scrollCapture != nil {
		if pointerID >= 0 && pointerID != r.captureID {
			return
		}
		if !r.scrollCapture.drag || point == image.Pt(-1, -1) {
			return
		}
		r.queueScrollbarDrag(point)
		return
	}
	if region := r.enabledRegion(r.captureHandle); region != nil && region.Role == "slider" {
		r.queueSliderValue(region, point)
	}
	if touch {
		if touch {
			r.transient.Hovered = ""
		}
		return
	}
	if region := r.hit(point); region != nil {
		r.transient.Hovered = region.Handle
	} else {
		r.transient.Hovered = ""
	}
}

func (r *Router) Press(pointerID int, point image.Point) bool {
	if r.inspecting || r.captureID != -1 {
		return false
	}
	region := r.hit(point)
	if region == nil {
		if r.transient.OpenSelect != "" {
			r.transient.OpenSelect = ""
			r.transient.ActiveOption = ""
			return true
		}
		return false
	}
	if axis := r.scrollbarAxis(region); axis != nil {
		if !axis.Enabled || region.Type == "scrollbar_corner" {
			return false
		}
		if region.Type == "scrollbar_thumb" {
			thumb := region.Bounds.ImageRectangle()
			track := axis.Bounds.ImageRectangle()
			grabOffset := point.X - thumb.Min.X
			if axis.Orientation == "vertical" {
				grabOffset = point.Y - thumb.Min.Y
			}
			r.scrollCapture = &scrollbarCapture{axis: axis, track: track, thumb: thumb, grabOffset: grabOffset, drag: true}
			r.captureID = pointerID
			r.captureHandle = axis.Handle
			r.transient.Pressed = axis.Handle
			r.transient.Focused = axis.Handle
			return true
		}
		thumb := scrollbarThumb(axis)
		page := scrollbarPage(axis)
		if thumb == nil || thumb.Bounds == nil {
			return false
		}
		thumbBounds := thumb.Bounds.ImageRectangle()
		delta := 0
		if axis.Orientation == "vertical" {
			if point.Y < thumbBounds.Min.Y {
				delta = -page
			} else if point.Y >= thumbBounds.Max.Y {
				delta = page
			}
		} else if point.X < thumbBounds.Min.X {
			delta = -page
		} else if point.X >= thumbBounds.Max.X {
			delta = page
		}
		if delta != 0 {
			r.transient.Focused = axis.Handle
			r.queueScrollbarDelta(axis, delta)
			r.scrollCapture = &scrollbarCapture{axis: axis, track: axis.Bounds.ImageRectangle(), thumb: thumbBounds}
			r.captureID = pointerID
			r.captureHandle = axis.Handle
			r.transient.Pressed = axis.Handle
			return true
		}
		return false
	}
	r.captureID = pointerID
	r.captureHandle = region.Handle
	r.transient.Pressed = region.Handle
	r.transient.Focused = region.Handle
	if region.Role == "slider" {
		r.queueSliderValue(region, point)
	}
	return true
}

func (r *Router) Release(pointerID int, point image.Point) (Activation, bool) {
	if pointerID != r.captureID || r.captureHandle == "" {
		return Activation{}, false
	}
	if r.scrollCapture != nil {
		var final *ScrollChange
		if r.scrollCapture.drag && point != image.Pt(-1, -1) {
			r.queueScrollbarDrag(point)
			if r.scrollChange != nil {
				copy := *r.scrollChange
				final = &copy
			}
		}
		r.cancelPress()
		if final != nil {
			r.scrollChange = final
		}
		return Activation{}, false
	}
	handle := r.captureHandle
	captured := r.enabledRegion(handle)
	r.cancelPress()
	if captured != nil && captured.Role == "slider" {
		return Activation{}, false
	}
	region := r.hit(point)
	if region == nil || region.Handle != handle {
		return Activation{}, false
	}
	activation := activationFor(region)
	if activation.OpenSelect != "" {
		r.openSelect(region)
		activation.ActiveOption = r.transient.ActiveOption
	}
	return activation, true
}

func (r *Router) Cancel(pointerID int) {
	if pointerID == r.captureID {
		hadScrollbarCapture := r.scrollCapture != nil
		r.cancelPress()
		if hadScrollbarCapture {
			r.scrollChange = nil
		}
	}
}

func (r *Router) FocusNext(reverse bool) string {
	if r.inspecting {
		return ""
	}
	r.closeSelect()
	var enabled []*semantic.Node
	for _, region := range r.regions {
		if region.Visible && region.Enabled && region.Bounds != nil && region.FocusOrder >= 0 {
			enabled = append(enabled, region)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool { return enabled[i].FocusOrder < enabled[j].FocusOrder })
	if len(enabled) == 0 {
		r.transient.Focused = ""
		return ""
	}
	index := -1
	for i := range enabled {
		if enabled[i].Handle == r.transient.Focused {
			index = i
			break
		}
	}
	if index < 0 {
		if current := r.enabledRegion(r.transient.Focused); current != nil && current.Group != "" {
			for i := range enabled {
				if enabled[i].Group == current.Group && enabled[i].Role == current.Role {
					index = i
					break
				}
			}
		}
	}
	if reverse {
		if index <= 0 {
			index = len(enabled) - 1
		} else {
			index--
		}
	} else {
		index = (index + 1) % len(enabled)
	}
	r.transient.Focused = enabled[index].Handle
	return r.transient.Focused
}

func (r *Router) KeyDown(name string) (Activation, bool) {
	if r.inspecting {
		return Activation{}, false
	}
	region := r.enabledRegion(r.transient.Focused)
	if r.transient.OpenSelect != "" {
		switch name {
		case "Escape":
			r.closeSelect()
			return Activation{}, false
		case "ArrowDown", "ArrowRight":
			r.moveActiveOption(1, false)
			return Activation{}, false
		case "ArrowUp", "ArrowLeft":
			r.moveActiveOption(-1, false)
			return Activation{}, false
		case "Home":
			r.moveActiveOption(1, true)
			return Activation{}, false
		case "End":
			r.moveActiveOption(-1, true)
			return Activation{}, false
		case "PageDown":
			r.moveActiveOption(10, false)
			return Activation{}, false
		case "PageUp":
			r.moveActiveOption(-10, false)
			return Activation{}, false
		case "Enter", "Space":
			if option := r.enabledRegion(r.transient.ActiveOption); option != nil && option.Role == "option" {
				activation := activationFor(option)
				activation.CloseSelect = true
				return activation, true
			}
		}
	}
	if region != nil && (region.Role == "radio" || region.Role == "tab") {
		switch name {
		case "ArrowRight", "ArrowDown":
			return r.moveComposite(region, 1, false)
		case "ArrowLeft", "ArrowUp":
			return r.moveComposite(region, -1, false)
		case "Home":
			return r.moveComposite(region, 1, true)
		case "End":
			return r.moveComposite(region, -1, true)
		}
	}
	if region != nil && (region.Role == "slider" || region.Role == "spinbutton") {
		if activation, ok := numericKeyActivation(region, name); ok {
			return activation, true
		}
	}
	if region != nil && region.Role == "scrollbar" {
		r.queueScrollbarKey(region, name)
		return Activation{}, false
	}
	switch name {
	case "Enter":
		if region != nil {
			if region.Role != "checkbox" && region.Role != "switch" {
				activation := activationFor(region)
				if activation.OpenSelect != "" {
					r.openSelect(region)
				}
				return activation, true
			}
		}
	case "Space":
		if region != nil {
			if region.Role == "combobox" {
				r.openSelect(region)
				return Activation{OpenSelect: region.Handle, ActiveOption: r.transient.ActiveOption}, true
			}
			r.keyboardPress = region.Handle
			r.keyboardKey = name
			r.transient.Pressed = region.Handle
		}
	case "Escape":
		r.keyboardPress = ""
		r.transient.Pressed = ""
	}
	return Activation{}, false
}

func (r *Router) SyncTransient(value Transient) {
	r.transient = value
}

func (r *Router) TakeValueChange() (ControlValueChange, bool) {
	if r.valueChange == nil {
		return ControlValueChange{}, false
	}
	result := *r.valueChange
	r.valueChange = nil
	return result, true
}

func (r *Router) TakeScrollChange() (ScrollChange, bool) {
	if r.scrollChange == nil {
		return ScrollChange{}, false
	}
	result := *r.scrollChange
	r.scrollChange = nil
	return result, true
}

func interactiveRole(role string) bool {
	switch role {
	case "button", "link", "textbox", "switch", "checkbox", "radio", "tab", "combobox", "option", "slider", "spinbutton", "scrollbar":
		return true
	default:
		return false
	}
}

func (r *Router) KeyUp(name string) (Activation, bool) {
	if name != "Space" || r.keyboardPress == "" {
		return Activation{}, false
	}
	handle := r.keyboardPress
	r.keyboardPress = ""
	r.transient.Pressed = ""
	if region := r.enabledRegion(handle); region != nil && r.transient.Focused == handle {
		return activationFor(region), true
	}
	return Activation{}, false
}

func (r *Router) Transient() Transient { return r.transient }

// ScrollbarPointerOwned reports whether a primary pointer is currently owned
// by a derived scrollbar track or thumb. Hosts use this to keep field
// selection, slider drags, canvas panning, and wheel routing from stealing the
// gesture.
func (r *Router) ScrollbarPointerOwned() bool { return r != nil && r.scrollCapture != nil }

// ScrollbarCaptured is the compatibility name for ScrollbarPointerOwned.
func (r *Router) ScrollbarCaptured() bool { return r.ScrollbarPointerOwned() }

// SetPointerMetadata records host-provided pointer details without dispatching
// an event or changing routing state.
func (r *Router) SetPointerMetadata(source string, buttons int, point image.Point) {
	if r == nil {
		return
	}
	r.pointerSource = source
	r.pointerButtons = buttons
	r.pointerPoint = point
	r.pointerPointSet = true
}

// Snapshot returns a deep copy of all observable router state. Snapshot reads
// do not consume pending value/scroll changes or alter focus/capture.
func (r *Router) Snapshot() RouterSnapshot {
	if r == nil {
		return RouterSnapshot{HoveredIDs: []string{}, PressedIDs: []string{}, ActiveIDs: []string{}, DisabledIDs: []string{}}
	}
	result := RouterSnapshot{
		HoveredIDs: []string{}, PressedIDs: []string{}, ActiveIDs: []string{}, DisabledIDs: []string{},
		OpenSelectID: r.semanticID(r.transient.OpenSelect), Inspecting: r.inspecting,
	}
	result.FocusedID = r.semanticID(r.transient.Focused)
	if id := r.semanticID(r.transient.Hovered); id != "" {
		result.HoveredIDs = []string{id}
	}
	if id := r.semanticID(r.transient.Pressed); id != "" {
		result.PressedIDs = []string{id}
	}
	if id := r.semanticID(r.transient.ActiveOption); id != "" {
		result.ActiveIDs = []string{id}
	}
	for _, region := range r.regions {
		if region == nil || region.Enabled {
			continue
		}
		if id := r.semanticID(region.Handle); id != "" {
			result.DisabledIDs = append(result.DisabledIDs, id)
		}
	}
	if r.captureHandle != "" && r.captureID >= 0 {
		if id := r.semanticID(r.captureHandle); id != "" {
			capture := &PointerCaptureSnapshot{OwnerID: id, PointerID: r.captureID, Source: r.pointerSource, Buttons: r.pointerButtons}
			if r.pointerPointSet {
				capture.Point = r.pointerPoint
			}
			result.PointerCapture = capture
		}
	}
	if r.keyboardPress != "" {
		if id := r.semanticID(r.keyboardPress); id != "" {
			result.KeyboardPress = &KeyboardPressSnapshot{OwnerID: id, Key: r.keyboardKey}
		}
	}
	if r.scrollCapture != nil && r.scrollCapture.axis != nil {
		result.ScrollbarGestureOwner = r.semanticID(r.scrollCapture.axis.Handle)
	}
	if r.captureHandle != "" {
		if region := r.enabledRegion(r.captureHandle); region != nil && region.Role == "slider" {
			result.SliderGestureOwner = r.semanticID(region.Handle)
		}
	}
	if r.valueChange != nil {
		result.QueueSizes.ValueChanges = 1
	}
	if r.scrollChange != nil {
		result.QueueSizes.ScrollChanges = 1
	}
	return result
}

func (r *Router) semanticID(handle string) string {
	if handle == "" {
		return ""
	}
	for _, region := range r.regions {
		if region != nil && region.Handle == handle {
			return region.ID
		}
	}
	return handle
}

func (r *Router) hit(point image.Point) *semantic.Node {
	if r.tree != nil {
		return semantic.TopmostAt(r.tree, point, func(node *semantic.Node) bool {
			return node.Enabled && (interactiveRole(node.Role) || scrollbarPart(node))
		})
	}
	for index := len(r.regions) - 1; index >= 0; index-- {
		region := r.regions[index]
		if !region.Visible || !region.Enabled || region.Bounds == nil || region.Clip == nil {
			continue
		}
		if point.In(region.Bounds.ImageRectangle().Intersect(region.Clip.ImageRectangle())) {
			return region
		}
	}
	return nil
}

func (r *Router) enabledRegion(handle string) *semantic.Node {
	for index := range r.regions {
		region := r.regions[index]
		if region.Handle == handle && region.Visible && region.Enabled && region.Bounds != nil {
			return region
		}
	}
	return nil
}

func (r *Router) cancelPress() {
	r.captureID = -1
	r.captureHandle = ""
	r.keyboardPress = ""
	r.keyboardKey = ""
	r.scrollCapture = nil
	r.valueChange = nil
	r.scrollChange = nil
	r.pointerSource = ""
	r.pointerButtons = 0
	r.pointerPoint = image.Point{}
	r.pointerPointSet = false
	r.transient.Pressed = ""
}

func scrollbarPart(node *semantic.Node) bool {
	if node == nil {
		return false
	}
	switch node.Type {
	case "scrollbar_track", "scrollbar_thumb", "scrollbar_corner":
		return true
	default:
		return false
	}
}

func (r *Router) scrollbarAxis(region *semantic.Node) *semantic.Node {
	if region == nil {
		return nil
	}
	if region.Role == "scrollbar" {
		return region
	}
	if !scrollbarPart(region) || region.Group == "" {
		return nil
	}
	for _, candidate := range r.regions {
		if candidate.Role == "scrollbar" && candidate.Handle == region.Group {
			return candidate
		}
	}
	return nil
}

func scrollbarThumb(axis *semantic.Node) *semantic.Node {
	if axis == nil {
		return nil
	}
	for _, child := range axis.Children {
		if child != nil && child.Type == "scrollbar_thumb" {
			return child
		}
	}
	return nil
}

func scrollbarPage(axis *semantic.Node) int {
	if axis == nil {
		return 1
	}
	length := 0
	if axis.Orientation == "vertical" {
		if axis.ViewportSize != nil {
			length = axis.ViewportSize.Height
		}
	} else if axis.ViewportSize != nil {
		length = axis.ViewportSize.Width
	}
	return max(1, length-16)
}

func (r *Router) queueScrollbarDelta(axis *semantic.Node, delta int) {
	if axis == nil || delta == 0 {
		return
	}
	change := ScrollChange{ID: axis.ID, Mode: "by"}
	if axis.Orientation == "vertical" {
		change.Y = delta
	} else {
		change.X = delta
	}
	r.scrollChange = &change
}

func (r *Router) queueScrollbarDrag(point image.Point) {
	capture := r.scrollCapture
	if capture == nil || capture.axis == nil || capture.axis.Max == nil {
		return
	}
	trackLength := capture.track.Dx()
	coordinate := point.X
	trackStart := capture.track.Min.X
	thumbLength := capture.thumb.Dx()
	if capture.axis.Orientation == "vertical" {
		trackLength = capture.track.Dy()
		coordinate = point.Y
		trackStart = capture.track.Min.Y
		thumbLength = capture.thumb.Dy()
	}
	travel := max(0, trackLength-thumbLength)
	position := coordinate - capture.grabOffset - trackStart
	position = min(max(0, position), travel)
	value := 0
	if travel > 0 {
		value = int(math.Floor(float64(position)*(*capture.axis.Max)/float64(travel) + .5))
	}
	change := ScrollChange{ID: capture.axis.ID, Mode: "to"}
	if capture.axis.Orientation == "vertical" {
		change.Y = value
	} else {
		change.X = value
	}
	r.scrollChange = &change
}

func (r *Router) queueScrollbarKey(axis *semantic.Node, name string) {
	if axis == nil || axis.Max == nil {
		return
	}
	page := scrollbarPage(axis)
	change := ScrollChange{ID: axis.ID, Mode: "by"}
	switch name {
	case "ArrowUp":
		if axis.Orientation != "vertical" {
			return
		}
		change.Y = -40
	case "ArrowDown":
		if axis.Orientation != "vertical" {
			return
		}
		change.Y = 40
	case "ArrowLeft":
		if axis.Orientation != "horizontal" {
			return
		}
		change.X = -40
	case "ArrowRight":
		if axis.Orientation != "horizontal" {
			return
		}
		change.X = 40
	case "PageUp":
		if axis.Orientation == "vertical" {
			change.Y = -page
		} else {
			change.X = -page
		}
	case "PageDown":
		if axis.Orientation == "vertical" {
			change.Y = page
		} else {
			change.X = page
		}
	case "Home":
		change.Mode = "to"
	case "End":
		change.Mode = "to"
		if axis.Orientation == "vertical" {
			change.Y = int(*axis.Max)
		} else {
			change.X = int(*axis.Max)
		}
	default:
		return
	}
	r.scrollChange = &change
}

func (r *Router) enabledRegionByIdentity(identity string) *semantic.Node {
	for _, region := range r.regions {
		if region != nil && (region.Handle == identity || region.ID == identity) && region.Visible && region.Enabled && region.Bounds != nil {
			return region
		}
	}
	return nil
}

func (r *Router) moveComposite(current *semantic.Node, delta int, edge bool) (Activation, bool) {
	items := r.enabledGroup(current.Group, current.Role)
	if len(items) == 0 {
		return Activation{}, false
	}
	index := indexOfHandle(items, current.Handle)
	if edge {
		if delta < 0 {
			index = len(items) - 1
		} else {
			index = 0
		}
	} else {
		index = (index + delta%len(items) + len(items)) % len(items)
	}
	r.transient.Focused = items[index].Handle
	return activationFor(items[index]), true
}

func (r *Router) enabledGroup(group, role string) []*semantic.Node {
	var result []*semantic.Node
	for _, region := range r.regions {
		if region.Group == group && region.Role == role && region.Visible && region.Enabled && region.Bounds != nil {
			result = append(result, region)
		}
	}
	return result
}

func indexOfHandle(items []*semantic.Node, handle string) int {
	for index, item := range items {
		if item.Handle == handle {
			return index
		}
	}
	return 0
}

func (r *Router) openSelect(selectNode *semantic.Node) {
	r.transient.OpenSelect = selectNode.Handle
	options := r.enabledGroup(selectNode.Handle, "option")
	for _, option := range options {
		if option.Selected != nil && *option.Selected {
			r.transient.ActiveOption = option.Handle
			return
		}
	}
	if len(options) > 0 {
		r.transient.ActiveOption = options[0].Handle
	}
}

func (r *Router) closeSelect() {
	r.transient.OpenSelect = ""
	r.transient.ActiveOption = ""
}

func (r *Router) moveActiveOption(delta int, edge bool) {
	options := r.enabledGroup(r.transient.OpenSelect, "option")
	if len(options) == 0 {
		return
	}
	index := indexOfHandle(options, r.transient.ActiveOption)
	if edge {
		if delta < 0 {
			index = len(options) - 1
		} else {
			index = 0
		}
	} else {
		index = min(max(0, index+delta), len(options)-1)
	}
	r.transient.ActiveOption = options[index].Handle
}

func numericKeyActivation(region *semantic.Node, name string) (Activation, bool) {
	if region.Binding == "" {
		return Activation{}, false
	}
	step := 1.0
	if region.Step != nil {
		step = *region.Step
	}
	action := document.Action{State: region.Binding}
	switch name {
	case "ArrowRight", "ArrowUp":
		action.Action, action.By = "increment", step
	case "ArrowLeft", "ArrowDown":
		action.Action, action.By = "decrement", step
	case "PageUp":
		action.Action, action.By = "increment", step*10
	case "PageDown":
		action.Action, action.By = "decrement", step*10
	case "Home":
		if region.Min == nil {
			return Activation{}, false
		}
		action.Action, action.Value = "set", *region.Min
	case "End":
		if region.Max == nil {
			return Activation{}, false
		}
		action.Action, action.Value = "set", *region.Max
	default:
		return Activation{}, false
	}
	return Activation{Scope: region.Scope, Actions: []document.Action{action}}, true
}

func (r *Router) queueSliderValue(region *semantic.Node, point image.Point) {
	if region.ID == "" || region.Min == nil || region.Max == nil || region.Bounds == nil {
		return
	}
	bounds := region.Bounds.ImageRectangle()
	fraction := 0.0
	if region.Orientation == "vertical" {
		if bounds.Dy() > 0 {
			fraction = float64(bounds.Max.Y-point.Y) / float64(bounds.Dy())
		}
	} else if bounds.Dx() > 0 {
		fraction = float64(point.X-bounds.Min.X) / float64(bounds.Dx())
	}
	fraction = math.Max(0, math.Min(1, fraction))
	r.valueChange = &ControlValueChange{ID: region.ID, Value: *region.Min + fraction*(*region.Max-*region.Min)}
}

func activationFor(region *semantic.Node) Activation {
	activation := Activation{Scope: region.Scope, Actions: append([]document.Action(nil), region.Actions...)}
	if region.Role == "combobox" {
		activation.OpenSelect = region.Handle
	}
	if region.Role == "option" {
		activation.CloseSelect = true
	}
	return activation
}
