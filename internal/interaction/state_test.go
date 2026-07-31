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
