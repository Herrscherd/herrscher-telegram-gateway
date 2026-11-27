package telegram

import (
	"context"
	"log/slog"
	"sync"
)

type ackKey struct {
	chat    int64
	message int64
}

type ackEntry struct {
	api      bot
	open     int
	shown    bool
	failed   bool
	pending  []func()
	draining bool
}

func (e *ackEntry) enqueue(step func()) bool {
	e.pending = append(e.pending, step)
	if e.draining {
		return false
	}
	e.draining = true
	return true
}

type ackRegistry struct {
	log     *slog.Logger
	mu      sync.Mutex
	entries map[ackKey]*ackEntry
}

func newAckRegistry(log *slog.Logger) *ackRegistry {
	return &ackRegistry{log: log, entries: make(map[ackKey]*ackEntry)}
}

func (r *ackRegistry) open(ctx context.Context, key ackKey, api bot) {
	r.mu.Lock()
	entry := r.entryFor(key)
	entry.open++
	if entry.shown {
		r.mu.Unlock()
		return
	}
	entry.shown = true
	entry.api = api
	mustDrain := entry.enqueue(func() {
		if err := r.set(ctx, api, key, ackEmoji); err != nil {
			r.log.Warn("ack reaction failed", "err", err)
		}
	})
	r.mu.Unlock()
	if mustDrain {
		r.drain(key, entry)
	}
}

func (r *ackRegistry) close(ctx context.Context, key ackKey, emoji string) {
	r.mu.Lock()
	entry, found := r.entries[key]
	if !found {
		r.mu.Unlock()
		return
	}
	entry.open--
	entry.failed = entry.failed || emoji == failedEmoji
	if entry.open > 0 {
		r.mu.Unlock()
		return
	}
	shown, api, outcome := entry.shown, entry.api, ""
	if entry.failed {
		outcome = failedEmoji
	}
	entry.shown, entry.failed = false, false
	if !shown {
		r.retireIfIdle(key, entry)
		r.mu.Unlock()
		return
	}
	mustDrain := entry.enqueue(func() {
		if err := r.set(ctx, api, key, outcome); err != nil {
			r.log.Warn("outcome reaction failed", "err", err)
		}
	})
	r.mu.Unlock()
	if mustDrain {
		r.drain(key, entry)
	}
}

func (r *ackRegistry) entryFor(key ackKey) *ackEntry {
	entry, found := r.entries[key]
	if !found {
		entry = &ackEntry{}
		r.entries[key] = entry
	}
	return entry
}

func (r *ackRegistry) drain(key ackKey, entry *ackEntry) {
	for {
		r.mu.Lock()
		if len(entry.pending) == 0 {
			entry.draining = false
			r.retireIfIdle(key, entry)
			r.mu.Unlock()
			return
		}
		step := entry.pending[0]
		entry.pending = entry.pending[1:]
		r.mu.Unlock()
		step()
	}
}

func (r *ackRegistry) retireIfIdle(key ackKey, entry *ackEntry) {
	if entry.open == 0 && !entry.draining && len(entry.pending) == 0 {
		delete(r.entries, key)
	}
}

func (r *ackRegistry) set(ctx context.Context, api bot, key ackKey, emoji string) error {
	return api.SetMessageReaction(ctx, key.chat, key.message, emoji)
}
