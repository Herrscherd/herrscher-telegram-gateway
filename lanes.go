package telegram

import (
	"context"
	"sync"
	"time"
)

const (
	updatesPerPoll = 100
	laneDepth      = updatesPerPoll
	laneIdleAfter  = 5 * time.Minute
)

type lifetime func() context.Context

type job func(ctx context.Context)

type lane struct {
	jobs    chan job
	senders int
}

type lanes struct {
	mu      sync.Mutex
	queues  map[string]*lane
	stopped bool
	idle    time.Duration
	workers sync.WaitGroup
}

func newLanes() *lanes {
	return &lanes{queues: make(map[string]*lane), idle: laneIdleAfter}
}

func (l *lanes) acquire(ctx context.Context, key string) (*lane, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil, false
	}
	found, ok := l.queues[key]
	if !ok {
		found = &lane{jobs: make(chan job, laneDepth)}
		l.queues[key] = found
		l.workers.Go(func() { l.work(ctx, key, found) })
	}
	found.senders++
	return found, true
}

func (l *lanes) release(target *lane) {
	l.mu.Lock()
	target.senders--
	l.mu.Unlock()
}

func (l *lanes) work(ctx context.Context, key string, target *lane) {
	timer := time.NewTimer(l.idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-target.jobs:
			next(ctx)
			timer.Reset(l.idle)
		case <-timer.C:
			if l.retire(key, target) {
				return
			}
			timer.Reset(l.idle)
		}
	}
}

func (l *lanes) retire(key string, target *lane) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if target.senders > 0 || len(target.jobs) > 0 {
		return false
	}
	delete(l.queues, key)
	return true
}

func (l *lanes) offer(ctx context.Context, key string, next job) bool {
	target, open := l.acquire(ctx, key)
	if !open {
		return false
	}
	defer l.release(target)
	select {
	case target.jobs <- next:
		return true
	default:
		return false
	}
}

func (l *lanes) push(ctx context.Context, key string, next job) bool {
	target, open := l.acquire(ctx, key)
	if !open {
		return false
	}
	defer l.release(target)
	select {
	case target.jobs <- next:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *lanes) stop() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
}

func (l *lanes) wait() { l.workers.Wait() }
