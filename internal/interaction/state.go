package interaction

import (
	"fmt"
	"math"
	"reflect"
	"strings"

	"gora/internal/document"
	"gora/internal/project"
)

// ScopeSpec describes one independently mutable lexical state scope.
type ScopeSpec struct {
	ID      string
	Context string
	State   map[string]document.StateDeclaration
	Initial map[string]any
}

// Transient is the non-persistent interaction state of the selected context.
type Transient struct {
	Hovered      string
	Pressed      string
	Focused      string
	OpenSelect   string
	ActiveOption string
}

type scope struct {
	context      string
	declarations map[string]document.StateDeclaration
	initial      map[string]any
	current      map[string]any
}

// Store owns all document state for a live runtime session.
type Store struct {
	scopes    map[string]*scope
	transient Transient
	revision  uint64
}

func NewStore() *Store {
	return &Store{scopes: make(map[string]*scope)}
}

// Reconcile installs a new set of declarations while retaining compatible values.
func (s *Store) Reconcile(specs []ScopeSpec) {
	s.reconcile("", specs, false)
}

// ReconcileContext replaces one selected context while retaining independent
// values belonging to other screens or fixtures.
func (s *Store) ReconcileContext(context string, specs []ScopeSpec) {
	s.reconcile(context, specs, true)
}

func (s *Store) reconcile(context string, specs []ScopeSpec, preserveOtherContexts bool) {
	next := make(map[string]*scope, len(specs)+len(s.scopes))
	if preserveOtherContexts {
		for id, previous := range s.scopes {
			if previous.context != context {
				next[id] = previous
			}
		}
	}
	for _, spec := range specs {
		initial := make(map[string]any, len(spec.State))
		current := make(map[string]any, len(spec.State))
		for name, declaration := range spec.State {
			value := declaration.Default
			if override, ok := spec.Initial[name]; ok {
				value = override
			}
			value = normalizeForDeclaration(declaration, value)
			initial[name] = value
			current[name] = value

			if previous, ok := s.scopes[spec.ID]; ok {
				oldDeclaration, declared := previous.declarations[name]
				oldValue, present := previous.current[name]
				if declared && present && oldDeclaration.Type == declaration.Type && valueMatches(declaration, oldValue) {
					current[name] = normalizeForDeclaration(declaration, oldValue)
				}
			}
		}
		next[spec.ID] = &scope{
			context:      spec.Context,
			declarations: cloneDeclarations(spec.State),
			initial:      initial,
			current:      current,
		}
	}
	s.scopes = next
	s.transient = Transient{}
	s.revision++
}

// Apply reduces an ordered action list against a working copy and commits once.
func (s *Store) Apply(scopeID string, actions []document.Action) error {
	scope, ok := s.scopes[scopeID]
	if !ok {
		return fmt.Errorf("unknown state scope %q", scopeID)
	}
	working := cloneValues(scope.current)
	for index, action := range actions {
		declaration, ok := scope.declarations[action.State]
		if !ok {
			return fmt.Errorf("action %d targets unknown state %q", index, action.State)
		}
		switch action.Action {
		case "set":
			value, err := resolveValue(action.Value, working, scopeID)
			if err != nil {
				return fmt.Errorf("action %d: %w", index, err)
			}
			value = normalizeForDeclaration(declaration, value)
			if !valueMatches(declaration, value) {
				return fmt.Errorf("action %d value does not match %s state %q", index, declaration.Type, action.State)
			}
			working[action.State] = value
		case "toggle":
			value, ok := working[action.State].(bool)
			if declaration.Type != "boolean" || !ok {
				return fmt.Errorf("action %d toggle requires boolean state %q", index, action.State)
			}
			working[action.State] = !value
		case "increment", "decrement":
			value, ok := numberValue(working[action.State])
			if declaration.Type != "number" || !ok {
				return fmt.Errorf("action %d %s requires number state %q", index, action.Action, action.State)
			}
			by := any(float64(1))
			if action.By != nil {
				by = action.By
			}
			resolved, err := resolveValue(by, working, scopeID)
			if err != nil {
				return fmt.Errorf("action %d: %w", index, err)
			}
			amount, ok := numberValue(resolved)
			if !ok || amount < 0 {
				return fmt.Errorf("action %d by must be a non-negative finite number", index)
			}
			if action.Action == "decrement" {
				amount = -amount
			}
			working[action.State] = normalizeForDeclaration(declaration, value+amount)
		case "reset":
			working[action.State] = scope.initial[action.State]
		default:
			return fmt.Errorf("action %d has unsupported kind %q", index, action.Action)
		}
	}
	if !reflect.DeepEqual(scope.current, working) {
		scope.current = working
		s.revision++
	}
	return nil
}

// ApplyActivation commits all lexical state effects atomically, then returns
// the optional navigation command for the runtime to perform.
func (s *Store) ApplyActivation(scopeID string, actions []document.Action) (*document.Action, error) {
	stateActions := make([]document.Action, 0, len(actions))
	var navigation *document.Action
	for _, action := range actions {
		switch action.Action {
		case "navigate", "replace", "back", "forward":
			command := action
			navigation = &command
		default:
			stateActions = append(stateActions, action)
		}
	}
	if len(stateActions) != 0 {
		if err := s.Apply(scopeID, stateActions); err != nil {
			return nil, err
		}
	}
	return navigation, nil
}

func (s *Store) Values(scopeID string) map[string]any {
	if scope, ok := s.scopes[scopeID]; ok {
		return cloneValues(scope.current)
	}
	return nil
}

// SetValues atomically replaces selected values in one lexical scope.
func (s *Store) SetValues(scopeID string, values map[string]any) error {
	scope, ok := s.scopes[scopeID]
	if !ok {
		return fmt.Errorf("unknown state scope %q", scopeID)
	}
	working := cloneValues(scope.current)
	for name, value := range values {
		declaration, ok := scope.declarations[name]
		if !ok {
			return fmt.Errorf("unknown state %q in scope %q", name, scopeID)
		}
		value = normalizeForDeclaration(declaration, value)
		if !valueMatches(declaration, value) {
			return fmt.Errorf("value does not match %s state %q", declaration.Type, name)
		}
		working[name] = value
	}
	if !reflect.DeepEqual(scope.current, working) {
		scope.current = working
		s.revision++
	}
	return nil
}

// ResetScope restores one lexical scope to its declared initial values.
func (s *Store) ResetScope(scopeID string) error {
	scope, ok := s.scopes[scopeID]
	if !ok {
		return fmt.Errorf("unknown state scope %q", scopeID)
	}
	if !reflect.DeepEqual(scope.current, scope.initial) {
		scope.current = cloneValues(scope.initial)
		s.revision++
	}
	s.transient = Transient{}
	return nil
}

func (s *Store) AllValues() map[string]map[string]any {
	values := make(map[string]map[string]any, len(s.scopes))
	for id, scope := range s.scopes {
		values[id] = cloneValues(scope.current)
	}
	return values
}

func (s *Store) ResetContext(context string) {
	changed := false
	for _, scope := range s.scopes {
		if scope.context == context && !reflect.DeepEqual(scope.current, scope.initial) {
			scope.current = cloneValues(scope.initial)
			changed = true
		}
	}
	s.transient = Transient{}
	if changed {
		s.revision++
	}
}

func (s *Store) SetTransient(value Transient) { s.transient = value }
func (s *Store) Transient() Transient         { return s.transient }
func (s *Store) Revision() uint64             { return s.revision }

func resolveValue(value any, working map[string]any, scopeID string) (any, error) {
	if reference, ok := value.(project.StateReference); ok {
		if reference.Scope != scopeID {
			return nil, fmt.Errorf("cross-scope state reference from %q to %q", scopeID, reference.Scope)
		}
		resolved, exists := working[reference.Name]
		if !exists {
			return nil, fmt.Errorf("unknown state reference %q", reference.Name)
		}
		return resolved, nil
	}
	mapping, ok := value.(map[string]any)
	if !ok || len(mapping) != 1 {
		return value, nil
	}
	ref, ok := mapping["ref"].(string)
	if !ok || !strings.HasPrefix(ref, "state.") {
		return value, nil
	}
	name := strings.TrimPrefix(ref, "state.")
	resolved, ok := working[name]
	if !ok {
		return nil, fmt.Errorf("unknown state reference %q", ref)
	}
	return resolved, nil
}

func valueMatches(declaration document.StateDeclaration, value any) bool {
	switch declaration.Type {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numberValue(value)
		return ok
	case "text":
		_, ok := value.(string)
		return ok
	case "enum":
		text, ok := value.(string)
		if !ok {
			return false
		}
		for _, allowed := range declaration.Values {
			if text == allowed {
				return true
			}
		}
	}
	return false
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

func normalizeValue(value any) any {
	if number, ok := numberValue(value); ok {
		return number
	}
	return value
}

func normalizeForDeclaration(declaration document.StateDeclaration, value any) any {
	value = normalizeValue(value)
	if declaration.Type != "number" {
		return value
	}
	number, ok := numberValue(value)
	if !ok {
		return value
	}
	if declaration.Min != nil && number < *declaration.Min {
		number = *declaration.Min
	}
	if declaration.Max != nil && number > *declaration.Max {
		number = *declaration.Max
	}
	if declaration.Step != nil && *declaration.Step > 0 {
		anchor := 0.0
		if declaration.Min != nil {
			anchor = *declaration.Min
		}
		steps := (number - anchor) / *declaration.Step
		lower := math.Floor(steps)
		fraction := steps - lower
		if fraction >= 0.5-1e-12 {
			lower++
		}
		number = anchor + lower**declaration.Step
		if declaration.Min != nil && number < *declaration.Min {
			number = *declaration.Min
		}
		if declaration.Max != nil && number > *declaration.Max {
			number = *declaration.Max
		}
	}
	return number
}

func cloneValues(values map[string]any) map[string]any {
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneDeclarations(values map[string]document.StateDeclaration) map[string]document.StateDeclaration {
	clone := make(map[string]document.StateDeclaration, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
