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
	api    bot
	open   int
	shown  bool
	failed bool
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
	defer r.mu.Unlock()
	entry, found := r.entries[key]
	if !found {
		entry = &ackEntry{}
		r.entries[key] = entry
	}
	entry.open++
	if entry.shown {
		return
	}
	entry.api = api
	if err := api.SetMessageReaction(ctx, key.chat, key.message, ackEmoji); err != nil {
		r.log.Warn("ack reaction failed", "err", err)
		return
	}
	entry.shown = true
}

func (r *ackRegistry) close(ctx context.Context, key ackKey, emoji string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[key]
	if !found {
		return
	}
	entry.open--
	entry.failed = entry.failed || emoji == failedEmoji
	if entry.open > 0 {
		return
	}
	delete(r.entries, key)
	if !entry.shown {
		return
	}
	if entry.failed {
		emoji = failedEmoji
	}
	if err := entry.api.SetMessageReaction(ctx, key.chat, key.message, emoji); err != nil {
		r.log.Warn("outcome reaction failed", "err", err)
	}
}
