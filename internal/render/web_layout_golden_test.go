package render

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/project"
)

func TestWebLayoutGoldenCaptures(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(repositoryRoot, "examples", "web-layout", "app.gora")
	cases := []struct {
		name  string
		width int
	}{
		{name: "wide", width: 1280},
		{name: "medium", width: 840},
		{name: "narrow", width: 480},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			loaded, diagnostics := project.Load(repositoryRoot, entry, test.width)
			if len(diagnostics) != 0 {
				t.Fatalf("load diagnostics: %+v", diagnostics)
			}
			root := loaded.Root
			if screen := loaded.Screens[loaded.Selected]; screen != nil {
				root = screen
			}
			if root == nil {
				t.Fatal("loaded example has no selected root")
			}
			captured, err := captureGio(root, image.Pt(test.width, loaded.Viewport.Height), State{}, 1)
			if err != nil {
				skipMetalUnavailable(t, err)
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, captured); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "web-layout-"+test.name+".png")
			if os.Getenv("GORA_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, encoded.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with GORA_UPDATE_GOLDEN=1)", err)
			}
			if !bytes.Equal(encoded.Bytes(), want) {
				t.Fatalf("capture differs from %s (run with GORA_UPDATE_GOLDEN=1 to review and update)", golden)
			}
		})
	}
}
