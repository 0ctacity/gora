package automation

import (
	"testing"
)

func TestTraceRecorderIsBoundedAndGenerational(t *testing.T) {
	recorder := NewTraceRecorder()
	if err := recorder.Configure(true, 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		recorder.Record(TraceEntry{Stage: "accepted", EventIndex: i})
	}
	snapshot := recorder.Snapshot()
	if !snapshot.Enabled || snapshot.Capacity != 2 || len(snapshot.Entries) != 2 || snapshot.Entries[0].EventIndex != 3 || snapshot.Entries[1].EventIndex != 4 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	generation := snapshot.Generation
	recorder.Clear()
	if cleared := recorder.Snapshot(); cleared.Generation != generation || len(cleared.Entries) != 0 {
		t.Fatalf("clear=%+v", cleared)
	}
	if err := recorder.Configure(true, 1); err != nil {
		t.Fatal(err)
	}
	if next := recorder.Snapshot(); next.Generation != generation+1 || next.Capacity != 1 {
		t.Fatalf("rollover=%+v", next)
	}
}

func TestTraceRecorderDisabledDoesNotRetainEntries(t *testing.T) {
	recorder := NewTraceRecorder()
	if err := recorder.Configure(false, 512); err != nil {
		t.Fatal(err)
	}
	recorder.Record(TraceEntry{Stage: "accepted"})
	if snapshot := recorder.Snapshot(); snapshot.Enabled || len(snapshot.Entries) != 0 {
		t.Fatalf("disabled snapshot=%+v", snapshot)
	}
}

func TestTraceRecorderSubscriptionCoalescesNewestRevisionAndCloses(t *testing.T) {
	recorder := NewTraceRecorder()
	updates, unsubscribe := recorder.Subscribe()
	if err := recorder.Configure(true, 4); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		recorder.Record(TraceEntry{Stage: "accepted", EventIndex: i})
	}
	latest := recorder.Snapshot().Revision
	select {
	case got := <-updates:
		if got != latest {
			t.Fatalf("coalesced revision=%d, latest=%d", got, latest)
		}
	default:
		t.Fatal("trace subscription did not notify")
	}
	unsubscribe()
	if latest == 0 {
		t.Fatal("trace revision did not advance")
	}
}
