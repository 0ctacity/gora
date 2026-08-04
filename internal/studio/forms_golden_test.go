package studio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/interaction"
)

func TestFormsGoldenCaptures(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "forms", "app.gora")
	tests := []struct {
		name      string
		width     int
		scale     int
		configure func(*Runtime) error
	}{
		{name: "default", width: 960},
		{name: "default-2x", width: 960, scale: 2},
		{name: "focused", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			field := namedSemanticNode(tree, "name-field")
			runtime.SetTransient(interaction.Transient{Focused: field.Handle})
			return nil
		}},
		{name: "selected", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			field := namedSemanticNode(tree, "name-field")
			runtime.SetTransient(interaction.Transient{Focused: field.Handle})
			return runtime.SetFieldSelection(field.ID, 0, 3)
		}},
		{name: "composing", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			field := namedSemanticNode(tree, "biography-field")
			if err := runtime.SetFieldDraft(field.ID, "İstanbul'da çalışan bir matematikçi.\nAnalytical Engine üzerine yazıyor."); err != nil {
				return err
			}
			runtime.SetTransient(interaction.Transient{Focused: field.Handle})
			if err := runtime.SetFieldComposition(field.ID, 0, 1); err != nil {
				return err
			}
			runtime.Scroll(520)
			return nil
		}},
		{name: "invalid", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			return runtime.SetFieldDraft(namedSemanticNode(tree, "email-field").ID, "invalid")
		}},
		{name: "submitted", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			if err := runtime.SetFieldDraft(namedSemanticNode(tree, "name-field").ID, "Grace Hopper"); err != nil {
				return err
			}
			return runtime.SubmitForm(namedSemanticNode(tree, "profile-form").ID)
		}},
		{name: "reset", width: 960, configure: func(runtime *Runtime) error {
			tree, err := runtime.RuntimeTree()
			if err != nil {
				return err
			}
			if err := runtime.SetFieldDraft(namedSemanticNode(tree, "name-field").ID, "Temporary"); err != nil {
				return err
			}
			return runtime.ResetForm(namedSemanticNode(tree, "profile-form").ID)
		}},
		{name: "wide", width: 1120},
		{name: "narrow", width: 480},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := NewRuntime(repositoryRoot, entry)
			if err != nil {
				t.Fatal(err)
			}
			runtime.SetViewport(test.width, 760)
			if test.configure != nil {
				if err := test.configure(runtime); err != nil {
					t.Fatal(err)
				}
			}
			scale := test.scale
			if scale == 0 {
				scale = 1
			}
			captured, _, err := runtime.CapturePNG(scale)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "forms-"+test.name+".png")
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
