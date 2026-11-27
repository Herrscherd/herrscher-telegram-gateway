package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
	"github.com/Herrscherd/tgctl"
)

const (
	pollBackoffFloor   = time.Second
	pollBackoffCeiling = time.Minute
	backoffGrowth      = 2
	inboundLane        = "in/"
	outboundLane       = "out/"
	webhookWord        = "webhook"
	voiceSignInWait    = 10 * time.Second
)

var (
	errNoChoices   = errors.New("telegram menu: no choices")
	errUnsupported = errors.New("telegram: reaction not offered by telegram")
	errWebhookSet  = errors.New("telegram: a webhook is set on this bot, so updates cannot be polled; remove it with deleteWebhook")
	errOtherPoller = errors.New("telegram: another process polls this bot with the same token")
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
// updates when the host binds its session control, one long poll for the house
// bot and one for each agent that has its own token, and stops on Close.
type Gateway struct {
	api    bot
	self   tgctl.User
	voices *voiceBots
	dial   func(token string) bot
	msgs   messages
	log    *slog.Logger
	menus  *menuTable
	router *router
	lanes  *lanes
	life   lifetime
	cancel context.CancelFunc
	now    func() time.Time
	album  time.Duration

	mu      sync.Mutex
	ctrl    contracts.SessionControl
	sinks   map[string]*sink
	drafts  atomic.Int64
	polling bool
	closed  bool
	fault   error
	poller  sync.WaitGroup
}

func newGateway(ctx context.Context, api bot, set settings, self tgctl.User, msgs messages, log *slog.Logger, dial func(token string) bot) *Gateway {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	life := func() context.Context { return run }
	voices := newVoiceBots(api, self)
	gateway := &Gateway{
		api: api, self: self, voices: voices, dial: dial, msgs: msgs, log: log,
		menus: newMenuTable(), lanes: newLanes(), life: life, cancel: cancel, now: time.Now, album: albumWindow,
		sinks: make(map[string]*sink),
	}
	gateway.drafts.Store(gateway.now().UnixMilli())
	gateway.router = &router{
		set: set, msgs: msgs, log: log, menus: gateway.menus, voices: voices,
		files:    stager{root: stagingRoot(), log: log},
		ctrl:     gateway.control,
		noteUser: gateway.noteUser,
		later:    gateway.later,
		pending:  make(map[string]pendingBind),
		albums:   make(map[string]*heldAlbum),
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
// long polling loops: the house bot's, then one for each agent voice that has
// its own token. Each such voice signs in and is registered before the binding
// returns, so it speaks through its own bot from then on. Only the first
// binding starts them, and none after Close.
func (g *Gateway) BindSessionControl(ctrl contracts.SessionControl) {
	voices := directoryVoices(ctrl)
	var signedIn sync.WaitGroup
	if !g.startPolling(ctrl, voices, &signedIn) {
		return
	}
	signedIn.Wait()
}

func (g *Gateway) startPolling(ctrl contracts.SessionControl, voices []contracts.AgentVoice, signedIn *sync.WaitGroup) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.polling {
		return false
	}
	g.ctrl = ctrl
	g.polling = true
	g.poller.Go(func() { g.poll(g.life(), g.houseOrigin()) })
	for _, voice := range voices {
		if !voice.Token.Valid() || !validAgent(voice.Agent) {
			continue
		}
		client := g.dial(voice.Token.Secret())
		signedIn.Add(1)
		g.poller.Go(func() {
			defer signedIn.Done()
			g.startVoice(voice.Agent, client)
		})
	}
	return true
}

func (g *Gateway) houseOrigin() origin {
	return origin{api: g.api, self: g.self}
}

func (g *Gateway) startVoice(agent string, client bot) {
	ctx, cancel := context.WithTimeout(g.life(), voiceSignInWait)
	identity, err := client.GetMe(ctx)
	cancel()
	if err != nil {
		if g.life().Err() == nil {
			g.log.Warn("agent bot skipped, its token could not sign in", "agent", agent, "err", err)
		}
		return
	}
	g.voices.register(agent, client, identity)
	from := origin{agent: agent, api: client, self: identity}
	g.poller.Go(func() { g.poll(g.life(), from) })
}

func (g *Gateway) control() contracts.SessionControl {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ctrl
}

// Close stops the polling loops and the conversation workers and waits for them,
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
		if lost := g.router.heldMessages(); lost > 0 {
			g.log.Warn("messages held for an album were dropped on close", "count", lost)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram gateway close: %w", ctx.Err())
	}
}

func (g *Gateway) poll(ctx context.Context, from origin) {
	offset := int64(0)
	backoff := pollBackoffFloor
	for ctx.Err() == nil {
		updates, err := from.api.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait, limited := tgctl.RetryAfter(err)
			if !limited {
				wait = backoff
				backoff = min(backoff*backoffGrowth, pollBackoffCeiling)
			}
			g.notePollFailure(from, err, wait)
			pause(ctx, wait)
			continue
		}
		if from.house() {
			g.setFault(nil)
		}
		backoff = pollBackoffFloor
		for _, next := range updates {
			offset = max(offset, next.UpdateID+1)
			g.dispatch(ctx, from, next)
		}
	}
}

func (g *Gateway) notePollFailure(from origin, err error, wait time.Duration) {
	if !from.house() {
		g.log.Warn("agent updates failed", "agent", from.agent, "err", err, "retry_in", wait)
		return
	}
	if failure, ok := errors.AsType[*tgctl.APIError](err); ok && errors.Is(err, tgctl.ErrConflict) {
		cause := errOtherPoller
		if strings.Contains(strings.ToLower(failure.Description), webhookWord) {
			cause = errWebhookSet
		}
		g.log.Error("telegram updates refused", "err", cause, "retry_in", wait)
		g.setFault(cause)
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

func (g *Gateway) dispatch(ctx context.Context, from origin, next tgctl.Update) {
	if !g.router.accepts(from, next) {
		return
	}
	if stopped := next.StoppedMessageGeneration; stopped != nil {
		g.onStopped(ctx, from, stopped)
		return
	}
	key := laneKeyOf(next)
	if !g.lanes.push(ctx, inboundLane+key, func(ctx context.Context) { g.router.onUpdate(ctx, from, next) }) {
		g.log.Warn("update dropped, the gateway is stopping", "update", next.UpdateID)
	}
}

func (g *Gateway) sinkFor(conv address) *sink {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := conv.String()
	found, ok := g.sinks[key]
	if !ok {
		found = &sink{conv: conv, voices: g.voices, drafts: &g.drafts, msgs: g.msgs, log: g.log, now: g.now}
		g.sinks[key] = found
	}
	return found
}

func (g *Gateway) draftedBy(from origin, conv address) bool {
	if !from.house() {
		return conv.agent == from.agent
	}
	return conv.agent == "" || !g.voices.hasOwn(conv.agent)
}

func (g *Gateway) sinksOf(from origin, stopped *tgctl.GenerationStopped) []*sink {
	g.mu.Lock()
	defer g.mu.Unlock()
	ref := chatRef{chat: stopped.Chat.ID, thread: stopped.ThreadID}
	var found []*sink
	for _, candidate := range g.sinks {
		if candidate.conv.chatRef == ref && g.draftedBy(from, candidate.conv) {
			found = append(found, candidate)
		}
	}
	return found
}

func (g *Gateway) onStopped(ctx context.Context, from origin, stopped *tgctl.GenerationStopped) {
	for _, target := range g.sinksOf(from, stopped) {
		conv := target.conv
		g.lanes.push(ctx, outboundLane+conv.String(), func(context.Context) {
			if target.stoppedDraft(stopped.DraftID) {
				g.router.interrupt(conv)
			}
		})
	}
}

func (g *Gateway) later(ctx context.Context, conv address, next job) bool {
	return g.lanes.later(ctx, inboundLane+conv.String(), g.album, next)
}

func (g *Gateway) noteUser(ctx context.Context, conv address, noted notedPrompt) {
	target := g.sinkFor(conv)
	g.lanes.push(ctx, outboundLane+conv.String(), func(ctx context.Context) { target.noteUser(ctx, noted) })
}

// EmitTo renders one live turn event into the conversation the host routed it
// to. A text delta is dropped when the conversation's queue is full, since the
// final reply carries the whole answer.
func (g *Gateway) EmitTo(conv contracts.Conversation, event contracts.Event) {
	ref, err := parseAddress(conv.ID)
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

// Post sends text as a new message in conv, its Markdown rendered as entities and
// split at the Telegram limit. It returns the id of the first message sent.
func (g *Gateway) Post(ctx context.Context, conv contracts.Conversation, text string) (contracts.MessageID, error) {
	return g.send(ctx, conv, 0, formatMarkdown(text))
}

// Reply sends text as an answer to replyTo in conv, its Markdown rendered as
// entities. A reply to a message that is gone is sent as a plain message.
func (g *Gateway) Reply(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, text string) (contracts.MessageID, error) {
	target, err := parseMessageID(string(replyTo))
	if err != nil {
		return "", err
	}
	return g.send(ctx, conv, target, formatMarkdown(text))
}

func (g *Gateway) send(ctx context.Context, conv contracts.Conversation, replyTo int64, text formatted) (contracts.MessageID, error) {
	ref, err := parseAddress(conv.ID)
	if err != nil {
		return "", err
	}
	speak := g.voices.speakerAt(ref)
	first := contracts.MessageID("")
	for index, part := range text.split(speak.budget()) {
		reply := int64(0)
		if index == 0 {
			reply = replyTo
		}
		sent, err := speak.api.SendMessage(ctx, speak.message(ref, reply, part))
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
	ref, err := parseAddress(conv.ID)
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
	return g.voices.speakerAt(ref).api.SetMessageReaction(ctx, ref.chat, target, offered)
}

// Menu sends prompt in reply to replyTo with one button per choice. A press is
// handed to the session of conv as a pick.
func (g *Gateway) Menu(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, prompt string, opts []contracts.Choice) error {
	if len(opts) == 0 {
		return errNoChoices
	}
	ref, err := parseAddress(conv.ID)
	if err != nil {
		return err
	}
	values, labels := make([]string, 0, len(opts)), make([]string, 0, len(opts))
	for _, choice := range opts {
		values, labels = append(values, choice.Value), append(labels, choice.Label)
	}
	token := g.menus.put(ref.chatRef.String(), ref.String(), values, labels)
	speak := g.voices.menuSpeakerAt(ref)
	part := speak.frame(plain(prompt).tail(speak.budget()))
	out := tgctl.OutgoingMessage{ChatID: ref.chat, ThreadID: ref.thread, Text: part.text, Entities: part.entities, ReplyMarkup: keyboard(choicePrefix, token, labels), LinkPreview: noPreview()}
	if target, parseErr := parseMessageID(string(replyTo)); parseErr == nil {
		out.ReplyParameters = &tgctl.ReplyParameters{MessageID: target, AllowSendingWithoutReply: true}
	}
	_, err = speak.api.SendMessage(ctx, out)
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
	if _, err := g.api.GetMe(ctx); err != nil {
		return 0, err
	}
	return g.now().Sub(start).Milliseconds(), nil
}
