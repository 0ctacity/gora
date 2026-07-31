package interaction

import (
	"image"

	"gora/internal/document"
	"gora/internal/render"
)

type Activation struct {
	Scope   string
	Actions []document.Action
}

// Router applies deterministic hit testing, pointer capture, focus, and keyboard activation.
type Router struct {
	regions       []render.InteractionRegion
	transient     Transient
	captureID     int
	captureHandle string
	keyboardPress string
	inspecting    bool
}

func NewRouter() *Router { return &Router{captureID: -1} }

func (r *Router) Update(regions []render.InteractionRegion) {
	r.regions = append(r.regions[:0], regions...)
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
	if r.inspecting || touch {
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
		return false
	}
	r.captureID = pointerID
	r.captureHandle = region.Handle
	r.transient.Pressed = region.Handle
	r.transient.Focused = region.Handle
	return true
}

func (r *Router) Release(pointerID int, point image.Point) (Activation, bool) {
	if pointerID != r.captureID || r.captureHandle == "" {
		return Activation{}, false
	}
	handle := r.captureHandle
	r.cancelPress()
	region := r.hit(point)
	if region == nil || region.Handle != handle {
		return Activation{}, false
	}
	return activationFor(region), true
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
	var enabled []render.InteractionRegion
	for _, region := range r.regions {
		if !region.Disabled && !region.Bounds.Intersect(region.Clip).Empty() {
			enabled = append(enabled, region)
		}
	}
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
	switch name {
	case "Enter":
		if region := r.enabledRegion(r.transient.Focused); region != nil {
			return activationFor(region), true
		}
	case "Space":
		if region := r.enabledRegion(r.transient.Focused); region != nil {
			r.keyboardPress = region.Handle
			r.transient.Pressed = region.Handle
		}
	case "Escape":
		r.keyboardPress = ""
		r.transient.Pressed = ""
	}
	return Activation{}, false
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

func (r *Router) hit(point image.Point) *render.InteractionRegion {
	for index := len(r.regions) - 1; index >= 0; index-- {
		region := &r.regions[index]
		if region.Disabled {
			continue
		}
		if point.In(region.Bounds.Intersect(region.Clip)) {
			return region
		}
	}
	return nil
}

func (r *Router) enabledRegion(handle string) *render.InteractionRegion {
	for index := range r.regions {
		if r.regions[index].Handle == handle && !r.regions[index].Disabled && !r.regions[index].Bounds.Intersect(r.regions[index].Clip).Empty() {
			return &r.regions[index]
		}
	}
	return nil
}

func (r *Router) cancelPress() {
	r.captureID = -1
	r.captureHandle = ""
	r.transient.Pressed = ""
}

func activationFor(region *render.InteractionRegion) Activation {
	return Activation{Scope: region.Scope, Actions: append([]document.Action(nil), region.Actions...)}
}
