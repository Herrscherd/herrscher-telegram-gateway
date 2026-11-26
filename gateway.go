package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const (
	pollBackoffFloor   = time.Second
	pollBackoffCeiling = time.Minute
	backoffGrowth      = 2
	inboundLane        = "in/"
	outboundLane       = "out/"
)

var (
	errNoChoices   = errors.New("telegram menu: no choices")
	errUnsupported = errors.New("telegram: reaction not offered by telegram")
	errWebhookSet  = errors.New("telegram: a webhook is set on this bot, so updates cannot be polled; remove it with deleteWebhook")
)

var (
	_ contracts.Gateway                = (*Gateway)(nil)
	_ contracts.GatewayCloser          = (*Gateway)(nil)
	_ contracts.NoticeSink             = (*Gateway)(nil)
	_ contracts.EventSink              = (*Gateway)(nil)
	_ contracts.RoutedEventSink        = (*Gateway)(nil)
	_ contracts.SessionControlReceiver = (*Gateway)(nil)
	_ contracts.Prober                 = (*Gateway)(nil)
)

// Gateway adapts the Telegram Bot API to the gateway ports. It starts receiving
// updates when the host binds its session control, and stops on Close.
type Gateway struct {
	api    bot
	msgs   messages
	log    *slog.Logger
	menus  *menuTable
	router *router
	lanes  *lanes
	life   lifetime
	cancel context.CancelFunc
	now    func() time.Time

	mu      sync.Mutex
	ctrl    contracts.SessionControl
	sinks   map[string]*sink
	polling bool
	closed  bool
	fault   error
	poller  sync.WaitGroup
}

func newGateway(ctx context.Context, api bot, set settings, self botIdentity, msgs messages, log *slog.Logger) *Gateway {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	life := func() context.Context { return run }
	gateway := &Gateway{
		api: api, msgs: msgs, log: log, menus: newMenuTable(),
		lanes: newLanes(), life: life, cancel: cancel, now: time.Now,
		sinks: make(map[string]*sink),
	}
	gateway.router = &router{
		api: api, set: set, self: self, msgs: msgs, log: log, menus: gateway.menus,
		files:    stager{api: api, root: stagingRoot(), log: log},
		ctrl:     gateway.control,
		noteUser: gateway.noteUser,
		pending:  make(map[string]pendingBind),
	}
	return gateway
}

// Manifest describes the gateway to the host. It hands over no https
// attachment: every file is staged on local disk first.
func (g *Gateway) Manifest() contracts.Manifest {
	return contracts.Manifest{
		Kind:         gatewayKind,
		Category:     contracts.CategoryGateway,
		Status:       contracts.StatusExperimental,
		Capabilities: contracts.Capabilities{Reactions: true, SelectMenus: true, Replies: true},
	}
}

// BindSessionControl receives the daemon's session controller and starts the
// long polling loop. Only the first binding starts it, and none after Close.
func (g *Gateway) BindSessionControl(ctrl contracts.SessionControl) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.polling {
		return
	}
	g.ctrl = ctrl
	g.polling = true
	g.poller.Go(func() { g.poll(g.life()) })
}

func (g *Gateway) control() contracts.SessionControl {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ctrl
}

// Close stops the polling loop and the conversation workers and waits for them,
// bounded by ctx. Once closed, the gateway never polls again.
func (g *Gateway) Close(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.lanes.stop()
	g.cancel()
	done := make(chan struct{})
	go func() {
		g.poller.Wait()
		g.lanes.wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram gateway close: %w", ctx.Err())
	}
}

func (g *Gateway) poll(ctx context.Context) {
	offset := int64(0)
	backoff := pollBackoffFloor
	for ctx.Err() == nil {
		updates, err := g.api.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait, limited := retryAfter(err)
			if !limited {
				wait = backoff
				backoff = min(backoff*backoffGrowth, pollBackoffCeiling)
			}
			g.notePollFailure(err, wait)
			pause(ctx, wait)
			continue
		}
		g.setFault(nil)
		backoff = pollBackoffFloor
		for _, next := range updates {
			offset = max(offset, next.UpdateID+1)
			g.dispatch(ctx, next)
		}
	}
}

func (g *Gateway) notePollFailure(err error, wait time.Duration) {
	if failure, ok := errors.AsType[*apiError](err); ok && failure.Code == http.StatusConflict {
		g.log.Error("telegram updates refused", "err", errWebhookSet, "retry_in", wait)
		g.setFault(errWebhookSet)
		return
	}
	g.log.Warn("telegram updates failed", "err", err, "retry_in", wait)
}

func (g *Gateway) setFault(err error) {
	g.mu.Lock()
	g.fault = err
	g.mu.Unlock()
}

func (g *Gateway) currentFault() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fault
}

func (g *Gateway) dispatch(ctx context.Context, next update) {
	if !g.router.accepts(next) {
		return
	}
	if stopped := next.StoppedGeneration; stopped != nil {
		g.onStopped(ctx, stopped)
		return
	}
	key := laneKeyOf(next)
	if !g.lanes.push(ctx, inboundLane+key, func(ctx context.Context) { g.router.onUpdate(ctx, next) }) {
		g.log.Warn("update dropped, the gateway is stopping", "update", next.UpdateID)
	}
}

func (g *Gateway) sinkFor(conv chatRef) *sink {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := conv.String()
	found, ok := g.sinks[key]
	if !ok {
		found = &sink{conv: conv, api: g.api, msgs: g.msgs, log: g.log, now: g.now}
		g.sinks[key] = found
	}
	return found
}

func (g *Gateway) onStopped(ctx context.Context, stopped *generationStopped) {
	conv := chatRef{chat: stopped.Chat.ID, thread: stopped.ThreadID}
	target := g.sinkFor(conv)
	g.lanes.push(ctx, outboundLane+conv.String(), func(context.Context) {
		if target.stoppedDraft(stopped.DraftID) {
			g.router.interrupt(conv)
		}
	})
}

func (g *Gateway) noteUser(ctx context.Context, conv chatRef, messageID int64) {
	target := g.sinkFor(conv)
	g.lanes.push(ctx, outboundLane+conv.String(), func(ctx context.Context) { target.noteUser(ctx, messageID) })
}

// EmitTo renders one live turn event into the conversation the host routed it
// to. A text delta is dropped when the conversation's queue is full, since the
// final reply carries the whole answer.
func (g *Gateway) EmitTo(conv contracts.Conversation, event contracts.Event) {
	ref, err := parseChatRef(conv.ID)
	if err != nil {
		return
	}
	target := g.sinkFor(ref)
	work := func(ctx context.Context) { target.apply(ctx, event) }
	if event.T == eventChunk {
		g.lanes.offer(g.life(), outboundLane+ref.String(), work)
		return
	}
	if !g.lanes.push(g.life(), outboundLane+ref.String(), work) {
		g.log.Warn("turn event dropped, the gateway is stopping", "event", event.T)
	}
}

// Emit drops an unrouted event: without a conversation there is nothing to
// render into.
func (g *Gateway) Emit(contracts.Event) {}

// Post sends text as a new message in conv, split at the Telegram limit. It
// returns the id of the first message sent.
func (g *Gateway) Post(ctx context.Context, conv contracts.Conversation, text string) (contracts.MessageID, error) {
	return g.send(ctx, conv, 0, plain(text))
}

// Reply sends text as an answer to replyTo in conv. A reply to a message that
// is gone is sent as a plain message.
func (g *Gateway) Reply(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, text string) (contracts.MessageID, error) {
	target, err := parseMessageID(string(replyTo))
	if err != nil {
		return "", err
	}
	return g.send(ctx, conv, target, plain(text))
}

func (g *Gateway) send(ctx context.Context, conv contracts.Conversation, replyTo int64, text formatted) (contracts.MessageID, error) {
	ref, err := parseChatRef(conv.ID)
	if err != nil {
		return "", err
	}
	first := contracts.MessageID("")
	for index, part := range text.split(messageMaxUnits) {
		out := outgoing{ChatID: ref.chat, ThreadID: ref.thread, Text: part.text, Entities: part.entities, LinkPreview: noPreview()}
		if index == 0 && replyTo != 0 {
			out.ReplyParameters = &replyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
		}
		sent, err := g.api.sendMessage(ctx, out)
		if err != nil {
			return first, err
		}
		if index == 0 {
			first = contracts.MessageID(formatID(sent.MessageID))
		}
	}
	return first, nil
}

// React sets emoji on msg in conv. Telegram offers a fixed set of reactions:
// an emoji outside it is mapped to its nearest one, or refused with an error.
func (g *Gateway) React(ctx context.Context, conv contracts.Conversation, msg contracts.MessageID, emoji string) error {
	ref, err := parseChatRef(conv.ID)
	if err != nil {
		return err
	}
	target, err := parseMessageID(string(msg))
	if err != nil {
		return err
	}
	offered, ok := offeredReaction(emoji)
	if !ok {
		return fmt.Errorf("%w: %q", errUnsupported, emoji)
	}
	return g.api.setMessageReaction(ctx, ref.chat, target, offered)
}

// Menu sends prompt in reply to replyTo with one button per choice. A press is
// handed to the session of conv as a pick.
func (g *Gateway) Menu(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, prompt string, opts []contracts.Choice) error {
	if len(opts) == 0 {
		return errNoChoices
	}
	ref, err := parseChatRef(conv.ID)
	if err != nil {
		return err
	}
	values, labels := make([]string, 0, len(opts)), make([]string, 0, len(opts))
	for _, choice := range opts {
		values, labels = append(values, choice.Value), append(labels, choice.Label)
	}
	token := g.menus.put(ref.String(), values, labels)
	part := plain(prompt).tail(messageMaxUnits)
	out := outgoing{ChatID: ref.chat, ThreadID: ref.thread, Text: part.text, ReplyMarkup: keyboard(choicePrefix, token, labels), LinkPreview: noPreview()}
	if target, parseErr := parseMessageID(string(replyTo)); parseErr == nil {
		out.ReplyParameters = &replyParameters{MessageID: target, AllowSendingWithoutReply: true}
	}
	_, err = g.api.sendMessage(ctx, out)
	return err
}

// Notify renders notice in lang and posts it into conv. A notice that cannot be
// rendered is posted as its fallback text, reported with
// contracts.ErrNoticeFallbackPosted.
func (g *Gateway) Notify(ctx context.Context, conv contracts.Conversation, lang i18n.Language, notice contracts.Notice) (contracts.MessageID, error) {
	resolved := g.msgs.resolve(lang)
	text, renderErr := g.msgs.notice(resolved, notice)
	if renderErr != nil {
		text = g.msgs.unexpected(resolved, renderErr.Error())
	}
	sent, err := g.send(ctx, conv, 0, text)
	if err != nil {
		return sent, err
	}
	if renderErr != nil {
		return sent, fmt.Errorf("telegram notify: %w: %w", contracts.ErrNoticeFallbackPosted, renderErr)
	}
	return sent, nil
}

// Probe measures one round trip to the Bot API, for liveness. It fails while
// Telegram refuses to hand out updates because a webhook is set.
func (g *Gateway) Probe(ctx context.Context) (int64, error) {
	if fault := g.currentFault(); fault != nil {
		return 0, fault
	}
	start := g.now()
	if _, err := g.api.getMe(ctx); err != nil {
		return 0, err
	}
	return g.now().Sub(start).Milliseconds(), nil
}
