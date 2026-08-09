package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gora/internal/session"
)

func TestSessionHandlerHandshakeAndCommandUseHostLoop(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	runtime := NewRuntimeAllowInvalid(root, entry)
	controller := NewHostController(2)
	defer controller.Close()
	identity := session.HostIdentity{InstanceID: "host-test", Root: root, Document: entry, Mode: session.HostModeApp, PID: 7, Automation: true, Capabilities: []string{"snapshot"}}
	handler := runtime.SessionHandlerWithController("app", identity, controller, nil)
	payload, _ := json.Marshal(session.HandshakePayload{Root: root, Document: entry, Mode: session.HostModeApp, Protocol: session.ProtocolVersion})
	response := handler(context.Background(), session.Request{Version: session.ProtocolVersion, RequestID: "h", Action: session.ActionHandshake, Payload: payload})
	if !response.OK {
		t.Fatalf("handshake response=%+v", response)
	}
	commandPayload, _ := json.Marshal(struct {
		Kind string `json:"kind"`
	}{Kind: "snapshot"})
	done := make(chan session.Response, 1)
	go func() {
		done <- handler(context.Background(), session.Request{Version: session.ProtocolVersion, RequestID: "c", Action: session.ActionCommand, Payload: commandPayload})
	}()
	select {
	case <-done:
		t.Fatal("command completed before host publication")
	case <-time.After(20 * time.Millisecond):
	}
	for _, command := range controller.Drain() {
		controller.Complete(command.RequestID, command.Apply())
	}
	controller.Publish(HostPublication{FrameRevision: 1})
	select {
	case response := <-done:
		if !response.OK {
			t.Fatalf("command response=%+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not complete")
	}
}

func TestHostPayloadRejectsTrailingJSON(t *testing.T) {
	var target struct {
		Kind string `json:"kind"`
	}
	if err := decodePayload(json.RawMessage(`{"kind":"snapshot"}{"extra":true}`), &target); err == nil {
		t.Fatal("trailing payload accepted")
	}
}

func TestHostControllerCompletesCommandsOnlyAfterPublication(t *testing.T) {
	controller := NewHostController(2)
	defer controller.Close()
	done := make(chan error, 1)
	go func() {
		done <- controller.Submit(context.Background(), HostCommand{RequestID: "r1", Apply: func() error { return nil }})
	}()
	select {
	case <-done:
		t.Fatal("command completed before host loop drained and published")
	case <-time.After(20 * time.Millisecond):
	}
	var commands []HostCommand
	deadline := time.Now().Add(time.Second)
	for len(commands) == 0 && time.Now().Before(deadline) {
		commands = controller.Drain()
		if len(commands) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if len(commands) == 0 {
		t.Fatal("command was not queued")
	}
	if len(commands) != 1 {
		t.Fatalf("drained commands = %d", len(commands))
	}
	controller.Complete(commands[0].RequestID, commands[0].Apply())
	select {
	case <-done:
		t.Fatal("command completed before frame publication")
	case <-time.After(20 * time.Millisecond):
	}
	controller.Publish(HostPublication{FrameRevision: 1})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not complete after publication")
	}
}

func TestHostControllerPublishesResultDataAfterFrame(t *testing.T) {
	controller := NewHostController(1)
	defer controller.Close()
	done := make(chan struct {
		data json.RawMessage
		err  error
	}, 1)
	go func() {
		data, err := controller.SubmitResult(context.Background(), HostCommand{RequestID: "result", Apply: func() error { return nil }})
		done <- struct {
			data json.RawMessage
			err  error
		}{data, err}
	}()
	var commands []HostCommand
	deadline := time.Now().Add(time.Second)
	for len(commands) == 0 && time.Now().Before(deadline) {
		commands = controller.Drain()
		if len(commands) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if len(commands) == 0 {
		t.Fatal("command was not queued")
	}
	controller.Complete(commands[0].RequestID, nil, json.RawMessage(`{"value":42}`))
	select {
	case <-done:
		t.Fatal("result completed before publication")
	case <-time.After(20 * time.Millisecond):
	}
	controller.Publish(HostPublication{FrameRevision: 1})
	select {
	case result := <-done:
		if result.err != nil || string(result.data) != `{"value":42}` {
			t.Fatalf("result=%s err=%v", result.data, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("result did not complete")
	}
}

func TestHostControllerQueueIsBounded(t *testing.T) {
	controller := NewHostController(1)
	defer controller.Close()
	if err := controller.TrySubmit(HostCommand{RequestID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.TrySubmit(HostCommand{RequestID: "two"}); err == nil {
		t.Fatal("bounded queue accepted a second command")
	}
}

func TestHostWatcherQueuesOneReloadPerDiskChange(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 40, height: 30 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reloads atomic.Int32
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- runtime.WatchWithReload(ctx, nil, func() {
			reloads.Add(1)
			runtime.Reload()
		})
	}()
	time.Sleep(100 * time.Millisecond)
	updated := []byte("gora: 1\nkind: app\nviewport: { width: 41, height: 30 }\nentry: main\nscreens:\n  main: { type: spacer }\n")
	if err := os.WriteFile(entry, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for reloads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reloads.Load() != 1 {
		t.Fatalf("reload count=%d, want exactly one", reloads.Load())
	}
	cancel()
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}
