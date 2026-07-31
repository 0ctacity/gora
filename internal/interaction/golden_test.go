package interaction

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/render"
)

func TestInteractionGoldenCaptures(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora")
	tests := []struct {
		name      string
		width     int
		configure func(*Store, *project.Node) Transient
	}{
		{name: "default", width: 1180},
		{name: "mutated", width: 1180, configure: func(store *Store, _ *project.Node) Transient {
			mustApplyGolden(t, store, "screen:main", []document.Action{{Action: "set", State: "plan", Value: "annual"}, {Action: "set", State: "status", Value: "Two months saved"}, {Action: "toggle", State: "details"}})
			mustApplyGolden(t, store, "screen:main/team-seats", []document.Action{{Action: "increment", State: "count", By: float64(2)}})
			return Transient{}
		}},
		{name: "hovered", width: 1180, configure: transientForName("annual-plan", "hovered")},
		{name: "pressed", width: 1180, configure: transientForName("annual-plan", "pressed")},
		{name: "focused", width: 1180, configure: transientForName("annual-plan", "focused")},
		{name: "disabled", width: 1180, configure: func(store *Store, root *project.Node) Transient {
			mustApplyGolden(t, store, "screen:main/team-seats", []document.Action{{Action: "decrement", State: "count", By: float64(3)}})
			return Transient{}
		}},
		{name: "wide", width: 1180},
		{name: "narrow", width: 480},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded, diagnostics := project.Load(repositoryRoot, entry, test.width)
			if len(diagnostics) != 0 {
				t.Fatalf("load diagnostics: %+v", diagnostics)
			}
			authored := loaded.Screens[loaded.Selected]
			store := NewStore()
			specs := make([]ScopeSpec, len(loaded.StateScopes))
			for index, scope := range loaded.StateScopes {
				specs[index] = ScopeSpec{ID: scope.ID, Context: scope.Context, State: scope.State, Initial: scope.Initial}
			}
			store.Reconcile(specs)
			transient := Transient{}
			if test.configure != nil {
				transient = test.configure(store, authored)
			}
			root := ResolvePersistentTree(authored, store.AllValues())
			// Resolve handles after persistent variants so hidden controls cannot be targeted.
			if test.name == "hovered" || test.name == "pressed" || test.name == "focused" {
				handle := handleByName(root, "annual-plan")
				switch test.name {
				case "hovered":
					transient.Hovered = handle
				case "pressed":
					transient.Pressed = handle
				case "focused":
					transient.Focused = handle
				}
			}
			state := render.State{Values: store.AllValues(), Hovered: transient.Hovered, Pressed: transient.Pressed, Focused: transient.Focused}
			output := filepath.Join(t.TempDir(), "capture.png")
			if err := render.Capture(output, root, image.Pt(test.width, 760), state, 1); err != nil {
				t.Fatal(err)
			}
			captured, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "interaction-"+test.name+".png")
			if os.Getenv("GORA_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, captured, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with GORA_UPDATE_GOLDEN=1)", err)
			}
			if !bytes.Equal(captured, want) {
				t.Fatalf("capture differs from %s (run with GORA_UPDATE_GOLDEN=1 to review and update)", golden)
			}
		})
	}
}

func mustApplyGolden(t *testing.T, store *Store, scope string, actions []document.Action) {
	t.Helper()
	if err := store.Apply(scope, actions); err != nil {
		t.Fatal(err)
	}
}

func transientForName(name, kind string) func(*Store, *project.Node) Transient {
	return func(_ *Store, root *project.Node) Transient {
		handle := handleByName(root, name)
		switch kind {
		case "hovered":
			return Transient{Hovered: handle}
		case "pressed":
			return Transient{Pressed: handle}
		case "focused":
			return Transient{Focused: handle}
		default:
			return Transient{}
		}
	}
}

func handleByName(root *project.Node, name string) string {
	if root == nil {
		return ""
	}
	if root.Name == name {
		return root.Handle
	}
	for _, child := range root.Children {
		if handle := handleByName(child, name); handle != "" {
			return handle
		}
	}
	return ""
}
