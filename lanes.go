package telegram

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"
)

const (
	updatesPerPoll = 100
	laneDepth      = updatesPerPoll
	laneBacklog    = 10 * laneDepth
	laneIdleAfter  = 5 * time.Minute
	flushRoom      = math.MaxInt
)

type lifetime func() context.Context

type job func(ctx context.Context)

type lane struct {
	jobs   chan job
	parked []job
}

type lanes struct {
	mu      sync.Mutex
	queues  map[string]*lane
	stopped bool
	idle    time.Duration
	log     *slog.Logger
	workers sync.WaitGroup
}

func newLanes(log *slog.Logger) *lanes {
	return &lanes{queues: make(map[string]*lane), idle: laneIdleAfter, log: log}
}

func (l *lanes) laneLocked(ctx context.Context, key string) *lane {
	found, ok := l.queues[key]
	if !ok {
		found = &lane{jobs: make(chan job, laneDepth)}
		l.queues[key] = found
		l.workers.Go(func() { l.work(ctx, key, found) })
	}
	return found
}

func (l *lanes) work(ctx context.Context, key string, target *lane) {
	timer := time.NewTimer(l.idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-target.jobs:
			l.promote(target)
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

func (l *lanes) promote(target *lane) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(target.parked) > 0 {
		select {
		case target.jobs <- target.parked[0]:
			target.parked = slices.Delete(target.parked, 0, 1)
		default:
			return
		}
	}
}

func (l *lanes) retire(key string, target *lane) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(target.jobs) > 0 || len(target.parked) > 0 {
		return false
	}
	delete(l.queues, key)
	return true
}

func (l *lanes) enqueue(ctx context.Context, key string, next job, room int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return false
	}
	target := l.laneLocked(ctx, key)
	if len(target.parked) == 0 {
		select {
		case target.jobs <- next:
			return true
		default:
		}
	}
	if len(target.parked) >= room {
		return false
	}
	target.parked = append(target.parked, next)
	return true
}

func (l *lanes) admit(ctx context.Context, key string, next job, room int) bool {
	if l.enqueue(ctx, key, next, room) {
		return true
	}
	l.log.Warn("work dropped, the gateway is stopping or the conversation has too much waiting", "lane", key)
	return false
}

func (l *lanes) offer(ctx context.Context, key string, next job) bool {
	return l.enqueue(ctx, key, next, 0)
}

func (l *lanes) push(ctx context.Context, key string, next job) bool {
	return l.admit(ctx, key, next, laneBacklog)
}

func (l *lanes) later(ctx context.Context, key string, delay time.Duration, next job) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return false
	}
	l.workers.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			l.admit(ctx, key, next, flushRoom)
		}
	})
	return true
}

func (l *lanes) stop() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
}

func (l *lanes) wait() { l.workers.Wait() }
