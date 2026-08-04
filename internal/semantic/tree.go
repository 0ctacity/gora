package semantic

import (
	"image"
	"net/url"
	"strconv"
	"strings"

	"gora/internal/document"
	"gora/internal/project"
)

type Geometry struct {
	Bounds     image.Rectangle
	Clip       image.Rectangle
	PaintOrder int
	Props      map[string]any
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Source struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type Effect struct {
	Action string `json:"action"`
	State  string `json:"state,omitempty"`
	To     string `json:"to,omitempty"`
	Value  any    `json:"value,omitempty"`
	By     any    `json:"by,omitempty"`
}

// Node is the canonical renderer-neutral runtime inspection node.
type Node struct {
	ID                   string            `json:"id"`
	Handle               string            `json:"-"`
	Type                 string            `json:"type"`
	Name                 string            `json:"name,omitempty"`
	Role                 string            `json:"role,omitempty"`
	Label                string            `json:"label,omitempty"`
	Value                any               `json:"value,omitempty"`
	CommittedValue       any               `json:"committed_value,omitempty"`
	Enabled              bool              `json:"enabled"`
	Current              bool              `json:"current,omitempty"`
	Checked              *bool             `json:"checked,omitempty"`
	Selected             *bool             `json:"selected,omitempty"`
	Expanded             *bool             `json:"expanded,omitempty"`
	ReadOnly             bool              `json:"read_only,omitempty"`
	Required             bool              `json:"required,omitempty"`
	Multiline            bool              `json:"multiline,omitempty"`
	Placeholder          string            `json:"placeholder,omitempty"`
	Dirty                bool              `json:"dirty,omitempty"`
	Touched              bool              `json:"touched,omitempty"`
	Valid                *bool             `json:"valid,omitempty"`
	Issues               any               `json:"issues,omitempty"`
	SelectionStart       int               `json:"selection_start,omitempty"`
	SelectionEnd         int               `json:"selection_end,omitempty"`
	Composition          string            `json:"composition,omitempty"`
	CompositionStart     int               `json:"composition_start,omitempty"`
	CompositionEnd       int               `json:"composition_end,omitempty"`
	Composing            bool              `json:"composing,omitempty"`
	PlaceholderShown     bool              `json:"placeholder_shown,omitempty"`
	InternalOffset       float64           `json:"internal_text_offset,omitempty"`
	InternalTextViewport *Rect             `json:"internal_text_viewport,omitempty"`
	Min                  *float64          `json:"min,omitempty"`
	Max                  *float64          `json:"max,omitempty"`
	Step                 *float64          `json:"step,omitempty"`
	Orientation          string            `json:"orientation,omitempty"`
	Visible              bool              `json:"visible"`
	InViewport           bool              `json:"in_viewport"`
	Bounds               *Rect             `json:"bounds"`
	Clip                 *Rect             `json:"clip"`
	Props                map[string]any    `json:"props,omitempty"`
	Place                map[string]any    `json:"place,omitempty"`
	Source               Source            `json:"source"`
	Breadcrumb           []string          `json:"breadcrumb,omitempty"`
	Scope                string            `json:"scope,omitempty"`
	Binding              string            `json:"binding,omitempty"`
	Form                 string            `json:"form,omitempty"`
	FormHandle           string            `json:"-"`
	Group                string            `json:"-"`
	State                map[string]any    `json:"state,omitempty"`
	Hovered              bool              `json:"hovered,omitempty"`
	Pressed              bool              `json:"pressed,omitempty"`
	Focused              bool              `json:"focused,omitempty"`
	FocusOrder           int               `json:"focus_order"`
	PaintOrder           int               `json:"paint_order"`
	Operations           []string          `json:"operations,omitempty"`
	Effects              []Effect          `json:"effects,omitempty"`
	Actions              []document.Action `json:"-"`
	Children             []*Node           `json:"children,omitempty"`
}

type Context struct {
	Screen  string
	Values  map[string]map[string]any
	Hovered string
	Pressed string
	Focused string
}

type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Envelope is the versioned live-inspection response shared by all hosts.
type Envelope struct {
	SchemaVersion       int                   `json:"schema_version"`
	Document            string                `json:"document"`
	HostMode            string                `json:"host_mode"`
	Valid               bool                  `json:"valid"`
	Diagnostics         []document.Diagnostic `json:"diagnostics"`
	RuntimeRevision     uint64                `json:"runtime_revision"`
	CurrentScreen       string                `json:"current_screen,omitempty"`
	CurrentFixture      string                `json:"current_fixture,omitempty"`
	AvailableSelections []string              `json:"available_selections"`
	Viewport            Viewport              `json:"viewport"`
	CanBack             bool                  `json:"can_back"`
	CanForward          bool                  `json:"can_forward"`
	Root                *Node                 `json:"root"`
}

// Build joins the resolved node hierarchy with final logical geometry and
// interaction state without discarding hidden nodes.
func Build(root *project.Node, geometry map[string]Geometry, context Context) *Node {
	tree := build(root, geometry, context, nil, false)
	resolveFormIDs(tree)
	assignFocusOrder(tree)
	return tree
}

func build(source *project.Node, geometry map[string]Geometry, context Context, path []int, ancestorHidden bool) *Node {
	if source == nil {
		return nil
	}
	hidden := ancestorHidden || source.Hidden
	node := &Node{
		ID:         semanticID(source, context.Screen, path),
		Handle:     source.Handle,
		Type:       source.Type,
		Name:       authoredName(source),
		Enabled:    !boolValue(source.Props["disabled"]),
		Visible:    !hidden,
		Props:      cloneMap(source.Props),
		Place:      cloneMap(source.Place),
		Source:     Source{File: source.Source.File, Line: source.Source.Line, Column: source.Source.Column},
		Breadcrumb: append([]string(nil), source.Breadcrumb...),
		Scope:      source.Scope,
		Binding:    source.Binding,
		FormHandle: source.Form,
		State:      cloneMap(context.Values[source.Scope]),
		Hovered:    context.Hovered == source.Handle,
		Pressed:    context.Pressed == source.Handle,
		Focused:    context.Focused == source.Handle,
		FocusOrder: -1,
	}
	if value, ok := source.Props["text"]; ok {
		node.Value = value
	} else if value, ok := source.Props["content"]; ok {
		node.Value = value
	}
	if !hidden {
		if resolved, ok := geometry[source.Handle]; ok {
			node.Bounds = rect(resolved.Bounds)
			node.Clip = rect(resolved.Clip)
			node.PaintOrder = resolved.PaintOrder
			node.InViewport = !resolved.Bounds.Intersect(resolved.Clip).Empty()
			if resolved.Props != nil {
				node.Props = cloneMap(resolved.Props)
			}
		}
	}
	if source.Type == "button" || source.Type == "link" {
		node.Role = source.Type
		node.Label, _ = source.Props["label"].(string)
		node.Operations = []string{"activate", "focus"}
		for _, action := range source.On.Activate {
			node.Effects = append(node.Effects, effect(action))
			node.Actions = append(node.Actions, action)
		}
		if source.Type == "link" {
			target, _ := source.Props["to"].(string)
			node.Current = target != "" && target == context.Screen
			node.Effects = append(node.Effects, Effect{Action: "navigate", To: target})
			node.Actions = append(node.Actions, document.Action{Action: "navigate", To: target})
		}
	}
	if source.Type == "form" {
		node.Role = "form"
		node.Label = node.Name
		node.Operations = []string{"submit", "reset"}
		for _, action := range source.On.Submit {
			node.Effects = append(node.Effects, effect(action))
			node.Actions = append(node.Actions, action)
		}
	}
	if source.Type == "text_field" || source.Type == "text_area" {
		node.Role = "textbox"
		node.Label, _ = source.Props["label"].(string)
		node.Value = source.Props["draft"]
		node.CommittedValue = source.Props["committed"]
		node.ReadOnly, _ = source.Props["read_only"].(bool)
		node.Required, _ = source.Props["required"].(bool)
		node.Multiline = source.Type == "text_area"
		node.Placeholder, _ = source.Props["placeholder"].(string)
		node.Dirty, _ = source.Props["dirty"].(bool)
		node.Touched, _ = source.Props["touched"].(bool)
		node.Issues = source.Props["issues"]
		node.SelectionStart = intValue(source.Props["selection_start"])
		node.SelectionEnd = intValue(source.Props["selection_end"])
		node.Composition, _ = source.Props["composition"].(string)
		node.CompositionStart = intValue(source.Props["composition_start"])
		node.CompositionEnd = intValue(source.Props["composition_end"])
		node.Composing, _ = source.Props["composing"].(bool)
		node.PlaceholderShown = node.Value == ""
		if offset, ok := numericValue(source.Props["internal_offset"]); ok {
			node.InternalOffset = offset
		}
		if valid, ok := source.Props["valid"].(bool); ok {
			node.Valid = boolPointer(valid)
		}
		node.Operations = []string{"set_draft", "set_value", "focus", "select_all"}
	}
	applyControlSemantics(node, source)
	for index, child := range source.Children {
		childPath := append(append([]int(nil), path...), index)
		node.Children = append(node.Children, build(child, geometry, context, childPath, hidden))
	}
	annotateComposite(node)
	return node
}

func resolveFormIDs(root *Node) {
	byHandle := make(map[string]string)
	for _, node := range Flatten(root) {
		if node.Role == "form" {
			byHandle[node.Handle] = node.ID
		}
	}
	for _, node := range Flatten(root) {
		if node.FormHandle != "" {
			node.Form = byHandle[node.FormHandle]
		}
	}
}

func annotateComposite(node *Node) {
	if node == nil {
		return
	}
	switch node.Type {
	case "text_field", "text_area":
		for _, child := range node.Children {
			if child == nil || child.Type != "field_box" {
				continue
			}
			node.InternalTextViewport = &Rect{
				X:      intValue(child.Props["internal_viewport_x"]),
				Y:      intValue(child.Props["internal_viewport_y"]),
				Width:  intValue(child.Props["internal_viewport_width"]),
				Height: intValue(child.Props["internal_viewport_height"]),
			}
			break
		}
	case "radio_group":
		for _, child := range node.Children {
			if child != nil && child.Type == "radio" {
				child.Group = node.Handle
			}
		}
	case "tabs":
		for _, child := range node.Children {
			if child == nil {
				continue
			}
			switch child.Type {
			case "tab":
				child.Group = node.Handle
			case "tab_panel":
				child.ID = node.ID + "/panel/" + escape(valueString(child.Value))
			}
		}
	case "select":
		for _, child := range node.Children {
			if child == nil {
				continue
			}
			switch child.Type {
			case "select_trigger":
				child.ID = node.ID + "/trigger"
			case "select_popup":
				child.ID = node.ID + "/popup"
				assignOptionGroup(child, node.Handle)
			}
		}
	case "slider":
		derivePartIDs(node, map[string]string{"slider_track": "track", "slider_fill": "fill", "slider_thumb": "thumb"})
	case "stepper":
		derivePartIDs(node, map[string]string{"stepper_decrement": "decrement", "stepper_value": "value", "stepper_increment": "increment"})
		value, _ := numericValue(node.Value)
		for _, child := range node.Children {
			if child == nil {
				continue
			}
			if child.Type == "stepper_decrement" && node.Min != nil && value <= *node.Min {
				child.Enabled = false
			}
			if child.Type == "stepper_increment" && node.Max != nil && value >= *node.Max {
				child.Enabled = false
			}
		}
	}
}

func assignOptionGroup(node *Node, group string) {
	if node == nil {
		return
	}
	if node.Type == "option" {
		node.Group = group
	}
	for _, child := range node.Children {
		assignOptionGroup(child, group)
	}
}

func derivePartIDs(node *Node, suffixes map[string]string) {
	for _, child := range node.Children {
		if child != nil {
			if suffix := suffixes[child.Type]; suffix != "" {
				child.ID = node.ID + "/part/" + suffix
				child.Group = node.Handle
			}
		}
	}
}

func valueString(value any) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(toString(value)), "/", "%2F"), " ", "%20"))
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return "value"
	}
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

func applyControlSemantics(node *Node, source *project.Node) {
	role := map[string]string{
		"toggle": "switch", "checkbox": "checkbox", "radio_group": "radiogroup", "radio": "radio",
		"tabs": "tablist", "tab": "tab", "tab_panel": "tabpanel", "select": "combobox",
		"select_popup": "listbox", "option": "option", "slider": "slider", "stepper": "spinbutton",
		"stepper_decrement": "button", "stepper_value": "status", "stepper_increment": "button",
	}[source.Type]
	if role == "" {
		return
	}
	node.Role = role
	node.Label, _ = source.Props["label"].(string)
	if value, exists := source.Props["value"]; exists {
		node.Value = value
	}
	node.Orientation, _ = source.Props["orientation"].(string)
	if node.Orientation == "" && (source.Type == "slider" || source.Type == "tabs") {
		node.Orientation = "horizontal"
	}
	if checked, ok := source.Props["checked"].(bool); ok && (source.Type == "toggle" || source.Type == "checkbox") {
		node.Checked = boolPointer(checked)
	}
	if selected, ok := source.Props["selected"].(bool); ok && (source.Type == "radio" || source.Type == "tab" || source.Type == "option") {
		node.Selected = boolPointer(selected)
	}
	if source.Type == "select" {
		open, _ := source.Props["open"].(bool)
		node.Expanded = boolPointer(open)
	}
	if source.BindingState != nil {
		node.Min = cloneFloat(source.BindingState.Min)
		node.Max = cloneFloat(source.BindingState.Max)
		node.Step = cloneFloat(source.BindingState.Step)
	}
	switch source.Type {
	case "toggle", "checkbox":
		node.Operations = []string{"toggle", "set_value", "focus"}
		if source.Binding != "" {
			action := document.Action{Action: "toggle", State: source.Binding}
			node.Actions = append(node.Actions, action)
			node.Effects = append(node.Effects, effect(action))
		}
	case "radio", "tab", "option":
		node.Operations = []string{"activate", "select"}
		if source.Binding != "" {
			action := document.Action{Action: "set", State: source.Binding, Value: source.Props["value"]}
			node.Actions = append(node.Actions, action)
			node.Effects = append(node.Effects, effect(action))
		}
	case "select":
		node.Operations = []string{"open", "set_value", "focus"}
	case "slider", "stepper":
		node.Operations = []string{"set_value", "increment", "decrement", "focus"}
	case "stepper_decrement", "stepper_increment":
		if source.Binding != "" {
			kind := "increment"
			if source.Type == "stepper_decrement" {
				kind = "decrement"
			}
			action := document.Action{Action: kind, State: source.Binding}
			node.Actions = append(node.Actions, action)
			node.Effects = append(node.Effects, effect(action))
		}
	}
}

func assignFocusOrder(root *Node) {
	if root == nil {
		return
	}
	candidates := make(map[string]bool)
	var choose func(*Node)
	choose = func(node *Node) {
		if node == nil {
			return
		}
		switch node.Type {
		case "button", "link", "text_field", "text_area", "toggle", "checkbox", "select", "slider", "stepper":
			if node.Visible && node.Enabled && node.Bounds != nil {
				candidates[node.Handle] = true
			}
		case "radio_group", "tabs":
			itemType := "radio"
			if node.Type == "tabs" {
				itemType = "tab"
			}
			var first, selected *Node
			for _, child := range node.Children {
				if child == nil || child.Type != itemType || !child.Visible || !child.Enabled || child.Bounds == nil {
					continue
				}
				if first == nil {
					first = child
				}
				if child.Selected != nil && *child.Selected {
					selected = child
				}
			}
			if selected != nil {
				candidates[selected.Handle] = true
			} else if first != nil {
				candidates[first.Handle] = true
			}
		}
		for _, child := range node.Children {
			choose(child)
		}
	}
	choose(root)
	order := 0
	var assign func(*Node)
	assign = func(node *Node) {
		if node == nil {
			return
		}
		if candidates[node.Handle] {
			node.FocusOrder = order
			order++
		}
		for _, child := range node.Children {
			assign(child)
		}
	}
	assign(root)
}

// Flatten returns the runtime tree in source order.
func Flatten(root *Node) []*Node {
	if root == nil {
		return nil
	}
	result := []*Node{root}
	for _, child := range root.Children {
		result = append(result, Flatten(child)...)
	}
	return result
}

func (r *Rect) ImageRectangle() image.Rectangle {
	if r == nil {
		return image.Rectangle{}
	}
	return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
}

func semanticID(node *project.Node, screen string, path []int) string {
	prefix := "screen/" + escape(screen)
	if namedInteractive(node.Type) {
		var result strings.Builder
		result.WriteString(prefix)
		for _, segment := range node.Breadcrumb {
			result.WriteString("/component/")
			result.WriteString(escape(segment))
		}
		result.WriteString("/node/")
		result.WriteString(escape(authoredName(node)))
		return result.String()
	}
	if len(path) == 0 {
		return prefix + "/path/0"
	}
	parts := make([]string, len(path))
	for index, segment := range path {
		parts[index] = strconv.Itoa(segment)
	}
	return prefix + "/path/0/" + strings.Join(parts, "/")
}

func authoredName(node *project.Node) string {
	if node.SourceName != "" {
		return node.SourceName
	}
	return node.Name
}

// StableID returns the reload-stable semantic ID of a named runtime node.
func StableID(node *project.Node, screen string) string {
	if node == nil || !namedInteractive(node.Type) {
		return ""
	}
	return semanticID(node, screen, nil)
}

func namedInteractive(nodeType string) bool {
	switch nodeType {
	case "form", "button", "link", "text_field", "text_area", "toggle", "checkbox", "radio_group", "radio", "tabs", "tab", "select", "option", "slider", "stepper":
		return true
	default:
		return false
	}
}

func effect(action document.Action) Effect {
	return Effect{Action: action.Action, State: action.State, To: action.To, Value: action.Value, By: action.By}
}

func escape(value string) string {
	return url.PathEscape(value)
}

func rect(value image.Rectangle) *Rect {
	return &Rect{X: value.Min.X, Y: value.Min.Y, Width: value.Dx(), Height: value.Dy()}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func boolPointer(value bool) *bool { return &value }

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
