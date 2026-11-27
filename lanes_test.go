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

func TestALaneWithAQueuedJobDoesNotRetireBeforeItRunsIt(t *testing.T) {
	queues := newLanes()
	queues.idle = time.Millisecond
	release := make(chan struct{})
	ran := make(chan struct{})
	if !queues.push(t.Context(), "conv", func(context.Context) { <-release }) {
		t.Fatal("push refused")
	}
	if !queues.push(t.Context(), "conv", func(context.Context) { close(ran) }) {
		t.Fatal("push refused")
	}
	close(release)

	select {
	case <-ran:
	case <-time.After(waitLimit):
		t.Fatal("the job queued behind a running job never ran")
	}
}
