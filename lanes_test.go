package telegram

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestAnIdleLaneRetiresItsWorker(t *testing.T) {
	queues := newLanes(slog.New(slog.DiscardHandler))
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
	queues := newLanes(slog.New(slog.DiscardHandler))
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

func TestAFullLaneParksJobsInOrderWithoutBlocking(t *testing.T) {
	queues := newLanes(slog.New(slog.DiscardHandler))
	release := make(chan struct{})
	if !queues.push(t.Context(), "conv", func(context.Context) { <-release }) {
		t.Fatal("push refused")
	}
	const count = laneDepth + 10
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		for index := range count {
			last := index == count-1
			if !queues.push(t.Context(), "conv", func(context.Context) {
				mu.Lock()
				order = append(order, index)
				mu.Unlock()
				if last {
					close(done)
				}
			}) {
				t.Errorf("push %d refused", index)
				return
			}
		}
	}()
	select {
	case <-pushed:
	case <-time.After(waitLimit):
		t.Fatal("pushing into a full lane blocked")
	}
	close(release)

	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("the parked jobs never ran")
	}
	mu.Lock()
	defer mu.Unlock()
	for position, got := range order {
		if got != position {
			t.Fatalf("job %d ran at position %d, want the order kept", got, position)
		}
	}
}
