package interaction

import (
	"fmt"
	"strconv"

	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/semantic"
)

// ResolveTree evaluates persistent state and variants into a new immutable tree.
func ResolveTree(root *project.Node, values map[string]map[string]any, transient Transient) *project.Node {
	return resolveTree(root, values, transient, true, controlContext{}, nil, "")
}

// ResolvePersistentTree evaluates document state while leaving transient button
// variants for the renderer's retained paint replay.
func ResolvePersistentTree(root *project.Node, values map[string]map[string]any) *project.Node {
	return resolveTree(root, values, Transient{}, false, controlContext{}, nil, "")
}

// ResolveTreeWithFields evaluates document state and injects renderer-neutral
// field drafts and validation state into the immutable effective tree.
func ResolveTreeWithFields(root *project.Node, values map[string]map[string]any, transient Transient, fields map[string]EditingState, screen string) *project.Node {
	return resolveTree(root, values, transient, true, controlContext{}, fields, screen)
}

// ResolvePersistentTreeWithFields is the retained-scene variant of ResolveTreeWithFields.
func ResolvePersistentTreeWithFields(root *project.Node, values map[string]map[string]any, fields map[string]EditingState, screen string) *project.Node {
	return resolveTree(root, values, Transient{}, false, controlContext{}, fields, screen)
}

type controlContext struct {
	kind        string
	value       any
	checked     bool
	selected    bool
	open        bool
	active      bool
	field       *EditingState
	placeholder string
	fieldHandle string
	minLines    any
	maxLines    any
}

func resolveTree(root *project.Node, values map[string]map[string]any, transient Transient, includeInteraction bool, inherited controlContext, fields map[string]EditingState, screen string) *project.Node {
	if root == nil {
		return nil
	}
	clone := *root
	clone.Props = resolveMap(root.Props, values)
	clone.Place = resolveMap(root.Place, values)
	clone.Children = nil
	control := inherited
	if root.Type == "text_field" || root.Type == "text_area" {
		control.kind = root.Type
		control.placeholder, _ = clone.Props["placeholder"].(string)
		control.fieldHandle = root.Handle
		control.minLines = clone.Props["min_lines"]
		control.maxLines = clone.Props["max_lines"]
		if state, ok := fields[semantic.StableID(root, screen)]; ok {
			copy := state
			control.field = &copy
			clone.Props["draft"] = state.Draft
			clone.Props["committed"] = state.Committed
			clone.Props["dirty"] = state.Dirty
			clone.Props["touched"] = state.Touched
			if state.Validated {
				clone.Props["valid"] = state.Valid
				clone.Props["issues"] = append([]ValidationIssue(nil), state.Issues...)
			} else {
				delete(clone.Props, "valid")
				delete(clone.Props, "issues")
			}
			clone.Props["selection_start"] = state.SelectionStart
			clone.Props["selection_end"] = state.SelectionEnd
			clone.Props["composing"] = state.Composing
			clone.Props["composition"] = state.Composition
			clone.Props["composition_start"] = state.CompositionStart
			clone.Props["composition_end"] = state.CompositionEnd
		}
	}
	if root.Type == "field_box" && control.field != nil {
		text := control.field.Draft
		if text == "" {
			text = control.placeholder
			if color, ok := clone.Props["placeholder_color"]; ok {
				clone.Props["color"] = color
			}
		}
		clone.Props["text"] = text
		clone.Props["field_handle"] = control.fieldHandle
		clone.Props["field_multiline"] = control.kind == "text_area"
		if control.minLines != nil {
			clone.Props["field_min_lines"] = control.minLines
		}
		if control.maxLines != nil {
			clone.Props["field_max_lines"] = control.maxLines
		}
		clone.Props["selection_start"] = runeOffsetForGrapheme(control.field.Draft, control.field.SelectionStart)
		clone.Props["selection_end"] = runeOffsetForGrapheme(control.field.Draft, control.field.SelectionEnd)
		clone.Props["composing"] = control.field.Composing
		clone.Props["composition"] = control.field.Composition
		clone.Props["composition_start"] = runeOffsetForGrapheme(control.field.Draft, control.field.CompositionStart)
		clone.Props["composition_end"] = runeOffsetForGrapheme(control.field.Draft, control.field.CompositionEnd)
		clone.Props["internal_offset"] = control.field.InternalOffset
		clone.Props["manual_internal_scroll"] = control.field.ManualScroll
	}
	if root.Binding != "" && bindingOwner(root.Type) {
		control = controlContext{kind: root.Type, value: values[root.Scope][root.Binding]}
		clone.Props["value"] = control.value
		if root.Type == "toggle" || root.Type == "checkbox" {
			control.checked, _ = control.value.(bool)
			clone.Props["checked"] = control.checked
		}
		if root.Type == "select" {
			control.open = includeInteraction && transient.OpenSelect == root.Handle
			clone.Props["open"] = control.open
		}
	}
	if root.Type == "radio" || root.Type == "tab" || root.Type == "option" {
		control.selected = comparableEqual(control.value, clone.Props["value"])
		clone.Props["selected"] = control.selected
		if root.Type == "option" {
			control.active = includeInteraction && transient.ActiveOption == root.Handle
			clone.Props["active"] = control.active
		}
	}
	visible := true
	for _, variant := range root.Variants {
		if variant.When.Interaction != "" && !includeInteraction && !persistentInteractionCondition(variant.When.Interaction) {
			continue
		}
		if conditionMatches(variant.When, root, clone.Props, values, transient, control) {
			clone.Props = merge(clone.Props, resolveMap(variant.Props, values))
			clone.Place = merge(clone.Place, resolveMap(variant.Place, values))
			if variant.Visible != nil {
				visible = *variant.Visible
			}
		}
	}
	if root.Type == "tab_panel" && !comparableEqual(control.value, clone.Props["value"]) {
		visible = false
	}
	if root.Type == "select_popup" && !control.open {
		visible = false
	}
	clone.Hidden = clone.Hidden || !visible
	if root.Type == "text" {
		for _, key := range []string{"text", "content"} {
			if value, ok := clone.Props[key]; ok {
				clone.Props[key] = scalarText(value)
			}
		}
	}
	for _, child := range root.Children {
		childControl := control
		if root.Type == "radio" || root.Type == "tab" || root.Type == "option" {
			childControl.selected = control.selected
		}
		if resolved := resolveTree(child, values, transient, includeInteraction, childControl, fields, screen); resolved != nil {
			clone.Children = append(clone.Children, resolved)
		}
	}
	return &clone
}

func persistentInteractionCondition(condition string) bool {
	switch condition {
	case "checked", "selected", "read_only", "valid", "invalid", "dirty", "touched", "placeholder_shown":
		return true
	default:
		return false
	}
}

func bindingOwner(nodeType string) bool {
	switch nodeType {
	case "toggle", "checkbox", "radio_group", "tabs", "select", "slider", "stepper":
		return true
	default:
		return false
	}
}

func conditionMatches(condition document.Condition, node *project.Node, props map[string]any, values map[string]map[string]any, transient Transient, control controlContext) bool {
	if condition.Interaction != "" {
		switch condition.Interaction {
		case "hovered":
			return transient.Hovered == node.Handle
		case "pressed":
			return transient.Pressed == node.Handle
		case "focused":
			return transient.Focused == node.Handle
		case "disabled":
			disabled, _ := props["disabled"].(bool)
			return disabled
		case "read_only":
			readOnly, _ := props["read_only"].(bool)
			return readOnly
		case "checked":
			return control.checked
		case "selected":
			return control.selected
		case "open":
			return control.open
		case "active":
			return control.active
		case "valid":
			return control.field != nil && control.field.Validated && control.field.Valid
		case "invalid":
			return control.field != nil && control.field.Validated && !control.field.Valid
		case "dirty":
			return control.field != nil && control.field.Dirty
		case "touched":
			return control.field != nil && control.field.Touched
		case "placeholder_shown":
			return control.field != nil && control.field.Draft == ""
		case "editing":
			return control.field != nil && transient.Focused == node.Handle
		case "composing":
			return control.field != nil && control.field.Composing
		}
		return false
	}
	left, ok := values[node.Scope][condition.State]
	if !ok {
		return false
	}
	right := resolveDynamic(condition.Value, values)
	switch condition.Operator {
	case "equals":
		return comparableEqual(left, right)
	case "not_equals":
		return !comparableEqual(left, right)
	}
	leftNumber, leftOK := numberValue(left)
	rightNumber, rightOK := numberValue(right)
	if !leftOK || !rightOK {
		return false
	}
	switch condition.Operator {
	case "less_than":
		return leftNumber < rightNumber
	case "less_than_or_equal":
		return leftNumber <= rightNumber
	case "greater_than":
		return leftNumber > rightNumber
	case "greater_than_or_equal":
		return leftNumber >= rightNumber
	}
	return false
}

func resolveMap(source map[string]any, values map[string]map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = resolveDynamic(value, values)
	}
	return result
}

func resolveDynamic(value any, values map[string]map[string]any) any {
	switch value := value.(type) {
	case project.StateReference:
		return values[value.Scope][value.Name]
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = resolveDynamic(child, values)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = resolveDynamic(child, values)
		}
		return result
	default:
		return value
	}
}

func scalarText(value any) any {
	switch value := value.(type) {
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(value, 10)
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func comparableEqual(left, right any) bool {
	if leftNumber, ok := numberValue(left); ok {
		rightNumber, rightOK := numberValue(right)
		return rightOK && leftNumber == rightNumber
	}
	switch left := left.(type) {
	case bool:
		right, ok := right.(bool)
		return ok && left == right
	case string:
		right, ok := right.(string)
		return ok && left == right
	}
	return false
}

func merge(base, override map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		result[key] = value
	}
	return result
}
