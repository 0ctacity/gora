package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	exit := Run([]string{"run", path, "--root", dir}, &stdout, &stderr, func(LaunchConfig) error {
		launched = true
		return nil
	})
	if exit != 1 || !launched {
		t.Fatalf("exit=%d launched=%v stderr=%s", exit, launched, stderr.String())
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
