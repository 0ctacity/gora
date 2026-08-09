package studio

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gora/internal/interaction"
	"gora/internal/semantic"
)

func TestRuntimeWaitForViewBlocksUntilMatchingPublishedFrame(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	done := make(chan AutomationSnapshot, 1)
	go func() {
		got, _ := runtime.WaitForView(context.Background(), WaitForViewRequest{
			AfterFrameRevision: initial.FrameRevision,
			AfterFrameSet:      true,
			Condition:          "published",
			StableFrames:       1,
			Timeout:            time.Second,
		})
		done <- got
	}()
	select {
	case <-done:
		t.Fatal("wait returned before a new frame")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	ready := runtime.AutomationSnapshot()
	if !ready.Agreement || !ready.RuntimePublished || !ready.GeometryPublished || ready.UnpublishedGeometry || !ready.Idle {
		t.Fatalf("ready automation snapshot = %+v", ready)
	}
	select {
	case got := <-done:
		if got.FrameRevision <= initial.FrameRevision || !got.Agreement {
			t.Fatalf("wait result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe the published frame")
	}
}

func TestRuntimeWaitForViewExplicitSatisfiedRevisionReturnsImmediately(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	got, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
		AfterFrameRevision:    initial.FrameRevision,
		AfterFrameSet:         true,
		AfterRuntimeRevision:  initial.RuntimeRevision,
		AfterRuntimeSet:       true,
		AllowAlreadySatisfied: true,
		Condition:             "published",
		StableFrames:          1,
		Timeout:               time.Second,
	})
	if err != nil || got.FrameRevision != initial.FrameRevision {
		t.Fatalf("immediate wait result=%+v err=%v initial=%+v", got, err, initial)
	}
}

func TestRuntimeWaitForViewTimeoutReturnsLatestSnapshot(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	got, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
		AfterFrameRevision: initial.FrameRevision,
		AfterFrameSet:      true,
		Condition:          "published",
		StableFrames:       1,
		Timeout:            10 * time.Millisecond,
	})
	var timeout *WaitTimeoutError
	if !errors.As(err, &timeout) || timeout.Snapshot.FrameRevision != got.FrameRevision {
		t.Fatalf("timeout err=%v snapshot=%+v latest=%+v", err, timeout, got)
	}
}

func TestRuntimeWaitForViewCloseWakesWaiter(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	done := make(chan error, 1)
	go func() {
		_, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
			AfterFrameRevision: initial.FrameRevision,
			AfterFrameSet:      true,
			Condition:          "published",
			StableFrames:       1,
			Timeout:            time.Second,
		})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	runtime.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRuntimeClosed) {
			t.Fatalf("close wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake waiter")
	}
}

func TestRuntimeAutomationSnapshotPreservesLastGoodOnInvalidReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n")
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	invalid := runtime.AutomationSnapshot()
	if invalid.Valid || !invalid.LastGoodAvailable || invalid.Agreement || invalid.RuntimePublished || invalid.GeometryPublished || invalid.publicationStreak != 0 || !invalid.UnpublishedGeometry || len(invalid.Diagnostics) == 0 {
		t.Fatalf("invalid last-good snapshot = %+v", invalid)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	if afterRead := runtime.AutomationSnapshot(); afterRead.FrameRevision != invalid.FrameRevision {
		t.Fatalf("invalid RuntimeTree republished the stale frame: before=%+v after=%+v", invalid, afterRead)
	}
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	recovered := runtime.AutomationSnapshot()
	if !recovered.Valid || !recovered.Agreement || recovered.ReloadRevision <= invalid.ReloadRevision {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
}

func TestRuntimeAutomationWaitsShareBoundedPublicationNotification(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	const waiters = 100
	done := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			_, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
				AfterFrameRevision: initial.FrameRevision,
				AfterFrameSet:      true,
				Condition:          "published",
				StableFrames:       1,
				Timeout:            time.Second,
			})
			done <- err
		}()
	}
	for i := 0; i < waiters; i++ {
		if _, err := runtime.RuntimeTree(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < waiters; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("waiter %d error=%v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d did not wake", i)
		}
	}
	final := runtime.AutomationSnapshot()
	if final.FrameRevision != initial.FrameRevision+waiters {
		t.Fatalf("repeated publications were not accounted for exactly: initial=%d final=%d", initial.FrameRevision, final.FrameRevision)
	}
	runtime.mu.RLock()
	bounded := runtime.syncCh != nil && runtime.doneCh != nil
	runtime.mu.RUnlock()
	if !bounded {
		t.Fatal("publication notification channels were not retained")
	}
}

func TestRuntimeAutomationSnapshotIncludesPublishedRouterTransientState(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	router := interaction.NewRouter()
	button := &semantic.Node{
		Type: "button", Role: "button", Handle: "button-handle", ID: "button-id",
		Visible: true, InViewport: true, Enabled: true,
		Bounds: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 30},
		Clip:   &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 30},
	}
	router.Update(button)
	router.SetPointerMetadata("mouse", 1, image.Pt(10, 12))
	if !router.Press(7, image.Pt(10, 12)) {
		t.Fatal("button press was not captured")
	}
	runtime.PublishRouterSnapshot(router.Snapshot())
	snapshot := runtime.AutomationSnapshot()
	if snapshot.Router.PointerCapture == nil || snapshot.Router.PointerCapture.OwnerID != button.ID || snapshot.Router.PointerCapture.PointerID != 7 {
		t.Fatalf("runtime router snapshot = %+v", snapshot.Router)
	}
}

func TestRuntimeSnapshotReportsGeometryRevisionAfterEffectiveTreeRebuild(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	runtime.SetViewport(120, 80)
	snapshot := runtime.Snapshot()
	automation := runtime.AutomationSnapshot()
	if snapshot.GeometryRevision == 0 || snapshot.GeometryRevision != automation.GeometryRevision {
		t.Fatalf("geometry revisions diverged after rebuild: snapshot=%d automation=%d", snapshot.GeometryRevision, automation.GeometryRevision)
	}
}

func TestRuntimeAutomationSnapshotReadsDoNotAdvanceRevisionsAfterResolution(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	first := runtime.AutomationSnapshot()
	second := runtime.AutomationSnapshot()
	if first.RuntimeRevision != second.RuntimeRevision || first.GeometryRevision != second.GeometryRevision || first.FrameRevision != second.FrameRevision || first.ReloadRevision != second.ReloadRevision || first.AutomationInputRevision != second.AutomationInputRevision {
		t.Fatalf("repeated automation reads advanced revisions: first=%+v second=%+v", first, second)
	}
}

func TestRuntimeAutomationSnapshotDoesNotReportAgreementDuringCandidateReload(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	runtime.mu.Lock()
	runtime.candidateReload = true
	runtime.mu.Unlock()
	snapshot := runtime.AutomationSnapshot()
	if snapshot.Agreement || snapshot.Idle || !snapshot.CandidateReload || snapshot.publicationStreak != 0 {
		t.Fatalf("candidate reload reported a current frame: %+v", snapshot)
	}
}

func TestRuntimeAutomationJSONUsesPublicEditingSnapshot(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	runtime.editing.Reconcile([]interaction.FieldSpec{{ID: "field", Scope: "screen:main", Binding: "name", Type: "text", Value: "Ada"}})
	if err := runtime.editing.SetDraft("field", "Ada Lovelace"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.editing.SetComposition("field", 0, 3); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(runtime.AutomationSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, hidden := range []string{"undo", "redo", "internal_offset", "manual_scroll", "preferred_column", "visual_columns"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("automation JSON exposed internal editing field %q: %s", hidden, text)
		}
	}
	for _, hidden := range []string{"publication_streak", "publicationStartFrame"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("automation JSON exposed internal synchronization field %q: %s", hidden, text)
		}
	}
	if !strings.Contains(text, `"editing":{"revision":`) || !strings.Contains(text, `"id":"field"`) {
		t.Fatalf("automation JSON omitted public editing metadata: %s", text)
	}
}

func TestRuntimeAutomationSnapshotIncludesCurrentFieldPublicMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field.gora")
	source := []byte(`gora: 1
kind: app
viewport: { width: 240, height: 100 }
state:
  name: { type: text, default: Ada Lovelace }
entry: main
screens:
  main:
    type: text_field
    name: name-field
    props: { label: Name, bind: name, min_length: 5 }
    children: [{ type: field_box }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	field := namedSemanticNode(tree, "name-field")
	if field == nil {
		t.Fatal("canonical textbox is absent")
	}
	if err := runtime.editing.SetDraft(field.ID, "Ada"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.editing.SetRuneSelection(field.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	if !runtime.editing.Touch(field.ID) {
		t.Fatal("field touch did not update editing state")
	}
	if err := runtime.editing.SetComposition(field.ID, 0, 1); err != nil {
		t.Fatal(err)
	}
	runtime.PublishRouterSnapshot(interaction.RouterSnapshot{FocusedID: field.ID})
	snapshot := runtime.AutomationSnapshot()
	current := snapshot.CurrentField
	if snapshot.CurrentFieldID != field.ID || current == nil {
		t.Fatalf("current field identity = %q/%+v, want %q", snapshot.CurrentFieldID, current, field.ID)
	}
	if current.ID != field.ID || current.Draft != "Ada" || current.Committed != "Ada Lovelace" || current.SelectionStart != 1 || current.SelectionEnd != 2 || current.Composition != "A" || current.CompositionStart != 0 || current.CompositionEnd != 1 || !current.Composing || !current.Focused || !current.Dirty || !current.Touched || current.Valid || !current.Validated || len(current.Issues) == 0 {
		t.Fatalf("current field metadata = %+v", *current)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"current_field"`) || !strings.Contains(text, `"current_field_id":"`+field.ID+`"`) {
		t.Fatalf("current field missing from automation JSON: %s", text)
	}
}

func TestRuntimeCurrentFrameValidationDoesNotPublishDuplicateFrame(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	before := runtime.AutomationSnapshot()
	if _, err := runtime.currentRuntimeTree(); err != nil {
		t.Fatal(err)
	}
	after := runtime.AutomationSnapshot()
	if after.FrameRevision != before.FrameRevision || after.RuntimeRevision != before.RuntimeRevision {
		t.Fatalf("current-frame validation published unexpectedly: before=%+v after=%+v", before, after)
	}
}

func TestRuntimeWaitForViewStableFramesCountsCoalescedPublications(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	got, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
		AfterFrameRevision:    initial.FrameRevision,
		AfterFrameSet:         true,
		Condition:             "published",
		StableFrames:          2,
		AllowAlreadySatisfied: true,
		Timeout:               20 * time.Millisecond,
	})
	if err != nil || got.FrameRevision < initial.FrameRevision+2 {
		t.Fatalf("coalesced stable wait = %+v err=%v initial=%+v", got, err, initial)
	}
}

func TestRuntimeWaitForViewStableFramesCountsOnlyPostBarrierPublications(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	if _, err := runtime.RuntimeTree(); err != nil { // frame 2, same geometry revision
		t.Fatal(err)
	}
	runtime.SetViewport(120, 80)
	if _, err := runtime.RuntimeTree(); err != nil { // frame 3, new geometry revision
		t.Fatal(err)
	}
	barrier := runtime.AutomationSnapshot()
	if _, err := runtime.RuntimeTree(); err != nil { // frame 4, only one frame after barrier
		t.Fatal(err)
	}
	got, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
		AfterFrameRevision:    barrier.FrameRevision,
		AfterFrameSet:         true,
		Condition:             "published",
		StableFrames:          2,
		AllowAlreadySatisfied: true,
		Timeout:               10 * time.Millisecond,
	})
	var timeout *WaitTimeoutError
	if !errors.As(err, &timeout) || got.FrameRevision != barrier.FrameRevision+1 {
		t.Fatalf("stable barrier was satisfied by pre-barrier publications: snapshot=%+v err=%v barrier=%+v", got, err, barrier)
	}
	if _, err := runtime.RuntimeTree(); err != nil { // frame 5, now two frames after barrier
		t.Fatal(err)
	}
	got, err = runtime.WaitForView(context.Background(), WaitForViewRequest{
		AfterFrameRevision:    barrier.FrameRevision,
		AfterFrameSet:         true,
		Condition:             "published",
		StableFrames:          2,
		AllowAlreadySatisfied: true,
		Timeout:               time.Second,
	})
	if err != nil || got.FrameRevision != barrier.FrameRevision+2 {
		t.Fatalf("stable barrier did not count post-barrier frames: snapshot=%+v err=%v barrier=%+v", got, err, barrier)
	}
}

func TestRuntimeMutationPublishesExactlyOneMatchingFrameForWait(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	initial := runtime.AutomationSnapshot()
	runtime.SetViewport(120, 80)
	mutated := runtime.AutomationSnapshot()
	if mutated.Agreement || mutated.RuntimeRevision == initial.RuntimeRevision {
		t.Fatalf("mutation unexpectedly published: %+v", mutated)
	}
	done := make(chan struct {
		snapshot AutomationSnapshot
		err      error
	}, 1)
	go func() {
		snapshot, err := runtime.WaitForView(context.Background(), WaitForViewRequest{
			AfterFrameRevision:   initial.FrameRevision,
			AfterFrameSet:        true,
			AfterRuntimeRevision: mutated.RuntimeRevision,
			AfterRuntimeSet:      true,
			Condition:            "idle",
			StableFrames:         1,
			Timeout:              time.Second,
		})
		done <- struct {
			snapshot AutomationSnapshot
			err      error
		}{snapshot, err}
	}()
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.snapshot.FrameRevision != initial.FrameRevision+1 || !result.snapshot.Agreement || !result.snapshot.Idle {
		t.Fatalf("mutation wait = %+v err=%v initial=%+v", result.snapshot, result.err, initial)
	}
}

func TestRuntimeWaitForViewRejectsNegativeStableFrames(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	if _, err := runtime.WaitForView(context.Background(), WaitForViewRequest{StableFrames: -1, Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "stable_frames") {
		t.Fatalf("negative stable_frames error = %v", err)
	}
}

func TestRuntimeCapturePublishesCurrentFrameBeforeRasterCapture(t *testing.T) {
	runtime := newAutomationTestRuntime(t)
	before := runtime.AutomationSnapshot()
	runtime.SetViewport(120, 80)
	_, _, captureErr := runtime.CapturePNG(1)
	if captureErr != nil && !strings.Contains(captureErr.Error(), "Metal") {
		t.Fatalf("capture error = %v", captureErr)
	}
	after := runtime.AutomationSnapshot()
	if after.FrameRevision != before.FrameRevision+1 || !after.Agreement || after.PublishedRuntimeRevision != after.RuntimeRevision {
		t.Fatalf("capture publication = %+v before=%+v err=%v", after, before, captureErr)
	}
	_, _, secondErr := runtime.CapturePNG(1)
	if secondErr != nil && !strings.Contains(secondErr.Error(), "Metal") {
		t.Fatalf("second capture error = %v", secondErr)
	}
	if repeated := runtime.AutomationSnapshot(); repeated.FrameRevision != after.FrameRevision {
		t.Fatalf("already-current capture republished: first=%+v repeated=%+v", after, repeated)
	}
}

func newAutomationTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	return runtime
}
