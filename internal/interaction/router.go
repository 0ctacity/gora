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

// Router applies deterministic hit testing, pointer capture, focus, and keyboard activation.
type Router struct {
	regions       []*semantic.Node
	transient     Transient
	captureID     int
	captureHandle string
	keyboardPress string
	valueChange   *ControlValueChange
	inspecting    bool
}

func NewRouter() *Router { return &Router{captureID: -1} }

func (r *Router) Update(tree *semantic.Node) {
	r.regions = r.regions[:0]
	for _, node := range semantic.Flatten(tree) {
		if interactiveRole(node.Role) {
			r.regions = append(r.regions, node)
		}
	}
	sort.SliceStable(r.regions, func(i, j int) bool {
		return r.regions[i].PaintOrder < r.regions[j].PaintOrder
	})
	if r.transient.Focused != "" && r.enabledRegion(r.transient.Focused) == nil {
		r.transient.Focused = ""
	}
	if r.transient.Hovered != "" && r.enabledRegion(r.transient.Hovered) == nil {
		r.transient.Hovered = ""
	}
	if r.transient.Pressed != "" && r.enabledRegion(r.transient.Pressed) == nil {
		r.cancelPress()
	}
}

func (r *Router) SetInspecting(enabled bool) {
	r.inspecting = enabled
	if enabled {
		r.transient = Transient{}
		r.captureID = -1
		r.captureHandle = ""
		r.keyboardPress = ""
	}
}

func (r *Router) Move(point image.Point, touch bool) {
	if r.inspecting {
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
		r.cancelPress()
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

func interactiveRole(role string) bool {
	switch role {
	case "button", "link", "textbox", "switch", "checkbox", "radio", "tab", "combobox", "option", "slider", "spinbutton":
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

func (r *Router) hit(point image.Point) *semantic.Node {
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
	r.transient.Pressed = ""
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
