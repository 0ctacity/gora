package interaction

import (
	"fmt"
	"strconv"

	"gora/internal/document"
	"gora/internal/project"
)

// ResolveTree evaluates persistent state and variants into a new immutable tree.
func ResolveTree(root *project.Node, values map[string]map[string]any, transient Transient) *project.Node {
	return resolveTree(root, values, transient, true, controlContext{})
}

// ResolvePersistentTree evaluates document state while leaving transient button
// variants for the renderer's retained paint replay.
func ResolvePersistentTree(root *project.Node, values map[string]map[string]any) *project.Node {
	return resolveTree(root, values, Transient{}, false, controlContext{})
}

type controlContext struct {
	kind     string
	value    any
	checked  bool
	selected bool
	open     bool
	active   bool
}

func resolveTree(root *project.Node, values map[string]map[string]any, transient Transient, includeInteraction bool, inherited controlContext) *project.Node {
	if root == nil {
		return nil
	}
	clone := *root
	clone.Props = resolveMap(root.Props, values)
	clone.Place = resolveMap(root.Place, values)
	clone.Children = nil
	control := inherited
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
		if variant.When.Interaction != "" && !includeInteraction && variant.When.Interaction != "checked" && variant.When.Interaction != "selected" {
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
		if resolved := resolveTree(child, values, transient, includeInteraction, childControl); resolved != nil {
			clone.Children = append(clone.Children, resolved)
		}
	}
	return &clone
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
		case "checked":
			return control.checked
		case "selected":
			return control.selected
		case "open":
			return control.open
		case "active":
			return control.active
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
