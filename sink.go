package telegram

import (
	"context"
	"log/slog"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	draftInterval   = time.Second
	editInterval    = 3 * time.Second
	retryWaitMax    = 30 * time.Second
	ackEmoji        = "👀"
	failedEmoji     = "💔"
	eventHuman      = "human"
	eventChunk      = "chunk"
	eventReset      = "reset"
	eventAbandoned  = "abandoned"
	eventReply      = "reply"
	turnFailedKey   = "telegram.turn.failed"
	turnStoppedKey  = "telegram.turn.interrupted"
	turnEmptyKey    = "telegram.turn.empty"
	turnDroppedKey  = "telegram.turn.abandoned"
	paragraphBreak  = "\n\n"
	noReplyToNobody = 0
)

type turnView struct {
	text      string
	draftID   int64
	live      int64
	shown     string
	nextFlush time.Time
}

type sink struct {
	conv  chatRef
	api   bot
	msgs  messages
	log   *slog.Logger
	now   func() time.Time
	user  int64
	acked int64
	turn  *turnView
	turns int64
}

func (s *sink) apply(ctx context.Context, event contracts.Event) {
	switch event.T {
	case eventHuman:
		s.begin(ctx)
	case eventChunk:
		s.chunk(ctx, event.Text)
	case eventReset:
		if s.turn != nil {
			s.turn.text = ""
		}
	case eventAbandoned:
		s.abandon(ctx)
	case eventReply:
		if event.Done {
			s.finish(ctx, event)
		}
	}
}

func (s *sink) noteUser(ctx context.Context, messageID int64) {
	s.user = messageID
	if s.turn != nil && s.acked == 0 {
		s.ack(ctx)
	}
}

func (s *sink) begin(ctx context.Context) {
	s.turns++
	s.turn = &turnView{draftID: s.turns}
	s.ack(ctx)
}

func (s *sink) ack(ctx context.Context) {
	if s.user == 0 {
		return
	}
	if err := s.api.setMessageReaction(ctx, s.conv.chat, s.user, ackEmoji); err != nil {
		s.log.Warn("ack reaction failed", "err", err)
		return
	}
	s.acked = s.user
}

func (s *sink) swapAck(ctx context.Context, emoji string) {
	if s.acked == 0 {
		return
	}
	if err := s.api.setMessageReaction(ctx, s.conv.chat, s.acked, emoji); err != nil {
		s.log.Warn("outcome reaction failed", "err", err)
	}
	s.acked = 0
}

func (s *sink) chunk(ctx context.Context, text string) {
	if s.turn == nil {
		s.begin(ctx)
	}
	s.turn.text += text
	if s.now().Before(s.turn.nextFlush) {
		return
	}
	if s.conv.private() {
		s.draft(ctx)
		return
	}
	s.edit(ctx)
}

func (s *sink) draft(ctx context.Context) {
	view := formatMarkdown(s.turn.text).tail(messageMaxUnits)
	err := s.api.sendMessageDraft(ctx, draft{
		ChatID: s.conv.chat, ThreadID: s.conv.thread, DraftID: s.turn.draftID,
		Text: view.text, Entities: view.entities, CanStop: true,
	})
	s.turn.nextFlush = s.after(err, draftInterval)
}

func (s *sink) edit(ctx context.Context) {
	view := plain(s.turn.text).tail(messageMaxUnits)
	if view.text == "" || view.text == s.turn.shown {
		return
	}
	var err error
	if s.turn.live == 0 {
		var sent message
		sent, err = s.api.sendMessage(ctx, s.outgoing(view, noReplyToNobody))
		s.turn.live = sent.MessageID
	} else {
		err = s.api.editMessageText(ctx, textEdit{ChatID: s.conv.chat, MessageID: s.turn.live, Text: view.text, LinkPreview: noPreview()})
	}
	if err == nil {
		s.turn.shown = view.text
	}
	s.turn.nextFlush = s.after(err, editInterval)
}

func (s *sink) after(err error, interval time.Duration) time.Time {
	if err == nil {
		return s.now().Add(interval)
	}
	if wait, limited := retryAfter(err); limited {
		return s.now().Add(wait)
	}
	s.log.Warn("live render failed", "err", err)
	return s.now().Add(interval)
}

func (s *sink) abandon(ctx context.Context) {
	if s.turn != nil && s.turn.live != 0 {
		text := s.msgs.say(turnDroppedKey, nil)
		if err := s.api.editMessageText(ctx, textEdit{ChatID: s.conv.chat, MessageID: s.turn.live, Text: text}); err != nil {
			s.log.Warn("abandoned render failed", "err", err)
		}
	}
	s.swapAck(ctx, "")
	s.turn = nil
}

func (s *sink) finish(ctx context.Context, event contracts.Event) {
	parts := s.answer(event).split(messageMaxUnits)
	live := int64(0)
	if s.turn != nil {
		live = s.turn.live
	}
	for index, part := range parts {
		if index == 0 && live != 0 {
			s.replace(ctx, live, part)
			continue
		}
		replyTo := int64(noReplyToNobody)
		if index == 0 {
			replyTo = s.user
		}
		s.send(ctx, s.outgoing(part, replyTo))
	}
	emoji := ""
	if event.Outcome == contracts.TurnFailed {
		emoji = failedEmoji
	}
	s.swapAck(ctx, emoji)
	s.turn = nil
}

func (s *sink) answer(event contracts.Event) formatted {
	closing := ""
	switch event.Outcome {
	case contracts.TurnFailed:
		closing = s.msgs.say(turnFailedKey, nil)
	case contracts.TurnInterrupted:
		closing = s.msgs.say(turnStoppedKey, nil)
	}
	body := event.Text
	switch {
	case body == "" && closing == "":
		body = s.msgs.say(turnEmptyKey, nil)
	case body == "":
		body = closing
	case closing != "":
		body += paragraphBreak + closing
	}
	return formatMarkdown(body)
}

func (s *sink) replace(ctx context.Context, live int64, part formatted) {
	edit := textEdit{ChatID: s.conv.chat, MessageID: live, Text: part.text, Entities: part.entities, LinkPreview: noPreview()}
	err := s.api.editMessageText(ctx, edit)
	if wait, limited := retryAfter(err); limited && pause(ctx, wait) {
		err = s.api.editMessageText(ctx, edit)
	}
	if err != nil {
		s.log.Warn("final edit failed, the answer is sent anew", "err", err)
		s.send(ctx, s.outgoing(part, noReplyToNobody))
	}
}

func (s *sink) send(ctx context.Context, out outgoing) {
	_, err := s.api.sendMessage(ctx, out)
	if wait, limited := retryAfter(err); limited && pause(ctx, wait) {
		_, err = s.api.sendMessage(ctx, out)
	}
	if err != nil {
		s.log.Warn("answer part not sent", "err", err)
	}
}

func (s *sink) outgoing(part formatted, replyTo int64) outgoing {
	out := outgoing{ChatID: s.conv.chat, ThreadID: s.conv.thread, Text: part.text, Entities: part.entities, LinkPreview: noPreview()}
	if replyTo != 0 {
		out.ReplyParameters = &replyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}
	return out
}

func noPreview() *linkPreview { return &linkPreview{IsDisabled: true} }

func pause(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(min(wait, retryWaitMax))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
