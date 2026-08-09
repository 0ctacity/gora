package studio

import (
	"fmt"

	"gora/internal/interaction"
	"gora/internal/semantic"
)

// FocusedFieldID returns the focused semantic field ID without consuming
// router state. It is used to complete omitted semantic_id edit targets.
func (runtime *Runtime) FocusedFieldID() string {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.focusedFieldIDLocked()
}

func (runtime *Runtime) focusedFieldIDLocked() string {
	if runtime.routerSnapshotSet && runtime.routerSnapshot.FocusedID != "" {
		return runtime.routerSnapshot.FocusedID
	}
	if runtime.router != nil {
		if focused := runtime.router.Snapshot().FocusedID; focused != "" {
			return focused
		}
	}
	if runtime.state == nil {
		return ""
	}
	tree := runtime.publishedTree
	if tree == nil {
		return ""
	}
	handle := runtime.state.Transient().Focused
	for _, node := range semantic.Flatten(tree) {
		if node != nil && node.Handle == handle {
			return node.ID
		}
	}
	return ""
}

// AutomationClipboard returns this view's isolated clipboard text.
func (runtime *Runtime) AutomationClipboard() string {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.automationClipboard
}

// SetAutomationClipboard updates only this view's automation clipboard.
func (runtime *Runtime) SetAutomationClipboard(text string) {
	runtime.mu.Lock()
	runtime.automationClipboard = text
	runtime.mu.Unlock()
}

// ConfigureAutomationFaults installs bounded host-owned fault counters. The
// host control service consumes these counters on the owning event loop.
func (runtime *Runtime) ConfigureAutomationFaults(rules map[string]int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.automationFaults = make(map[string]int, len(rules))
	for kind, remaining := range rules {
		if remaining > 0 {
			runtime.automationFaults[kind] = remaining
		}
	}
}

func (runtime *Runtime) ClearAutomationFaults() {
	runtime.mu.Lock()
	runtime.automationFaults = make(map[string]int)
	runtime.mu.Unlock()
}

func (runtime *Runtime) ConsumeAutomationFault(kind string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	remaining := runtime.automationFaults[kind]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(runtime.automationFaults, kind)
	} else {
		runtime.automationFaults[kind] = remaining - 1
	}
	return true
}

func (runtime *Runtime) focusedEditableField(tree *semantic.Node, expected string) (*semantic.Node, error) {
	focused := runtime.routerSnapshot.FocusedID
	if !runtime.routerSnapshotSet && runtime.router != nil {
		focused = runtime.router.Snapshot().FocusedID
	}
	if focused == "" && runtime.state != nil {
		handle := runtime.state.Transient().Focused
		for _, node := range semantic.Flatten(tree) {
			if node != nil && node.Handle == handle {
				focused = node.ID
				break
			}
		}
	}
	if focused == "" {
		return nil, fmt.Errorf("editing requires a focused field")
	}
	if expected != "" && expected != focused {
		return nil, fmt.Errorf("edit semantic_id %q does not match focused field %q", expected, focused)
	}
	for _, node := range semantic.Flatten(tree) {
		if node == nil || node.ID != focused {
			continue
		}
		if node.Role != "textbox" || !node.Visible || !node.InViewport || !node.Enabled || node.ReadOnly {
			return nil, fmt.Errorf("focused field %q is not editable", focused)
		}
		return node, nil
	}
	return nil, fmt.Errorf("focused field %q is unavailable", focused)
}

// ValidateEditBatch checks evolving grapheme ranges before any command in the
// batch is delivered. Focus/visibility is checked when each command arrives,
// so an earlier pointer/key event may legitimately establish focus.
func (runtime *Runtime) ValidateEditBatch(commands []interaction.EditCommand) error {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	// Focus can legitimately change earlier in the same ordered batch (for
	// example a pointer press followed by an edit). Range/history validation is
	// the structural preflight; ApplyEditCommand rechecks focus at delivery.
	resolved := make([]interaction.EditCommand, len(commands))
	copy(resolved, commands)
	focused := runtime.focusedFieldIDLocked()
	for index := range resolved {
		if resolved[index].FieldID == "" {
			if focused == "" {
				continue
			}
			resolved[index].FieldID = focused
		}
	}
	for _, command := range resolved {
		if command.FieldID == "" {
			continue
		}
		if _, ok := runtime.editing.State(command.FieldID); !ok {
			return fmt.Errorf("unknown field %q", command.FieldID)
		}
	}
	filtered := resolved[:0]
	for _, command := range resolved {
		if command.FieldID != "" {
			filtered = append(filtered, command)
		}
	}
	return runtime.editing.ValidateEditCommandsWithClipboard(filtered, runtime.automationClipboard)
}

// ApplyEditCommand applies one renderer-neutral command atomically with its
// resulting document publication. Clipboard operations remain view-local.
func (runtime *Runtime) ApplyEditCommand(command interaction.EditCommand) error {
	tree, err := runtime.currentRuntimeTree()
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	field, err := runtime.focusedEditableField(tree, command.FieldID)
	if err != nil {
		return err
	}
	command.FieldID = field.ID
	return runtime.applyEditCommandLocked(command)
}

// applyEditCommandLocked is the single mutation/commit path shared by native
// Gio wrappers and headless automation. The caller owns runtime.mu.
func (runtime *Runtime) applyEditCommandLocked(command interaction.EditCommand) error {
	if runtime.editing == nil {
		return fmt.Errorf("field editing is unavailable")
	}
	state, ok := runtime.editing.State(command.FieldID)
	if !ok {
		return fmt.Errorf("unknown field %q", command.FieldID)
	}
	before := runtime.editing.Revision()
	switch command.Kind {
	case interaction.EditClipboardCopy:
		runtime.automationClipboard, _ = runtime.editing.SelectedText(command.FieldID)
	case interaction.EditClipboardCut:
		runtime.automationClipboard, _ = runtime.editing.SelectedText(command.FieldID)
		if _, _, ok := runtime.editing.GraphemeSelection(command.FieldID); ok {
			if err := runtime.editing.ApplyEditCommand(interaction.EditCommand{Kind: interaction.EditReplace, FieldID: command.FieldID, Start: state.SelectionStart, End: state.SelectionEnd}); err != nil {
				return err
			}
		}
	case interaction.EditClipboardPaste:
		if err := runtime.editing.ApplyEditCommand(interaction.EditCommand{Kind: interaction.EditReplace, FieldID: command.FieldID, Start: state.SelectionStart, End: state.SelectionEnd, Text: runtime.automationClipboard}); err != nil {
			return err
		}
	default:
		if err := runtime.editing.ApplyEditCommand(command); err != nil {
			return err
		}
	}
	if runtime.editing.Revision() == before {
		return nil
	}
	if err := runtime.publishValidFieldDraftLocked(command.FieldID); err != nil {
		return err
	}
	runtime.effectiveRoot = nil
	runtime.runtimeRevision++
	runtime.signalLocked()
	return nil
}
