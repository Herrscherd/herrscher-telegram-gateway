package telegram

import (
	"context"
	"sync"
)

const laneDepth = 256

type lifetime func() context.Context

type job func(ctx context.Context)

type lanes struct {
	mu      sync.Mutex
	queues  map[string]chan job
	stopped bool
	workers sync.WaitGroup
}

func newLanes() *lanes {
	return &lanes{queues: make(map[string]chan job)}
}

func (l *lanes) queue(ctx context.Context, key string) (chan job, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil, false
	}
	queue, found := l.queues[key]
	if !found {
		queue = make(chan job, laneDepth)
		l.queues[key] = queue
		l.workers.Go(func() { work(ctx, queue) })
	}
	return queue, true
}

func work(ctx context.Context, queue chan job) {
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-queue:
			next(ctx)
		}
	}
}

func (l *lanes) offer(ctx context.Context, key string, next job) bool {
	queue, open := l.queue(ctx, key)
	if !open {
		return false
	}
	select {
	case queue <- next:
		return true
	default:
		return false
	}
}

func (l *lanes) push(ctx context.Context, key string, next job) bool {
	queue, open := l.queue(ctx, key)
	if !open {
		return false
	}
	select {
	case queue <- next:
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
