package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/session"
)

func TestValidateJSONContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.gora")
	if err := os.WriteFile(path, []byte("gora: 2\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"validate", path, "--root", dir, "--format", "json"}, &stdout, &stderr, nil)
	if exit != 1 {
		t.Fatalf("exit = %d, stderr=%s", exit, stderr.String())
	}
	var report JSONReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if report.SchemaVersion != 1 || report.Valid || len(report.Diagnostics) == 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestUsageFailureUsesExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"render"}, &stdout, &stderr, nil); exit != 2 {
		t.Fatalf("exit = %d", exit)
	}
}

func TestRunRejectsTokenModuleWithoutLaunching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launched := false
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"run", path, "--root", dir}, &stdout, &stderr, func(LaunchConfig) error {
		launched = true
		return nil
	})
	if exit != 2 || launched {
		t.Fatalf("exit=%d launched=%v stderr=%s", exit, launched, stderr.String())
	}
}

func TestRunSelectsAppStudioAndHeadlessModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		mode LaunchMode
	}{
		{name: "app", args: nil, mode: LaunchApp},
		{name: "studio", args: []string{"--studio"}, mode: LaunchStudio},
		{name: "headless", args: []string{"--headless"}, mode: LaunchHeadless},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var launched LaunchConfig
			args := append([]string{"run", path, "--root", dir}, test.args...)
			var stdout, stderr bytes.Buffer
			exit := Run(args, &stdout, &stderr, func(config LaunchConfig) error {
				launched = config
				return nil
			})
			if exit != 0 || launched.Mode != test.mode {
				t.Fatalf("exit=%d mode=%q stderr=%s", exit, launched.Mode, stderr.String())
			}
		})
	}
}

func TestRunRejectsConflictingHostModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"run", path, "--root", dir, "--studio", "--headless"}, &stdout, &stderr, func(LaunchConfig) error {
		t.Fatal("conflicting modes launched")
		return nil
	})
	if exit != 2 || !bytes.Contains(stderr.Bytes(), []byte("mutually exclusive")) {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}

func TestValidateMissingFileUsesExitTwo(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"validate", filepath.Join(dir, "missing.gora"), "--root", dir}, &stdout, &stderr, nil)
	if exit != 2 {
		t.Fatalf("exit=%d output=%s", exit, stdout.String())
	}
}

func TestRunInvalidDocumentLaunchesDiagnosticStudioAndReturnsOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launched := false
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"run", path, "--root", dir, "--studio"}, &stdout, &stderr, func(config LaunchConfig) error {
		if config.Mode != LaunchStudio {
			t.Fatalf("mode = %q", config.Mode)
		}
		launched = true
		return nil
	})
	if exit != 1 || !launched {
		t.Fatalf("exit=%d launched=%v stderr=%s", exit, launched, stderr.String())
	}
}

func TestRunInvalidDocumentDoesNotOpenPlainApp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launched := false
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"run", path, "--root", dir}, &stdout, &stderr, func(LaunchConfig) error {
		launched = true
		return nil
	})
	if exit != 1 || launched {
		t.Fatalf("exit=%d launched=%v stderr=%s", exit, launched, stderr.String())
	}
}

func TestRenderSelectsRequestedLiveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, resolvedPath, err := canonicalPair(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	appSocket, err := session.SocketPath(resolvedRoot, resolvedPath, string(LaunchApp))
	if err != nil {
		t.Fatal(err)
	}
	headlessSocket, err := session.SocketPath(resolvedRoot, resolvedPath, string(LaunchHeadless))
	if err != nil {
		t.Fatal(err)
	}
	if appSocket == headlessSocket {
		t.Fatal("mode-specific sessions share a socket")
	}
	server, err := session.Listen(headlessSocket, func(_ context.Context, request session.Request) session.Response {
		if request.Action != "render" {
			return session.Response{Error: request.Action}
		}
		return session.Response{OK: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"render", path, "--root", dir, "--from", "headless", "--output", filepath.Join(dir, "capture.png")}, &stdout, &stderr, nil)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}

func TestRunInvalidTokenModuleStillDoesNotLaunch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.gora")
	if err := os.WriteFile(path, []byte("gora: 2\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launched := false
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"run", path, "--root", dir}, &stdout, &stderr, func(LaunchConfig) error {
		launched = true
		return nil
	})
	if exit != 2 || launched {
		t.Fatalf("exit=%d launched=%v stderr=%s", exit, launched, stderr.String())
	}
}
