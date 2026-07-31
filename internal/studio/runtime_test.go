package studio

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadPreservesLastGoodFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 200 }
entry: main
screens:
  main:
    type: surface
    props: { background: "#112233" }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot()
	if before.Root == nil || before.Invalid {
		t.Fatalf("initial snapshot = %#v", before)
	}

	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	after := runtime.Snapshot()
	if after.Root != before.Root || !after.Invalid || len(after.Diagnostics) == 0 {
		t.Fatalf("last-good state not preserved: %#v", after)
	}
}

func TestNamedScrollPersistsAndUnnamedScrollResets(t *testing.T) {
	tests := []struct {
		name      string
		nodeName  string
		preserved bool
	}{
		{name: "named", nodeName: "feed", preserved: true},
		{name: "unnamed", preserved: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.gora")
			nameLine := ""
			if test.nodeName != "" {
				nameLine = "\n    name: " + test.nodeName
			}
			source := `gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: scroll` + nameLine + `
    props: { axis: vertical }
    children:
      - type: spacer
        props: { height: 400 }
`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(dir, path)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Scroll(30)
			if len(runtime.Snapshot().Scroll) != 1 {
				t.Fatal("scroll offset was not recorded")
			}
			runtime.Reload()
			got := len(runtime.Snapshot().Scroll) == 1
			if got != test.preserved {
				t.Fatalf("preserved=%v, want %v", got, test.preserved)
			}
		})
	}
}

func TestScrollAxisTargetsHorizontalScrollAlongsideVerticalScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: overlay
    children:
      - type: scroll
        name: feed
        props: { axis: vertical }
        children:
          - type: spacer
            props: { height: 400 }
      - type: scroll
        name: rail
        props: { axis: horizontal }
        children:
          - type: spacer
            props: { width: 400 }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}

	runtime.ScrollAxis("horizontal", 30)

	scroll := runtime.Snapshot().Scroll
	if got := scroll["rail"].X; got != 30 {
		t.Fatalf("horizontal offset = %d", got)
	}
	if _, ok := scroll["feed"]; ok {
		t.Fatal("horizontal gesture moved the vertical scroll node")
	}
}

func TestSetScrollOffsetSupportsDraggableScrollbar(t *testing.T) {
	runtime := &Runtime{scroll: make(map[string]image.Point)}
	runtime.SetScrollOffset("feed", "vertical", 95)
	runtime.SetScrollOffset("gallery", "horizontal", 42)
	snapshot := runtime.Snapshot()
	if got := snapshot.Scroll["feed"].Y; got != 95 {
		t.Fatalf("vertical offset = %d", got)
	}
	if got := snapshot.Scroll["gallery"].X; got != 42 {
		t.Fatalf("horizontal offset = %d", got)
	}
}

func TestCaptureUsesViewportScaleAndWarnsForLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 20, height: 10 }
entry: main
screens:
  main: { type: surface, props: { background: "#112233" } }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	output := filepath.Join(dir, "capture.png")
	warning, err := runtime.Capture(output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if warning == "" {
		t.Fatal("missing last-good warning")
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Width != 40 || captured.Height != 20 {
		t.Fatalf("capture size = %dx%d", captured.Width, captured.Height)
	}
}
