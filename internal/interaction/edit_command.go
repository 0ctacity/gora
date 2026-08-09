package interaction

import "fmt"

// EditCommandKind identifies one renderer-neutral editing operation. The
// command surface is shared by native Gio input and headless automation.
type EditCommandKind string

const (
	EditReplace           EditCommandKind = "replace"
	EditSelection         EditCommandKind = "selection"
	EditCompositionStart  EditCommandKind = "composition_start"
	EditCompositionUpdate EditCommandKind = "composition_update"
	EditCompositionCommit EditCommandKind = "composition_commit"
	EditCompositionCancel EditCommandKind = "composition_cancel"
	EditClipboardCopy     EditCommandKind = "clipboard_copy"
	EditClipboardCut      EditCommandKind = "clipboard_cut"
	EditClipboardPaste    EditCommandKind = "clipboard_paste"
	EditUndo              EditCommandKind = "undo"
	EditRedo              EditCommandKind = "redo"
)

// EditCommand is deliberately independent of Gio, MCP, and rendering. Start
// and End are grapheme indexes into the draft visible immediately before the
// command. A caller validates a complete batch before delivery; this type
// validates each command against the current draft as it is applied.
type EditCommand struct {
	Kind    EditCommandKind
	FieldID string
	Start   int
	End     int
	Text    string
}

// GraphemeIndexAtRune converts a Gio rune offset to the nearest grapheme
// boundary used by renderer-neutral edit commands.
func GraphemeIndexAtRune(draft string, runeIndex int) int {
	runes := []rune(draft)
	runeIndex = max(0, min(runeIndex, len(runes)))
	return graphemeCount(string(runes[:runeIndex]))
}

// Clone returns a private editing-store copy suitable for transactional
// preflight. Internal composition bases and bounded history are copied too,
// so simulation uses the exact command semantics rather than a second model.
func (s *EditingStore) Clone() *EditingStore {
	clone := NewEditingStore()
	if s == nil {
		return clone
	}
	clone.revision = s.revision
	for id, state := range s.fields {
		copy := *state
		copy.Issues = append([]ValidationIssue(nil), state.Issues...)
		copy.Undo = append([]EditSnapshot(nil), state.Undo...)
		copy.Redo = append([]EditSnapshot(nil), state.Redo...)
		if state.compositionBase != nil {
			base := *state.compositionBase
			copy.compositionBase = &base
		}
		clone.fields[id] = &copy
	}
	return clone
}

// GraphemeSelection exposes the current selection in grapheme indexes.
func (s *EditingStore) GraphemeSelection(id string) (int, int, bool) {
	state := s.fields[id]
	if state == nil {
		return 0, 0, false
	}
	return state.SelectionStart, state.SelectionEnd, true
}

// HistoryDepth reports bounded undo/redo counts without exposing snapshots.
func (s *EditingStore) HistoryDepth(id string) (int, int, bool) {
	state := s.fields[id]
	if state == nil {
		return 0, 0, false
	}
	return len(state.Undo), len(state.Redo), true
}

func validateGraphemeRange(draft string, start, end int) error {
	if start < 0 || end < 0 || start > end || end > graphemeCount(draft) {
		return fmt.Errorf("grapheme range [%d,%d) is outside draft length %d", start, end, graphemeCount(draft))
	}
	return nil
}

func validateSelectionRange(draft string, start, end int) error {
	length := graphemeCount(draft)
	if start < 0 || end < 0 || start > length || end > length {
		return fmt.Errorf("grapheme selection [%d,%d) is outside draft length %d", start, end, length)
	}
	return nil
}

func (s *EditingStore) ApplyEditCommand(command EditCommand) error {
	state, err := s.field(command.FieldID)
	if err != nil {
		return err
	}
	switch command.Kind {
	case EditReplace:
		if err := validateGraphemeRange(state.Draft, command.Start, command.End); err != nil {
			return err
		}
		start := runeOffsetForGrapheme(state.Draft, command.Start)
		end := runeOffsetForGrapheme(state.Draft, command.End)
		return s.ApplyRuneEdit(command.FieldID, start, end, command.Text)
	case EditSelection:
		if err := validateSelectionRange(state.Draft, command.Start, command.End); err != nil {
			return err
		}
		start := runeOffsetForGrapheme(state.Draft, command.Start)
		end := runeOffsetForGrapheme(state.Draft, command.End)
		return s.SetRuneSelection(command.FieldID, start, end)
	case EditCompositionStart:
		if err := validateGraphemeRange(state.Draft, command.Start, command.End); err != nil {
			return err
		}
		start := runeOffsetForGrapheme(state.Draft, command.Start)
		end := runeOffsetForGrapheme(state.Draft, command.End)
		return s.StartComposition(command.FieldID, start, end)
	case EditCompositionUpdate:
		if err := validateGraphemeRange(state.Draft, command.Start, command.End); err != nil {
			return err
		}
		start := runeOffsetForGrapheme(state.Draft, command.Start)
		end := runeOffsetForGrapheme(state.Draft, command.End)
		if !state.Composing {
			if err := s.StartComposition(command.FieldID, start, end); err != nil {
				return err
			}
		}
		if err := s.ApplyRuneEdit(command.FieldID, start, end, command.Text); err != nil {
			return err
		}
		return nil
	case EditCompositionCommit:
		return s.SetComposition(command.FieldID, 0, 0)
	case EditCompositionCancel:
		s.CancelComposition(command.FieldID)
		return nil
	case EditUndo:
		if !s.Undo(command.FieldID) {
			return nil
		}
		return nil
	case EditRedo:
		if !s.Redo(command.FieldID) {
			return nil
		}
		return nil
	default:
		return fmt.Errorf("unsupported edit command %q", command.Kind)
	}
}

// StartComposition begins an IME transaction even when the initial range is
// empty (a caret composition). Gio's legacy SetComposition keeps its existing
// empty-range commit behavior; automation commands use this explicit method.
func (s *EditingStore) StartComposition(id string, start, end int) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	runes := []rune(state.Draft)
	start = max(0, min(start, len(runes)))
	end = max(0, min(end, len(runes)))
	if start > end {
		start, end = end, start
	}
	if !state.Composing {
		base := state.snapshot()
		state.compositionBase = &base
	}
	state.Composing = true
	state.CompositionStart = graphemeCount(string(runes[:start]))
	state.CompositionEnd = graphemeCount(string(runes[:end]))
	state.Composition = string(runes[start:end])
	s.revision++
	return nil
}

// ValidateEditCommandsWithClipboard runs the exact command path on an
// isolated clone, including evolving clipboard, selection, composition, and
// undo/redo state. The receiver is never mutated.
func (s *EditingStore) ValidateEditCommandsWithClipboard(commands []EditCommand, clipboard string) error {
	simulation := s.Clone()
	for index, command := range commands {
		if command.Kind == EditClipboardCopy || command.Kind == EditClipboardCut {
			selected, ok := simulation.SelectedText(command.FieldID)
			if !ok {
				return fmt.Errorf("edit %d: unknown field %q", index, command.FieldID)
			}
			clipboard = selected
			if command.Kind == EditClipboardCut {
				start, end, _ := simulation.GraphemeSelection(command.FieldID)
				command = EditCommand{Kind: EditReplace, FieldID: command.FieldID, Start: start, End: end}
			} else {
				continue
			}
		}
		if command.Kind == EditClipboardPaste {
			start, end, ok := simulation.GraphemeSelection(command.FieldID)
			if !ok {
				return fmt.Errorf("edit %d: unknown field %q", index, command.FieldID)
			}
			command = EditCommand{Kind: EditReplace, FieldID: command.FieldID, Start: start, End: end, Text: clipboard}
		}
		if err := simulation.ApplyEditCommand(command); err != nil {
			return fmt.Errorf("edit %d: %w", index, err)
		}
	}
	return nil
}

// ValidateEditCommands checks a batch against a simulated evolving draft,
// without mutating the store. This is used by automation before delivery.
func (s *EditingStore) ValidateEditCommands(commands []EditCommand) error {
	return s.ValidateEditCommandsWithClipboard(commands, "")
}
