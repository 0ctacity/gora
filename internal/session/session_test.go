package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerRoundTrip(t *testing.T) {
	socket := filepath.Join(socketTempDir(t), "gora.sock")
	server, err := Listen(socket, func(_ context.Context, request Request) Response {
		return Response{OK: true, Warning: request.Action}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response, err := Send(socket, Request{Action: "focus"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Warning != "focus" {
		t.Fatalf("response = %#v", response)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	socket := filepath.Join(socketTempDir(t), "gora.sock")
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := Listen(socket, func(context.Context, Request) Response { return Response{OK: true} })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
}

func TestSocketPathSeparatesRuntimeModes(t *testing.T) {
	dir := socketTempDir(t)
	app, err := SocketPath(dir, filepath.Join(dir, "app.gora"), "app")
	if err != nil {
		t.Fatal(err)
	}
	studio, err := SocketPath(dir, filepath.Join(dir, "app.gora"), "studio")
	if err != nil {
		t.Fatal(err)
	}
	if app == studio {
		t.Fatal("app and Studio session paths match")
	}
}

func socketTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "gora-session-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
