package project

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gora/internal/document"
)

// TestBundledExamplesValidateStrictly keeps every checked-in document on the
// same validation path used by the CLI.  The example directory is the
// containment root for each document so component imports can resolve to
// their sibling tokens/components without widening the project boundary.
func TestBundledExamplesValidateStrictly(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	examplesRoot := filepath.Join(repositoryRoot, "examples")
	var files []string
	err := filepath.WalkDir(examplesRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".gora" {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no bundled .gora examples found")
	}
	for _, path := range files {
		path := path
		t.Run(filepath.ToSlash(filepath.Join("examples", mustRelative(t, examplesRoot, path))), func(t *testing.T) {
			relative, err := filepath.Rel(examplesRoot, path)
			if err != nil {
				t.Fatal(err)
			}
			projectName := relative
			for i := 0; i < len(relative); i++ {
				if relative[i] == filepath.Separator {
					projectName = relative[:i]
					break
				}
			}
			root := filepath.Join(examplesRoot, projectName)
			if diagnostics := Validate(root, path); len(diagnostics) != 0 {
				t.Fatalf("%s diagnostics: %s", path, formatDiagnostics(diagnostics))
			}
		})
	}
}

func mustRelative(t *testing.T, root, path string) string {
	t.Helper()
	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return relative
}

func formatDiagnostics(diagnostics []document.Diagnostic) string {
	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		parts = append(parts, fmt.Sprintf("%s: %s", diagnostic.Code, diagnostic.Message))
	}
	return strings.Join(parts, "; ")
}
