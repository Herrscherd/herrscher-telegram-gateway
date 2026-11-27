package telegram

import (
	"context"
	"sync"

	"github.com/Herrscherd/tgctl"
)

type origin struct {
	agent string
	api   bot
	self  tgctl.User
}

func (o origin) house() bool { return o.agent == "" }

type ownBot struct {
	client bot
	self   tgctl.User
}

type voiceBots struct {
	house bot
	self  tgctl.User

	mu      sync.Mutex
	own     map[string]ownBot
	signing map[string]chan struct{}
}

func newVoiceBots(house bot, self tgctl.User) *voiceBots {
	return &voiceBots{house: house, self: self, own: make(map[string]ownBot), signing: make(map[string]chan struct{})}
}

func (b *voiceBots) expect(agent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.signing[agent] = make(chan struct{})
}

func (b *voiceBots) admit(agent string, client bot, self tgctl.User) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settleLocked(agent)
	if b.ownsLocked(self.ID) {
		return false
	}
	b.own[agent] = ownBot{client: client, self: self}
	return true
}

func (b *voiceBots) refuse(agent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settleLocked(agent)
}

func (b *voiceBots) settleLocked(agent string) {
	ready, pending := b.signing[agent]
	if !pending {
		return
	}
	close(ready)
	delete(b.signing, agent)
}

func (b *voiceBots) ownsLocked(id int64) bool {
	if id == b.self.ID {
		return true
	}
	for _, bot := range b.own {
		if bot.self.ID == id {
			return true
		}
	}
	return false
}

func (b *voiceBots) awaitSignIn(ctx context.Context, agent string) error {
	b.mu.Lock()
	ready, pending := b.signing[agent]
	b.mu.Unlock()
	if !pending {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *voiceBots) ownOf(agent string) (ownBot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bot, ok := b.own[agent]
	return bot, ok
}

func (b *voiceBots) hasOwn(agent string) bool {
	_, ok := b.ownOf(agent)
	return ok
}

func (b *voiceBots) identities() []tgctl.User {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]tgctl.User, 0, len(b.own))
	for _, bot := range b.own {
		ids = append(ids, bot.self)
	}
	return ids
}

func (b *voiceBots) authoredByOwn(msg *tgctl.Message) bool {
	if msg.From == nil || msg.From.ID == 0 {
		return false
	}
	if msg.From.ID == b.self.ID {
		return true
	}
	for _, id := range b.identities() {
		if id.ID == msg.From.ID {
			return true
		}
	}
	return false
}

func (b *voiceBots) mentionsAgentBot(raw string, entities []tgctl.Entity) bool {
	return mentionedByAny(raw, entities, b.identities())
}

func (b *voiceBots) mentionsOtherAgentBot(raw string, entities []tgctl.Entity, self tgctl.User) bool {
	var others []tgctl.User
	for _, id := range b.identities() {
		if id.ID != self.ID {
			others = append(others, id)
		}
	}
	return mentionedByAny(raw, entities, others)
}

func mentionedByAny(raw string, entities []tgctl.Entity, ids []tgctl.User) bool {
	for _, id := range ids {
		if mentionedBy(raw, entities, id) {
			return true
		}
	}
	return false
}

func (b *voiceBots) speakerAt(conv address) speaker {
	if own, ok := b.ownOf(conv.agent); ok {
		return speaker{api: own.client}
	}
	return speaker{api: b.house, header: agentHeader(conv.agent)}
}

func (b *voiceBots) menuSpeakerAt(conv address) speaker {
	return speaker{api: b.house, header: agentHeader(conv.agent)}
}

type speaker struct {
	api    bot
	header formatted
}

func (s speaker) budget() int { return messageMaxUnits - s.header.units() }

func (s speaker) frame(part formatted) formatted {
	return part.prepend(s.header)
}

func (s speaker) message(conv address, replyTo int64, part formatted) tgctl.OutgoingMessage {
	framed := s.frame(part)
	out := tgctl.OutgoingMessage{ChatID: conv.chat, ThreadID: conv.thread, Text: framed.text, Entities: framed.entities, LinkPreview: noPreview()}
	if replyTo != 0 {
		out.ReplyParameters = &tgctl.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}
	return out
}

func agentHeader(agent string) formatted {
	if agent == "" {
		return formatted{}
	}
	var out formatter
	out.span(entityBold, agent, tgctl.Entity{})
	out.write("\n")
	return out.result()
}
