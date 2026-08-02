package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectWatcherReloadsAnAffectedView(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	write := func(width int) {
		t.Helper()
		source := fmt.Sprintf("gora: 1\nkind: app\nviewport: { width: %d, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n", width)
		if err := os.WriteFile(entry, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(100)
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	write(220)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, summaryErr := registry.ViewSummary(project.ID, view.ID)
		if summaryErr == nil && current.Viewport.Width == 220 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, _ := registry.ViewSummary(project.ID, view.ID)
	t.Fatalf("watcher did not reload view: %+v", current)
}

func TestRegistryReusesCanonicalProjectsAndViews(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()

	first, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.OpenProject(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID || len(registry.ListProjects()) != 1 {
		t.Fatalf("projects: first=%+v second=%+v all=%+v", first, second, registry.ListProjects())
	}

	view1, err := registry.OpenView(first.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	view2, err := registry.OpenView(first.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	if view1.ID == "" || view1.ID != view2.ID || !view1.Valid || len(registry.ListViews(first.ID)) != 1 {
		t.Fatalf("views: first=%+v second=%+v", view1, view2)
	}
}

func TestRegistryOpensInvalidAndTokenDocuments(t *testing.T) {
	root := t.TempDir()
	invalid := filepath.Join(root, "bad.gora")
	tokens := filepath.Join(root, "theme.gora")
	if err := os.WriteFile(invalid, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokens, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := registry.OpenView(project.ID, "bad.gora")
	if err != nil {
		t.Fatal(err)
	}
	if bad.Valid || len(bad.Diagnostics) == 0 {
		t.Fatalf("invalid view = %+v", bad)
	}
	theme, err := registry.OpenView(project.ID, "theme.gora")
	if err != nil {
		t.Fatal(err)
	}
	if !theme.Valid || theme.Kind != "tokens" || theme.RuntimeAvailable {
		t.Fatalf("token view = %+v", theme)
	}
}

func TestRegistryRejectsViewOutsideProject(t *testing.T) {
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "app.gora")
	if err := os.WriteFile(outside, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenView(project.ID, outside); err == nil {
		t.Fatal("outside view was accepted")
	}
}
