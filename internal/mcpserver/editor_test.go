package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyDocumentChangesCreatesAndPatchesValidatedDocuments(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := registry.ApplyDocumentChanges(project.ID, []DocumentChange{
		{Mode: "create", File: "theme.gora", Document: map[string]any{"gora": float64(1), "kind": "tokens", "tokens": map[string]any{}}},
		{Mode: "create", File: "app.gora", Document: map[string]any{
			"gora": float64(1), "kind": "app",
			"viewport": map[string]any{"width": float64(100), "height": float64(80)},
			"entry":    "main", "screens": map[string]any{"main": map[string]any{"type": "spacer"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Documents) != 2 {
		t.Fatalf("created = %+v", created)
	}
	source, err := os.ReadFile(filepath.Join(root, "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "{") || !strings.HasSuffix(string(source), "\n") {
		t.Fatalf("not canonical block YAML:\n%s", source)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	before, err := registry.DocumentResource(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := registry.ApplyDocumentChanges(project.ID, []DocumentChange{{
		Mode: "patch", File: "app.gora", ExpectedRevision: before.Revision,
		Operations: []PatchOperation{{Op: "replace", Path: "/viewport/width", Value: float64(320)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Documents[0].Revision == before.Revision {
		t.Fatal("revision did not change")
	}
	after, err := registry.ViewSummary(project.ID, view.ID)
	if err != nil || after.Viewport.Width != 320 {
		t.Fatalf("reloaded view error=%v summary=%+v", err, after)
	}
	if _, err := registry.ApplyDocumentChanges(project.ID, []DocumentChange{{
		Mode: "patch", File: "app.gora", ExpectedRevision: before.Revision,
		Operations: []PatchOperation{{Op: "replace", Path: "/viewport/width", Value: float64(640)}},
	}}); err == nil {
		t.Fatal("stale revision was accepted")
	}
}

func TestJSONPointerArrayInsertAndEscaping(t *testing.T) {
	value := any(map[string]any{"a/b": []any{"first"}})
	updated, err := applyPatch(value, PatchOperation{Op: "add", Path: "/a~1b/-", Value: "second"})
	if err != nil {
		t.Fatal(err)
	}
	items := updated.(map[string]any)["a/b"].([]any)
	if len(items) != 2 || items[1] != "second" {
		t.Fatalf("items = %#v", items)
	}
}
