package telegram

import (
	"context"
	"testing"
	"time"
)

func TestAnIdleLaneRetiresItsWorker(t *testing.T) {
	queues := newLanes()
	queues.idle = 10 * time.Millisecond
	ran := make(chan struct{})

	if !queues.push(t.Context(), "conv", func(context.Context) { close(ran) }) {
		t.Fatal("push refused")
	}
	<-ran
	retired := make(chan struct{})
	go func() {
		queues.wait()
		close(retired)
	}()

	select {
	case <-retired:
	case <-time.After(waitLimit):
		t.Fatal("the idle worker never returned")
	}
	queues.mu.Lock()
	defer queues.mu.Unlock()
	if len(queues.queues) != 0 {
		t.Fatalf("%d lanes left", len(queues.queues))
	}
}

func TestALaneWithASenderWaitingDoesNotRetire(t *testing.T) {
	queues := newLanes()
	queues.idle = time.Millisecond
	target, open := queues.acquire(t.Context(), "conv")
	if !open {
		t.Fatal("acquire refused")
	}
	idle := time.NewTimer(20 * queues.idle)
	<-idle.C
	ran := make(chan struct{})
	target.jobs <- func(context.Context) { close(ran) }
	queues.release(target)

	select {
	case <-ran:
	case <-time.After(waitLimit):
		t.Fatal("the job sent by a waiting sender never ran")
	}
}
