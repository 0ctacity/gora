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

func TestSemanticControlsGoldenCaptures(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "semantic-controls", "app.gora")
	tests := []struct {
		name      string
		width     int
		configure func(*Store, *project.Node) Transient
	}{
		{name: "default", width: 960},
		{name: "mutated", width: 960, configure: func(store *Store, _ *project.Node) Transient {
			mustApplyGolden(t, store, "screen:controls", []document.Action{{Action: "toggle", State: "notifications"}, {Action: "set", State: "plan", Value: "annual"}, {Action: "set", State: "section", Value: "security"}, {Action: "set", State: "team", Value: "design"}, {Action: "set", State: "volume", Value: float64(75)}, {Action: "set", State: "seats", Value: float64(8)}})
			return Transient{}
		}},
		{name: "focused", width: 960, configure: semanticTransient("volume-slider", "focused")},
		{name: "selected", width: 960, configure: func(store *Store, _ *project.Node) Transient {
			mustApplyGolden(t, store, "screen:controls", []document.Action{{Action: "set", State: "plan", Value: "annual"}})
			return Transient{}
		}},
		{name: "disabled", width: 960, configure: semanticPopupTransient("team-select", "engineering-option")},
		{name: "popup-open", width: 960, configure: semanticPopupTransient("team-select", "design-option")},
		{name: "wide", width: 960},
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
			root := ResolveTree(authored, store.AllValues(), transient)
			state := render.State{Values: store.AllValues(), Hovered: transient.Hovered, Pressed: transient.Pressed, Focused: transient.Focused, OpenSelect: transient.OpenSelect, ActiveOption: transient.ActiveOption}
			output := filepath.Join(t.TempDir(), "capture.png")
			if err := render.Capture(output, root, image.Pt(test.width, 760), state, 1); err != nil {
				t.Fatal(err)
			}
			captured, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "semantic-controls-"+test.name+".png")
			if os.Getenv("GORA_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, captured, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with GORA_UPDATE_GOLDEN=1)", err)
			}
			if !bytes.Equal(captured, want) {
				t.Fatalf("capture differs from %s", golden)
			}
		})
	}
}

func semanticTransient(name, kind string) func(*Store, *project.Node) Transient {
	return func(_ *Store, root *project.Node) Transient {
		handle := handleByName(root, name)
		if kind == "focused" {
			return Transient{Focused: handle}
		}
		return Transient{}
	}
}

func semanticPopupTransient(selectName, optionName string) func(*Store, *project.Node) Transient {
	return func(_ *Store, root *project.Node) Transient {
		return Transient{OpenSelect: handleByName(root, selectName), ActiveOption: handleByName(root, optionName)}
	}
}
