package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersionedHandshakeValidation(t *testing.T) {
	identity := HostIdentity{InstanceID: "host-1", Root: "/project", Document: "/project/app.gora", Mode: HostModeApp, PID: 42, Automation: true, Capabilities: []string{"clock", "snapshot"}}
	if err := ValidateHandshake(identity, identity, ProtocolVersion, HostModeApp); err != nil {
		t.Fatalf("valid handshake rejected: %v", err)
	}
	for name, mutate := range map[string]func(*HostIdentity){
		"automation": func(value *HostIdentity) { value.Automation = false },
		"mode":       func(value *HostIdentity) { value.Mode = HostModeStudio },
		"instance":   func(value *HostIdentity) { value.InstanceID = "other" },
		"pid":        func(value *HostIdentity) { value.PID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := identity
			mutate(&candidate)
			if err := ValidateHandshake(identity, candidate, ProtocolVersion, HostModeApp); err == nil {
				t.Fatal("invalid handshake accepted")
			}
		})
	}
}

func TestHandshakeCanonicalizesSymlinkIdentity(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	expected := HostIdentity{Root: actual, Document: filepath.Join(actual, "app.gora"), Mode: HostModeApp}
	got := expected
	got.Root = link
	got.Document = filepath.Join(link, "app.gora")
	got.Automation = true
	got.PID = 1
	got.Capabilities = []string{}
	if err := ValidateHandshake(expected, got, ProtocolVersion, HostModeApp); err != nil {
		t.Fatalf("symlink-equivalent handshake rejected: %v", err)
	}
}

func TestVersionedRequestStrictDecodePreservesLegacy(t *testing.T) {
	legacy, err := DecodeRequest([]byte(`{"action":"focus"}`))
	if err != nil || legacy.Action != "focus" {
		t.Fatalf("legacy request = %#v err=%v", legacy, err)
	}
	encoded, _ := json.Marshal(Request{Version: ProtocolVersion, RequestID: "r1", Action: ActionSnapshot, Payload: json.RawMessage(`{}`)})
	request, err := DecodeRequest(encoded)
	if err != nil || request.Version != ProtocolVersion || request.RequestID != "r1" {
		t.Fatalf("versioned request = %#v err=%v", request, err)
	}
	if _, err := DecodeRequest([]byte(`{"action":"focus","unknown":true}`)); err == nil {
		t.Fatal("unknown request field accepted")
	}
	if _, err := DecodeRequest([]byte(`{"action":"focus"}{"action":"render"}`)); err == nil {
		t.Fatal("trailing request value accepted")
	}
}

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
