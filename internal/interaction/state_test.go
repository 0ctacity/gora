package interaction

import (
	"testing"

	"gora/internal/document"
	"gora/internal/project"
)

func TestStoreAppliesOrderedActionsAtomically(t *testing.T) {
	store := NewStore()
	store.Reconcile([]ScopeSpec{{ID: "screen:main", State: map[string]document.StateDeclaration{
		"count": {Type: "number", Default: int64(2)},
		"on":    {Type: "boolean", Default: false},
	}}})
	err := store.Apply("screen:main", []document.Action{
		{Action: "increment", State: "count", By: int64(3)},
		{Action: "set", State: "count", Value: project.StateReference{Scope: "screen:main", Name: "count"}},
		{Action: "toggle", State: "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := store.Values("screen:main")
	if values["count"] != float64(5) || values["on"] != true {
		t.Fatalf("values = %#v", values)
	}
}

func TestStoreCommitsStateBeforeReturningNavigation(t *testing.T) {
	store := NewStore()
	store.Reconcile([]ScopeSpec{{ID: "screen:main", Context: "main", State: map[string]document.StateDeclaration{
		"count": {Type: "number", Default: float64(1)},
	}}})
	navigation, err := store.ApplyActivation("screen:main", []document.Action{
		{Action: "increment", State: "count", By: float64(2)},
		{Action: "navigate", To: "reports"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.Values("screen:main")["count"] != float64(3) || navigation == nil || navigation.Action != "navigate" || navigation.To != "reports" {
		t.Fatalf("state=%v navigation=%+v", store.Values("screen:main"), navigation)
	}

	navigation, err = store.ApplyActivation("screen:no-state", []document.Action{{Action: "back"}})
	if err != nil || navigation == nil || navigation.Action != "back" {
		t.Fatalf("pure navigation = %+v, %v", navigation, err)
	}
}

func TestStorePreservesOnlyCompatibleReloadValues(t *testing.T) {
	store := NewStore()
	store.Reconcile([]ScopeSpec{{ID: "component:card", State: map[string]document.StateDeclaration{
		"plan": {Type: "enum", Values: []string{"monthly", "annual"}, Default: "monthly"},
	}}})
	if err := store.Apply("component:card", []document.Action{{Action: "set", State: "plan", Value: "annual"}}); err != nil {
		t.Fatal(err)
	}
	store.Reconcile([]ScopeSpec{{ID: "component:card", State: map[string]document.StateDeclaration{
		"plan": {Type: "enum", Values: []string{"monthly"}, Default: "monthly"},
	}}})
	if got := store.Values("component:card")["plan"]; got != "monthly" {
		t.Fatalf("plan = %#v", got)
	}
}

func TestStoreResetContextAndTransientState(t *testing.T) {
	store := NewStore()
	store.Reconcile([]ScopeSpec{
		{ID: "screen:main", Context: "main", State: map[string]document.StateDeclaration{"open": {Type: "boolean", Default: false}}},
		{ID: "screen:main/card", Context: "main", State: map[string]document.StateDeclaration{"open": {Type: "boolean", Default: false}}},
	})
	_ = store.Apply("screen:main", []document.Action{{Action: "toggle", State: "open"}})
	_ = store.Apply("screen:main/card", []document.Action{{Action: "toggle", State: "open"}})
	store.SetTransient(Transient{Hovered: "button", Focused: "button", Pressed: "button"})
	store.ResetContext("main")
	if store.Values("screen:main")["open"] != false || store.Values("screen:main/card")["open"] != false {
		t.Fatal("context state was not reset")
	}
	if store.Transient() != (Transient{}) {
		t.Fatalf("transient = %+v", store.Transient())
	}
}

func TestStorePreservesInactiveFixtureContexts(t *testing.T) {
	store := NewStore()
	declaration := map[string]document.StateDeclaration{"count": {Type: "number", Default: float64(1)}}
	store.ReconcileContext("default", []ScopeSpec{{ID: "fixture:default", Context: "default", State: declaration}})
	if err := store.Apply("fixture:default", []document.Action{{Action: "increment", State: "count", By: float64(2)}}); err != nil {
		t.Fatal(err)
	}
	store.ReconcileContext("annual", []ScopeSpec{{ID: "fixture:annual", Context: "annual", State: declaration}})
	store.ReconcileContext("default", []ScopeSpec{{ID: "fixture:default", Context: "default", State: declaration}})
	if got := store.Values("fixture:default")["count"]; got != float64(3) {
		t.Fatalf("inactive fixture value = %#v", got)
	}
}

func TestStoreNormalizesEveryNumericWriteToDeclaredDomain(t *testing.T) {
	minimum, maximum, step := 0.0, 10.0, 2.0
	store := NewStore()
	store.Reconcile([]ScopeSpec{{ID: "screen:main", State: map[string]document.StateDeclaration{
		"count": {Type: "number", Default: float64(2), Min: &minimum, Max: &maximum, Step: &step},
	}}})

	if err := store.SetValues("screen:main", map[string]any{"count": float64(11)}); err != nil {
		t.Fatal(err)
	}
	if got := store.Values("screen:main")["count"]; got != float64(10) {
		t.Fatalf("clamped set = %#v", got)
	}

	if err := store.Apply("screen:main", []document.Action{{Action: "set", State: "count", Value: float64(5)}}); err != nil {
		t.Fatal(err)
	}
	if got := store.Values("screen:main")["count"]; got != float64(6) {
		t.Fatalf("positive tie = %#v", got)
	}

	if err := store.Apply("screen:main", []document.Action{{Action: "decrement", State: "count", By: float64(20)}}); err != nil {
		t.Fatal(err)
	}
	if got := store.Values("screen:main")["count"]; got != float64(0) {
		t.Fatalf("clamped decrement = %#v", got)
	}
}

func TestStoreNormalizesPreservedValueIntoChangedDomain(t *testing.T) {
	store := NewStore()
	store.Reconcile([]ScopeSpec{{ID: "screen:main", State: map[string]document.StateDeclaration{
		"count": {Type: "number", Default: float64(1)},
	}}})
	if err := store.SetValues("screen:main", map[string]any{"count": float64(9)}); err != nil {
		t.Fatal(err)
	}
	minimum, maximum, step := 0.0, 8.0, 2.0
	store.Reconcile([]ScopeSpec{{ID: "screen:main", State: map[string]document.StateDeclaration{
		"count": {Type: "number", Default: float64(2), Min: &minimum, Max: &maximum, Step: &step},
	}}})
	if got := store.Values("screen:main")["count"]; got != float64(8) {
		t.Fatalf("reconciled value = %#v", got)
	}
}
