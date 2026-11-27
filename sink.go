package telegram

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
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

type notedPrompt struct {
	id     int64
	author string
	text   string
}

func (n notedPrompt) asks(author, text string) bool {
	return n.author == author && n.text == text
}

type turnView struct {
	text      string
	draftID   int64
	asked     notedPrompt
	prompt    int64
	live      int64
	shown     string
	nextFlush time.Time
}

type sink struct {
	conv    address
	voices  *voiceBots
	drafts  *atomic.Int64
	msgs    messages
	log     *slog.Logger
	now     func() time.Time
	prompts []notedPrompt
	acked   int64
	turn    *turnView
}

func (s *sink) speaker() speaker { return s.voices.speakerAt(s.conv) }

func (s *sink) apply(ctx context.Context, event contracts.Event) {
	switch event.T {
	case eventHuman:
		s.begin(ctx, event.Who, event.Text)
	case eventChunk:
		s.chunk(ctx, event.Text)
	case eventReset:
		if s.turn != nil {
			s.turn.text = ""
		}
	case eventAbandoned:
		s.dropAbandoned(event)
		s.abandon(ctx)
	case eventReply:
		if event.Done {
			s.finish(ctx, event)
		}
	}
}

func (s *sink) noteUser(ctx context.Context, noted notedPrompt) {
	if s.turn != nil && s.turn.prompt == 0 && noted.asks(s.turn.asked.author, s.turn.asked.text) {
		s.turn.prompt = noted.id
		s.ack(ctx)
		return
	}
	if len(s.prompts) == laneDepth {
		s.prompts = s.prompts[1:]
	}
	s.prompts = append(s.prompts, noted)
}

func (s *sink) dropAbandoned(event contracts.Event) {
	if s.turn != nil || len(s.prompts) == 0 {
		return
	}
	if event.Who != "" || event.Text != "" {
		s.claim(event.Who, event.Text)
		return
	}
	s.prompts = s.prompts[1:]
}

func (s *sink) claim(author, text string) int64 {
	for index, noted := range s.prompts {
		if noted.asks(author, text) {
			s.prompts = s.prompts[index+1:]
			return noted.id
		}
	}
	return 0
}

func (s *sink) begin(ctx context.Context, author, text string) {
	s.turn = &turnView{draftID: s.drafts.Add(1), asked: notedPrompt{author: author, text: text}, prompt: s.claim(author, text)}
	s.ack(ctx)
}

func (s *sink) ack(ctx context.Context) {
	if s.turn.prompt == 0 {
		return
	}
	if err := s.speaker().api.SetMessageReaction(ctx, s.conv.chat, s.turn.prompt, ackEmoji); err != nil {
		s.log.Warn("ack reaction failed", "err", err)
		return
	}
	s.acked = s.turn.prompt
}

func (s *sink) stoppedDraft(draftID int64) bool {
	return s.turn != nil && s.conv.private() && s.turn.draftID == draftID
}

func (s *sink) swapAck(ctx context.Context, emoji string) {
	if s.acked == 0 {
		return
	}
	if err := s.speaker().api.SetMessageReaction(ctx, s.conv.chat, s.acked, emoji); err != nil {
		s.log.Warn("outcome reaction failed", "err", err)
	}
	s.acked = 0
}

func (s *sink) chunk(ctx context.Context, text string) {
	if s.turn == nil {
		s.begin(ctx, "", "")
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
	view := s.speaker().frame(formatMarkdown(s.turn.text).tail(s.speaker().budget()))
	err := s.speaker().api.SendMessageDraft(ctx, tgctl.MessageDraft{
		ChatID: s.conv.chat, ThreadID: s.conv.thread, DraftID: s.turn.draftID,
		Text: view.text, Entities: view.entities, CanStop: true,
	})
	s.turn.nextFlush = s.after(err, draftInterval)
}

func (s *sink) edit(ctx context.Context) {
	view := plain(s.turn.text).tail(s.speaker().budget())
	if view.text == "" || view.text == s.turn.shown {
		return
	}
	var err error
	if s.turn.live == 0 {
		var sent tgctl.Message
		sent, err = s.speaker().api.SendMessage(ctx, s.outgoing(view, noReplyToNobody))
		s.turn.live = sent.MessageID
	} else {
		framed := s.speaker().frame(view)
		err = s.speaker().api.EditMessageText(ctx, tgctl.EditMessageText{ChatID: s.conv.chat, MessageID: s.turn.live, Text: framed.text, Entities: framed.entities, LinkPreview: noPreview()})
	}
	if err == nil || errors.Is(err, tgctl.ErrNotModified) {
		s.turn.shown = view.text
		err = nil
	}
	s.turn.nextFlush = s.after(err, editInterval)
}

func (s *sink) after(err error, interval time.Duration) time.Time {
	if err == nil {
		return s.now().Add(interval)
	}
	if wait, limited := tgctl.RetryAfter(err); limited {
		return s.now().Add(wait)
	}
	s.log.Warn("live render failed", "err", err)
	return s.now().Add(interval)
}

func (s *sink) abandon(ctx context.Context) {
	if s.turn != nil && s.turn.live != 0 {
		text := s.speaker().frame(plain(s.msgs.say(turnDroppedKey, nil)))
		if err := s.speaker().api.EditMessageText(ctx, tgctl.EditMessageText{ChatID: s.conv.chat, MessageID: s.turn.live, Text: text.text, Entities: text.entities}); err != nil {
			s.log.Warn("abandoned render failed", "err", err)
		}
	}
	s.swapAck(ctx, "")
	s.turn = nil
}

func (s *sink) finish(ctx context.Context, event contracts.Event) {
	parts := s.answer(event).split(s.speaker().budget())
	shown := turnView{}
	if s.turn != nil {
		shown = *s.turn
	}
	for index, part := range parts {
		if index == 0 && shown.live != 0 {
			s.replace(ctx, shown, part)
			continue
		}
		replyTo := int64(noReplyToNobody)
		if index == 0 {
			replyTo = shown.prompt
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

func (s *sink) replace(ctx context.Context, shown turnView, part formatted) {
	if part.text == shown.shown && len(part.entities) == 0 {
		return
	}
	framed := s.speaker().frame(part)
	edit := tgctl.EditMessageText{ChatID: s.conv.chat, MessageID: shown.live, Text: framed.text, Entities: framed.entities, LinkPreview: noPreview()}
	err := s.speaker().api.EditMessageText(ctx, edit)
	if wait, limited := tgctl.RetryAfter(err); limited && pause(ctx, wait) {
		err = s.speaker().api.EditMessageText(ctx, edit)
	}
	if err != nil && !errors.Is(err, tgctl.ErrNotModified) {
		s.log.Warn("final edit failed, the answer is sent anew", "err", err)
		s.send(ctx, s.outgoing(part, noReplyToNobody))
	}
}

func (s *sink) send(ctx context.Context, out tgctl.OutgoingMessage) {
	_, err := s.speaker().api.SendMessage(ctx, out)
	if wait, limited := tgctl.RetryAfter(err); limited && pause(ctx, wait) {
		_, err = s.speaker().api.SendMessage(ctx, out)
	}
	if err != nil {
		s.log.Warn("answer part not sent", "err", err)
	}
}

func (s *sink) outgoing(part formatted, replyTo int64) tgctl.OutgoingMessage {
	return s.speaker().message(s.conv, replyTo, part)
}

func noPreview() *tgctl.LinkPreviewOptions { return &tgctl.LinkPreviewOptions{IsDisabled: true} }

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
