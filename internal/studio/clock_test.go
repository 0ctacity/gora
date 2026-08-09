package studio

import (
	"testing"
	"time"

	"gora/internal/interaction"
)

func TestViewClockOrdersBoundedInteractionTimersAndCleansUp(t *testing.T) {
	runtime := newRuntime("", "")
	if _, err := runtime.SetViewClock("frozen"); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	base := runtime.clockTimeMS
	runtime.timerQueue = nil
	runtime.nextTimer = nil
	runtime.timerDispatchLog = nil
	runtime.timerOrder = 0
	runtime.timerOrder++
	runtime.enqueueTimerLocked(viewTimer{dueMS: base + 20, source: "same-late", sourceOrder: runtime.timerOrder})
	runtime.timerOrder++
	runtime.enqueueTimerLocked(viewTimer{dueMS: base + 10, source: "early", sourceOrder: runtime.timerOrder})
	runtime.timerOrder++
	runtime.enqueueTimerLocked(viewTimer{dueMS: base + 20, source: "same-early", sourceOrder: runtime.timerOrder})
	runtime.mu.Unlock()
	if _, err := runtime.AdvanceViewClock(20, false); err != nil {
		t.Fatal(err)
	}
	runtime.mu.RLock()
	got := append([]string(nil), runtime.timerDispatchLog...)
	runtime.mu.RUnlock()
	if len(got) != 3 || got[0] != "early" || got[1] != "same-late" || got[2] != "same-early" {
		t.Fatalf("timer order=%v", got)
	}
	for i := 0; i < 100; i++ {
		runtime.mu.Lock()
		runtime.timerOrder++
		runtime.enqueueTimerLocked(viewTimer{dueMS: runtime.clockTimeMS + 1, source: "cycle", sourceOrder: runtime.timerOrder})
		runtime.mu.Unlock()
		if _, err := runtime.AdvanceViewClock(1, false); err != nil {
			t.Fatal(err)
		}
	}
	runtime.mu.RLock()
	if len(runtime.timerQueue) > 1 || len(runtime.timerDispatchLog) > 64 {
		runtime.mu.RUnlock()
		t.Fatalf("timer retention queue=%d log=%d", len(runtime.timerQueue), len(runtime.timerDispatchLog))
	}
	runtime.mu.RUnlock()
	runtime.Close()
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if len(runtime.timerQueue) != 0 || runtime.nextTimer != nil {
		t.Fatalf("close retained timers queue=%d next=%+v", len(runtime.timerQueue), runtime.nextTimer)
	}
}

func TestViewClockRunUntilIdleDrainsDueTimersWithoutGeometryRebuild(t *testing.T) {
	runtime := newRuntime("", "")
	if _, err := runtime.SetViewClock("frozen"); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.timerQueue = nil
	runtime.nextTimer = nil
	runtime.timerOrder++
	runtime.enqueueTimerLocked(viewTimer{dueMS: runtime.clockTimeMS, source: "due", sourceOrder: runtime.timerOrder})
	runtime.mu.Unlock()
	geometry := runtime.Snapshot().GeometryRevision
	if _, err := runtime.RunUntilIdle(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().GeometryRevision; got != geometry {
		t.Fatalf("timer transient rebuilt geometry %d -> %d", geometry, got)
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if len(runtime.timerDispatchLog) == 0 || runtime.timerDispatchLog[len(runtime.timerDispatchLog)-1] != "due" {
		t.Fatalf("run-until-idle log=%v", runtime.timerDispatchLog)
	}
}

func TestViewClockTimerQueueCapsThousandEnqueuesAndKeepsEarliest(t *testing.T) {
	runtime := newRuntime("", "")
	runtime.mu.Lock()
	runtime.timerQueue = nil
	runtime.nextTimer = nil
	runtime.timerOrder = 0
	base := runtime.clockTimeMS
	for i := 0; i < 5000; i++ {
		runtime.timerOrder++
		runtime.enqueueTimerLocked(viewTimer{dueMS: base + int64(i%10), source: "timer", sourceOrder: runtime.timerOrder})
	}
	if len(runtime.timerQueue) != maxInteractionTimers {
		runtime.mu.Unlock()
		t.Fatalf("timer queue len=%d, want %d", len(runtime.timerQueue), maxInteractionTimers)
	}
	if runtime.nextTimer == nil || runtime.nextTimer.dueMS != base || runtime.nextTimer.sourceOrder != 1 {
		got := runtime.nextTimer
		runtime.mu.Unlock()
		t.Fatalf("earliest next timer=%+v", got)
	}
	runtime.mu.Unlock()
}

func TestValidateEditBatchDoesNotReenterRuntimeReadLock(t *testing.T) {
	runtime := newRuntime("", "")
	done := make(chan error, 1)
	go func() { done <- runtime.ValidateEditBatch(nil) }()
	for i := 0; i < 100; i++ {
		runtime.PublishRouterSnapshot(interaction.RouterSnapshot{})
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ValidateEditBatch appears deadlocked")
	}
}
