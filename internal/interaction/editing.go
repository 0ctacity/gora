package interaction

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"

	"gora/internal/document"
)

const maxFieldUndo = 100

var decimalDraftPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

// FieldSpec is the renderer-neutral contract for one editable field.
type FieldSpec struct {
	ID          string
	Scope       string
	Binding     string
	Type        string
	Multiline   bool
	Value       any
	Declaration document.StateDeclaration
	Disabled    bool
	Required    bool
	MinLength   *int
	MaxLength   *int
	Pattern     string
	HasPattern  bool
	MinLines    *int
	MaxLines    *int
}

// ValidationIssue is a deterministic field validation result.
type ValidationIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EditSnapshot stores the user-visible text and selection for undo/redo.
type EditSnapshot struct {
	Draft          string
	SelectionStart int
	SelectionEnd   int
}

// EditingState owns a field's draft independently from its committed state value.
type EditingState struct {
	Draft              string
	Committed          any
	SelectionStart     int
	SelectionEnd       int
	Composition        string
	CompositionStart   int
	CompositionEnd     int
	Composing          bool
	Focused            bool
	Dirty              bool
	Touched            bool
	Valid              bool
	Validated          bool
	Issues             []ValidationIssue
	InternalOffset     float64
	ManualScroll       bool
	PreferredColumn    int
	HasPreferredColumn bool
	VisualColumns      int
	Undo               []EditSnapshot
	Redo               []EditSnapshot
	compositionBase    *EditSnapshot
	baseline           any
	spec               FieldSpec
}

// EditingStore owns drafts and editing metadata keyed by stable semantic ID.
type EditingStore struct {
	fields   map[string]*EditingState
	revision uint64
}

// FieldSnapshot is the public immutable editing metadata exposed to automation.
// It intentionally excludes undo/redo history and internal layout offsets.
type FieldSnapshot struct {
	ID               string            `json:"id"`
	Draft            string            `json:"draft"`
	Committed        any               `json:"committed,omitempty"`
	SelectionStart   int               `json:"selection_start"`
	SelectionEnd     int               `json:"selection_end"`
	Composition      string            `json:"composition,omitempty"`
	CompositionStart int               `json:"composition_start,omitempty"`
	CompositionEnd   int               `json:"composition_end,omitempty"`
	Composing        bool              `json:"composing"`
	Focused          bool              `json:"focused"`
	Dirty            bool              `json:"dirty"`
	Touched          bool              `json:"touched"`
	Valid            bool              `json:"valid"`
	Validated        bool              `json:"validated"`
	Issues           []ValidationIssue `json:"issues,omitempty"`
}

// EditingStoreSnapshot is an immutable read-only copy of public field
// metadata. The fields map is keyed by semantic ID and contains no editor
// history or implementation-only geometry state.
type EditingStoreSnapshot struct {
	Revision uint64                   `json:"revision"`
	Fields   map[string]FieldSnapshot `json:"fields"`
}

func NewEditingStore() *EditingStore {
	return &EditingStore{fields: make(map[string]*EditingState)}
}

// Reconcile retains compatible drafts across reloads and drops removed fields.
func (s *EditingStore) Reconcile(specs []FieldSpec) {
	next := make(map[string]*EditingState, len(specs))
	for _, spec := range specs {
		if previous := s.fields[spec.ID]; previous != nil && compatibleField(previous.spec, spec) {
			manualScroll, internalOffset := previous.ManualScroll, previous.InternalOffset
			previous.spec = spec
			previous.SelectionStart = clampGraphemeIndex(previous.Draft, previous.SelectionStart)
			previous.SelectionEnd = clampGraphemeIndex(previous.Draft, previous.SelectionEnd)
			previous.refreshValidation()
			if manualScroll {
				previous.ManualScroll = true
				previous.InternalOffset = internalOffset
				previous.clampInternalOffset()
			}
			next[spec.ID] = previous
			continue
		}
		draft := fieldText(spec.Value)
		state := &EditingState{
			Draft:          draft,
			Committed:      spec.Value,
			SelectionStart: graphemeCount(draft),
			SelectionEnd:   graphemeCount(draft),
			baseline:       spec.Value,
			spec:           spec,
		}
		state.refreshValidation()
		next[spec.ID] = state
	}
	s.fields = next
	s.revision++
}

func compatibleField(left, right FieldSpec) bool {
	return left.ID == right.ID && left.Scope == right.Scope && left.Binding == right.Binding && left.Type == right.Type && left.Multiline == right.Multiline
}

func (s *EditingStore) State(id string) (EditingState, bool) {
	state, ok := s.fields[id]
	if !ok {
		return EditingState{}, false
	}
	copy := *state
	copy.Issues = append([]ValidationIssue(nil), state.Issues...)
	copy.Undo = append([]EditSnapshot(nil), state.Undo...)
	copy.Redo = append([]EditSnapshot(nil), state.Redo...)
	return copy, true
}

func (s *EditingStore) States() map[string]EditingState {
	result := make(map[string]EditingState, len(s.fields))
	for id := range s.fields {
		result[id], _ = s.State(id)
	}
	return result
}

// Snapshot returns a deep copy of the complete visible editing store.
func (s *EditingStore) Snapshot() EditingStoreSnapshot {
	if s == nil {
		return EditingStoreSnapshot{Fields: map[string]FieldSnapshot{}}
	}
	fields := make(map[string]FieldSnapshot, len(s.fields))
	for id, state := range s.fields {
		fields[id] = FieldSnapshot{
			ID:               id,
			Draft:            state.Draft,
			Committed:        state.Committed,
			SelectionStart:   state.SelectionStart,
			SelectionEnd:     state.SelectionEnd,
			Composition:      state.Composition,
			CompositionStart: state.CompositionStart,
			CompositionEnd:   state.CompositionEnd,
			Composing:        state.Composing,
			Focused:          state.Focused,
			Dirty:            state.Dirty,
			Touched:          state.Touched,
			Valid:            state.Valid,
			Validated:        state.Validated,
			Issues:           append([]ValidationIssue(nil), state.Issues...),
		}
	}
	return EditingStoreSnapshot{Revision: s.revision, Fields: fields}
}

// SyncCommitted replaces drafts whose underlying lexical state changed outside
// the field editor (actions, resets, selection changes, or MCP writes).
func (s *EditingStore) SyncCommitted(values map[string]map[string]any) {
	for id, state := range s.fields {
		value := values[state.spec.Scope][state.spec.Binding]
		if !reflect.DeepEqual(value, state.Committed) {
			_ = s.ReplaceCommitted(id, value)
		}
	}
}

func (s *EditingStore) SetDraft(id, draft string) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	draft = normalizeDraftInput(state.spec, draft)
	if state.Draft == draft {
		return nil
	}
	if !state.Composing {
		state.pushUndo()
	}
	state.Draft = draft
	state.SelectionStart = graphemeCount(draft)
	state.SelectionEnd = state.SelectionStart
	state.Composition = ""
	state.CompositionStart = 0
	state.CompositionEnd = 0
	state.Composing = false
	state.compositionBase = nil
	state.HasPreferredColumn = false
	state.Redo = nil
	state.refreshValidation()
	s.revision++
	return nil
}

// ReplaceCommitted applies an external state write and resets the local draft.
func (s *EditingStore) ReplaceCommitted(id string, value any) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	state.Committed = value
	state.baseline = value
	state.Draft = fieldText(value)
	state.SelectionStart = graphemeCount(state.Draft)
	state.SelectionEnd = state.SelectionStart
	clearCompositionState(state)
	state.HasPreferredColumn = false
	state.Dirty = false
	state.Undo = nil
	state.Redo = nil
	state.refreshValidation()
	s.revision++
	return nil
}

// Commit validates and converts a draft without mutating document state itself.
func (s *EditingStore) Commit(id string) (any, error) {
	state, err := s.field(id)
	if err != nil {
		return nil, err
	}
	state.Touched = true
	finishCompositionState(state)
	state.HasPreferredColumn = false
	state.refreshValidation()
	if !state.Valid {
		return nil, fmt.Errorf("field %q is invalid", id)
	}
	value := any(state.Draft)
	if state.spec.Type == "number" {
		if !decimalDraftPattern.MatchString(state.Draft) {
			return nil, fmt.Errorf("field %q is not a decimal number", id)
		}
		number, parseErr := strconv.ParseFloat(state.Draft, 64)
		if parseErr != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("field %q is not a finite decimal number", id)
		}
		value = normalizeForDeclaration(state.spec.Declaration, number)
	}
	state.Committed = value
	state.baseline = value
	state.Draft = fieldText(value)
	state.SelectionStart = clampGraphemeIndex(state.Draft, state.SelectionStart)
	state.SelectionEnd = clampGraphemeIndex(state.Draft, state.SelectionEnd)
	state.Dirty = false
	state.refreshValidation()
	s.revision++
	return value, nil
}

// PrepareCommit validates and converts a draft without changing its committed value.
func (s *EditingStore) PrepareCommit(id string) (any, error) {
	state, err := s.field(id)
	if err != nil {
		return nil, err
	}
	state.Touched = true
	finishCompositionState(state)
	state.refreshValidation()
	s.revision++
	if !state.Valid {
		return nil, fmt.Errorf("field %q is invalid", id)
	}
	if state.spec.Type != "number" {
		return state.Draft, nil
	}
	if !decimalDraftPattern.MatchString(state.Draft) {
		return nil, fmt.Errorf("field %q is not a decimal number", id)
	}
	number, parseErr := strconv.ParseFloat(state.Draft, 64)
	if parseErr != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, fmt.Errorf("field %q is not a finite decimal number", id)
	}
	return normalizeForDeclaration(state.spec.Declaration, number), nil
}

func (s *EditingStore) AcceptCommitted(id string, value any) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	state.Committed = value
	state.baseline = value
	state.Draft = fieldText(value)
	state.Dirty = false
	clearCompositionState(state)
	state.SelectionStart = clampGraphemeIndex(state.Draft, state.SelectionStart)
	state.SelectionEnd = clampGraphemeIndex(state.Draft, state.SelectionEnd)
	state.refreshValidation()
	s.revision++
	return nil
}

// PublishableValue returns the current valid typed draft without changing
// editing state. Active IME composition is published only after it finishes.
func (s *EditingStore) PublishableValue(id string) (FieldSpec, any, bool) {
	state := s.fields[id]
	if state == nil || state.Composing || !state.Validated || !state.Valid {
		return FieldSpec{}, nil, false
	}
	if state.spec.Type != "number" {
		return state.spec, state.Draft, true
	}
	if !decimalDraftPattern.MatchString(state.Draft) {
		return state.spec, nil, false
	}
	number, err := strconv.ParseFloat(state.Draft, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return state.spec, nil, false
	}
	return state.spec, normalizeForDeclaration(state.spec.Declaration, number), true
}

// AcceptPublished records a value written through a valid draft without
// replacing the draft, selection, composition, or undo history.
func (s *EditingStore) AcceptPublished(id string, value any) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(state.Committed, value) {
		return nil
	}
	state.Committed = value
	state.Dirty = state.Draft != fieldText(state.baseline)
	s.revision++
	return nil
}

func (s *EditingStore) Reset(ids []string, values map[string]map[string]any) {
	for _, id := range ids {
		state := s.fields[id]
		if state == nil {
			continue
		}
		_ = s.ReplaceCommitted(id, values[state.spec.Scope][state.spec.Binding])
		state.Touched = false
	}
}

func (s *EditingStore) field(id string) (*EditingState, error) {
	state := s.fields[id]
	if state == nil {
		return nil, fmt.Errorf("unknown field %q", id)
	}
	return state, nil
}

func (s *EditingStore) Undo(id string) bool {
	state := s.fields[id]
	if state == nil || len(state.Undo) == 0 {
		return false
	}
	state.Redo = append(state.Redo, state.snapshot())
	last := len(state.Undo) - 1
	state.restore(state.Undo[last])
	state.Undo = state.Undo[:last]
	state.refreshValidation()
	s.revision++
	return true
}

func (s *EditingStore) Redo(id string) bool {
	state := s.fields[id]
	if state == nil || len(state.Redo) == 0 {
		return false
	}
	if !state.Composing {
		state.pushUndo()
	}
	last := len(state.Redo) - 1
	state.restore(state.Redo[last])
	state.Redo = state.Redo[:last]
	state.refreshValidation()
	s.revision++
	return true
}

func (s *EditingStore) Select(id string, start, end int) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	state.SelectionStart = clampGraphemeIndex(state.Draft, start)
	state.SelectionEnd = clampGraphemeIndex(state.Draft, end)
	state.ManualScroll = false
	state.revealCaretLine()
	s.revision++
	return nil
}

// ApplyRuneEdit applies a Gio IME edit range while retaining grapheme-indexed
// selection internally.
func (s *EditingStore) ApplyRuneEdit(id string, start, end int, replacement string) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	replacement = normalizeDraftInput(state.spec, replacement)
	runes := []rune(state.Draft)
	start = max(0, min(start, len(runes)))
	end = max(0, min(end, len(runes)))
	if start > end {
		start, end = end, start
	}
	if start == end && replacement == "" && !state.Composing {
		return nil
	}
	if !state.Composing {
		state.pushUndo()
	}
	runes = append(append(append([]rune(nil), runes[:start]...), []rune(replacement)...), runes[end:]...)
	state.Draft = string(runes)
	caretRunes := start + len([]rune(replacement))
	state.SelectionStart = graphemeCount(string(runes[:caretRunes]))
	state.SelectionEnd = state.SelectionStart
	state.Redo = nil
	if state.Composing {
		state.CompositionStart = graphemeCount(string(runes[:start]))
		state.CompositionEnd = state.SelectionEnd
		state.Composition = string(runes[start:caretRunes])
	}
	state.HasPreferredColumn = false
	state.refreshValidation()
	s.revision++
	return nil
}

func normalizeDraftInput(spec FieldSpec, value string) string {
	if spec.Multiline {
		return value
	}
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}

// ValidateCommitted applies field-shape and validation rules before an
// external semantic/MCP value replaces the draft.
func (s *EditingStore) ValidateCommitted(id string, value any) (any, error) {
	state, err := s.field(id)
	if err != nil {
		return nil, err
	}
	if text, ok := value.(string); ok {
		value = normalizeDraftInput(state.spec, text)
	}
	issues := validateFieldDraft(state.spec, fieldText(value))
	if len(issues) != 0 {
		return nil, fmt.Errorf("field %q value is invalid: %s", id, issues[0].Message)
	}
	return value, nil
}

func (s *EditingStore) SetRuneSelection(id string, start, end int) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	runes := []rune(state.Draft)
	start = max(0, min(start, len(runes)))
	end = max(0, min(end, len(runes)))
	nextStart := graphemeCount(string(runes[:start]))
	nextEnd := graphemeCount(string(runes[:end]))
	if state.SelectionStart == nextStart && state.SelectionEnd == nextEnd {
		if state.ManualScroll {
			state.ManualScroll = false
			state.revealCaretLine()
			s.revision++
		}
		return nil
	}
	state.SelectionStart = nextStart
	state.SelectionEnd = nextEnd
	state.HasPreferredColumn = false
	state.ManualScroll = false
	state.revealCaretLine()
	s.revision++
	return nil
}

// MoveSelection applies conventional grapheme, word, line, and document
// movement while retaining SelectionStart as the extension anchor.
func (s *EditingStore) MoveSelection(id, movement string, extend bool) bool {
	state := s.fields[id]
	if state == nil {
		return false
	}
	runes := []rune(state.Draft)
	anchor := runeOffsetForGrapheme(state.Draft, state.SelectionStart)
	caret := runeOffsetForGrapheme(state.Draft, state.SelectionEnd)
	if !extend && anchor != caret {
		switch movement {
		case "grapheme-left", "word-left", "line-start", "document-start", "line-up":
			caret = min(anchor, caret)
		default:
			caret = max(anchor, caret)
		}
	} else if movement == "line-up" || movement == "line-down" {
		if !state.HasPreferredColumn {
			if state.spec.Multiline && state.VisualColumns > 0 {
				state.PreferredColumn = visualFieldPosition(state, runes, caret).column
			} else {
				state.PreferredColumn = caret - movedRuneOffset(runes, caret, "line-start")
			}
			state.HasPreferredColumn = true
		}
		caret = movedFieldRuneOffset(state, runes, caret, movement, state.PreferredColumn)
	} else {
		state.HasPreferredColumn = false
		caret = movedFieldRuneOffset(state, runes, caret, movement, 0)
	}
	nextStart := caret
	if extend {
		nextStart = anchor
	}
	keepPreferred := movement == "line-up" || movement == "line-down"
	preferredColumn := state.PreferredColumn
	before := s.revision
	_ = s.SetRuneSelection(id, nextStart, caret)
	if keepPreferred {
		state.PreferredColumn = preferredColumn
		state.HasPreferredColumn = true
	}
	return s.revision != before
}

// SetVisualColumns supplies the current wrapped text viewport width. It is a
// renderer metric, not authored state, but it affects visual-line movement.
func (s *EditingStore) SetVisualColumns(id string, columns int) bool {
	state := s.fields[id]
	if state == nil {
		return false
	}
	columns = max(0, columns)
	if state.VisualColumns == columns {
		return false
	}
	state.VisualColumns = columns
	state.HasPreferredColumn = false
	if state.ManualScroll {
		state.clampInternalOffset()
	} else {
		state.revealCaretLine()
	}
	s.revision++
	return true
}

// ScrollInternal moves a multiline field's own logical line viewport. It
// returns false at a boundary so an input adapter may pass residual scrolling
// to an ancestor document scrollport.
func (s *EditingStore) ScrollInternal(id string, lines int) bool {
	state := s.fields[id]
	if state == nil || !state.spec.Multiline || state.spec.MaxLines == nil || *state.spec.MaxLines <= 0 || lines == 0 {
		return false
	}
	layout := visualFieldLayout(state, []rune(state.Draft))
	maximum := max(0, len(layout.widths)-*state.spec.MaxLines)
	current := min(max(0, int(state.InternalOffset)), maximum)
	next := min(max(0, current+lines), maximum)
	if next == current {
		return false
	}
	state.InternalOffset = float64(next)
	state.ManualScroll = true
	s.revision++
	return true
}

// DeleteSelection removes the current selection or the adjacent
// grapheme/word. It returns false when the requested deletion is a no-op.
func (s *EditingStore) DeleteSelection(id string, backward, word bool) bool {
	state := s.fields[id]
	if state == nil {
		return false
	}
	start := runeOffsetForGrapheme(state.Draft, state.SelectionStart)
	end := runeOffsetForGrapheme(state.Draft, state.SelectionEnd)
	if start > end {
		start, end = end, start
	}
	if start == end {
		movement := "grapheme-right"
		if backward {
			movement = "grapheme-left"
		}
		if word {
			if backward {
				movement = "word-left"
			} else {
				movement = "word-right"
			}
		}
		adjacent := movedRuneOffset([]rune(state.Draft), start, movement)
		start, end = min(start, adjacent), max(end, adjacent)
	}
	if start == end {
		return false
	}
	return s.ApplyRuneEdit(id, start, end, "") == nil
}

func (s *EditingStore) Touch(id string) bool {
	state := s.fields[id]
	if state == nil {
		return false
	}
	changed := !state.Touched || state.Composing || state.Composition != ""
	if !changed {
		return false
	}
	state.Touched = true
	finishCompositionState(state)
	state.HasPreferredColumn = false
	s.revision++
	return true
}

func movedRuneOffset(runes []rune, caret int, movement string) int {
	caret = max(0, min(caret, len(runes)))
	switch movement {
	case "grapheme-left":
		return runeOffsetForGrapheme(string(runes), graphemeCount(string(runes[:caret]))-1)
	case "grapheme-right":
		return runeOffsetForGrapheme(string(runes), graphemeCount(string(runes[:caret]))+1)
	case "word-left":
		for caret > 0 && !isWordRune(runes[caret-1]) {
			caret--
		}
		for caret > 0 && isWordRune(runes[caret-1]) {
			caret--
		}
		return caret
	case "word-right":
		if caret < len(runes) && isWordRune(runes[caret]) {
			for caret < len(runes) && isWordRune(runes[caret]) {
				caret++
			}
		}
		for caret < len(runes) && !isWordRune(runes[caret]) {
			caret++
		}
		return caret
	case "line-start":
		for caret > 0 && runes[caret-1] != '\n' {
			caret--
		}
		return caret
	case "line-end":
		for caret < len(runes) && runes[caret] != '\n' {
			caret++
		}
		return caret
	case "document-start":
		return 0
	case "document-end":
		return len(runes)
	case "line-up", "line-down":
		column := caret - movedRuneOffset(runes, caret, "line-start")
		return movedLineRuneOffset(runes, caret, movement, column)
	}
	return caret
}

func movedLineRuneOffset(runes []rune, caret int, movement string, column int) int {
	start := movedRuneOffset(runes, caret, "line-start")
	if movement == "line-up" {
		if start == 0 {
			return caret
		}
		previousEnd := start - 1
		previousStart := movedRuneOffset(runes, previousEnd, "line-start")
		return min(previousStart+column, previousEnd)
	}
	end := movedRuneOffset(runes, caret, "line-end")
	if end == len(runes) {
		return caret
	}
	nextStart := end + 1
	nextEnd := movedRuneOffset(runes, nextStart, "line-end")
	return min(nextStart+column, nextEnd)
}

type editingVisualPosition struct {
	line   int
	column int
}

type editingVisualLayout struct {
	positions []editingVisualPosition
	widths    []int
	starts    []int
	ends      []int
}

func visualFieldLayout(state *EditingState, runes []rune) editingVisualLayout {
	positions := make([]editingVisualPosition, len(runes)+1)
	widths := []int{0}
	starts := []int{0}
	ends := []int{0}
	line, column := 0, 0
	for index, value := range runes {
		if state.spec.Multiline && value != '\n' && state.VisualColumns > 0 && column >= state.VisualColumns {
			line++
			column = 0
			widths = append(widths, 0)
			starts = append(starts, index)
			ends = append(ends, index)
		}
		positions[index] = editingVisualPosition{line: line, column: column}
		if value == '\n' && state.spec.Multiline {
			ends[line] = index
			line++
			column = 0
			widths = append(widths, 0)
			starts = append(starts, index+1)
			ends = append(ends, index+1)
			continue
		}
		column++
		widths[line] = max(widths[line], column)
		ends[line] = index + 1
	}
	positions[len(runes)] = editingVisualPosition{line: line, column: column}
	return editingVisualLayout{positions: positions, widths: widths, starts: starts, ends: ends}
}

func visualFieldPosition(state *EditingState, runes []rune, caret int) editingVisualPosition {
	layout := visualFieldLayout(state, runes)
	caret = max(0, min(caret, len(layout.positions)-1))
	return layout.positions[caret]
}

func movedFieldRuneOffset(state *EditingState, runes []rune, caret int, movement string, preferredColumn int) int {
	visualMovement := movement == "line-start" || movement == "line-end" || movement == "line-up" || movement == "line-down"
	if !state.spec.Multiline || state.VisualColumns <= 0 || !visualMovement {
		if movement == "line-up" || movement == "line-down" {
			return movedLineRuneOffset(runes, caret, movement, preferredColumn)
		}
		return movedRuneOffset(runes, caret, movement)
	}
	layout := visualFieldLayout(state, runes)
	caret = max(0, min(caret, len(layout.positions)-1))
	current := layout.positions[caret]
	targetLine := current.line
	switch movement {
	case "line-up":
		targetLine--
	case "line-down":
		targetLine++
	case "line-start":
		return layout.starts[targetLine]
	case "line-end":
		return layout.ends[targetLine]
	}
	if targetLine < 0 {
		return caret
	}
	best, distance := caret, int(^uint(0)>>1)
	found := false
	if targetLine >= len(layout.widths) {
		return caret
	}
	if preferredColumn >= layout.widths[targetLine] {
		return layout.ends[targetLine]
	}
	for index, position := range layout.positions {
		if position.line != targetLine {
			continue
		}
		found = true
		candidate := editingAbsInt(position.column - preferredColumn)
		if candidate < distance || candidate == distance && index > best {
			best, distance = index, candidate
		}
	}
	if !found {
		return caret
	}
	return best
}

func editingAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func isWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_'
}

func (s *EditingStore) SetComposition(id string, start, end int) error {
	state, err := s.field(id)
	if err != nil {
		return err
	}
	nextComposing := start != end
	if !nextComposing && !state.Composing && state.Composition == "" {
		return nil
	}
	if nextComposing {
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
	} else {
		finishCompositionState(state)
	}
	s.revision++
	return nil
}

func finishCompositionState(state *EditingState) {
	if state == nil {
		return
	}
	if state.compositionBase != nil && state.Draft != state.compositionBase.Draft {
		state.Undo = append(state.Undo, *state.compositionBase)
		if len(state.Undo) > maxFieldUndo {
			state.Undo = append([]EditSnapshot(nil), state.Undo[len(state.Undo)-maxFieldUndo:]...)
		}
		state.Redo = nil
	}
	clearCompositionState(state)
}

func clearCompositionState(state *EditingState) {
	if state == nil {
		return
	}
	state.Composing = false
	state.Composition = ""
	state.CompositionStart = 0
	state.CompositionEnd = 0
	state.compositionBase = nil
}

// CancelComposition restores the draft and selection from composition start.
func (s *EditingStore) CancelComposition(id string) bool {
	state := s.fields[id]
	if state == nil || !state.Composing {
		return false
	}
	if state.compositionBase != nil {
		state.restore(*state.compositionBase)
	}
	clearCompositionState(state)
	state.refreshValidation()
	s.revision++
	return true
}

func (s *EditingStore) RuneSelection(id string) (int, int, bool) {
	state := s.fields[id]
	if state == nil {
		return 0, 0, false
	}
	return runeOffsetForGrapheme(state.Draft, state.SelectionStart), runeOffsetForGrapheme(state.Draft, state.SelectionEnd), true
}

func (s *EditingStore) Draft(id string) (string, bool) {
	state := s.fields[id]
	if state == nil {
		return "", false
	}
	return state.Draft, true
}

func (s *EditingStore) SelectedText(id string) (string, bool) {
	state := s.fields[id]
	if state == nil {
		return "", false
	}
	start := runeOffsetForGrapheme(state.Draft, state.SelectionStart)
	end := runeOffsetForGrapheme(state.Draft, state.SelectionEnd)
	if start > end {
		start, end = end, start
	}
	runes := []rune(state.Draft)
	return string(runes[start:end]), true
}

func (s *EditingStore) Revision() uint64 { return s.revision }

func (state *EditingState) snapshot() EditSnapshot {
	return EditSnapshot{Draft: state.Draft, SelectionStart: state.SelectionStart, SelectionEnd: state.SelectionEnd}
}

func (state *EditingState) restore(snapshot EditSnapshot) {
	state.Draft = snapshot.Draft
	state.SelectionStart = clampGraphemeIndex(state.Draft, snapshot.SelectionStart)
	state.SelectionEnd = clampGraphemeIndex(state.Draft, snapshot.SelectionEnd)
	clearCompositionState(state)
}

func (state *EditingState) pushUndo() {
	state.Undo = append(state.Undo, state.snapshot())
	if len(state.Undo) > maxFieldUndo {
		copy(state.Undo, state.Undo[len(state.Undo)-maxFieldUndo:])
		state.Undo = state.Undo[:maxFieldUndo]
	}
}

func (state *EditingState) refreshValidation() {
	state.Validated = !state.spec.Disabled
	if state.Validated {
		state.Issues = validateFieldDraft(state.spec, state.Draft)
		state.Valid = len(state.Issues) == 0
	} else {
		state.Issues = nil
		state.Valid = false
	}
	state.Dirty = state.Draft != fieldText(state.baseline)
	state.ManualScroll = false
	state.revealCaretLine()
}

func (state *EditingState) clampInternalOffset() {
	if state.spec.MaxLines == nil || *state.spec.MaxLines <= 0 {
		state.InternalOffset = 0
		state.ManualScroll = false
		return
	}
	layout := visualFieldLayout(state, []rune(state.Draft))
	maximum := max(0, len(layout.widths)-*state.spec.MaxLines)
	state.InternalOffset = float64(min(max(0, int(state.InternalOffset)), maximum))
}

func (state *EditingState) revealCaretLine() {
	if state.spec.MaxLines == nil || *state.spec.MaxLines <= 0 {
		state.InternalOffset = 0
		return
	}
	runeOffset := runeOffsetForGrapheme(state.Draft, state.SelectionEnd)
	runes := []rune(state.Draft)
	line := 0
	for _, r := range runes[:min(runeOffset, len(runes))] {
		if r == '\n' {
			line++
		}
	}
	maximumVisible := *state.spec.MaxLines
	if line >= int(state.InternalOffset)+maximumVisible {
		state.InternalOffset = float64(line - maximumVisible + 1)
	}
	if line < int(state.InternalOffset) {
		state.InternalOffset = float64(line)
	}
}

func validateFieldDraft(spec FieldSpec, draft string) []ValidationIssue {
	var issues []ValidationIssue
	count := graphemeCount(draft)
	if spec.Required && count == 0 {
		issues = append(issues, ValidationIssue{Code: "required", Message: "A value is required."})
	}
	if spec.MinLength != nil && count < *spec.MinLength {
		issues = append(issues, ValidationIssue{Code: "min_length", Message: fmt.Sprintf("Use at least %d characters.", *spec.MinLength)})
	}
	if spec.MaxLength != nil && count > *spec.MaxLength {
		issues = append(issues, ValidationIssue{Code: "max_length", Message: fmt.Sprintf("Use at most %d characters.", *spec.MaxLength)})
	}
	if spec.HasPattern {
		if expression, err := regexp.Compile("^(?:" + spec.Pattern + ")$"); err == nil && !expression.MatchString(draft) {
			issues = append(issues, ValidationIssue{Code: "pattern", Message: "The value does not match the required format."})
		}
	}
	if spec.Type == "number" {
		value, err := strconv.ParseFloat(draft, 64)
		if !decimalDraftPattern.MatchString(draft) || err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			issues = append(issues, ValidationIssue{Code: "number", Message: "Enter a finite decimal number."})
		}
	}
	return issues
}

func fieldText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func clampGraphemeIndex(text string, index int) int {
	if index < 0 {
		return 0
	}
	if count := graphemeCount(text); index > count {
		return count
	}
	return index
}

func runeOffsetForGrapheme(text string, index int) int {
	offsets := graphemeRuneOffsets(text)
	if index <= 0 {
		return 0
	}
	if index >= len(offsets)-1 {
		return len([]rune(text))
	}
	return offsets[index]
}

func graphemeRuneOffsets(text string) []int {
	offsets := []int{0}
	runeOffset := 0
	clusters := uniseg.NewGraphemes(text)
	for clusters.Next() {
		runeOffset += len(clusters.Runes())
		offsets = append(offsets, runeOffset)
	}
	return offsets
}

// graphemeCount returns Unicode extended grapheme clusters (user-perceived
// characters), never bytes or runes.
func graphemeCount(text string) int {
	return len(graphemeRuneOffsets(text)) - 1
}
