package telegram

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const (
	repoMenuMax    = 20
	stopCommand    = "/stop"
	localPrefix    = "local:"
	remotePrefix   = "remote:"
	repoNoneKey    = "telegram.repo.none"
	repoAskKey     = "telegram.repo.ask"
	repoCappedKey  = "telegram.repo.ask_capped"
	repoDoneKey    = "telegram.repo.started"
	deniedKey      = "telegram.menu.denied"
	ownedKey       = "telegram.menu.owned"
	goneKey        = "telegram.menu.gone"
	pickedKey      = "telegram.menu.picked"
	lostKey        = "telegram.undelivered"
	stopNoneKey    = "telegram.stop.none"
	stopSentKey    = "telegram.stop.sent"
	notReadyKey    = "telegram.control.unavailable"
	fileRefusedKey = "telegram.file.refused"
	albumWindow    = time.Second
	albumMaxItems  = 10
)

type pendingBind struct {
	token   string
	author  int64
	inbound contracts.Inbound
	values  []string
}

type router struct {
	set        settings
	msgs       messages
	log        *slog.Logger
	menus      *menuTable
	voices     *voiceBots
	files      stager
	ctrl       func() contracts.SessionControl
	noteUser   func(ctx context.Context, conv address, noted notedPrompt)
	accepted   func(ctx context.Context, conv address, noted notedPrompt)
	forgetUser func(ctx context.Context, conv address, noted notedPrompt)
	later      func(ctx context.Context, conv address, next job) bool

	notes   atomic.Uint64
	mu      sync.Mutex
	pending map[string]pendingBind
	albums  map[string]*heldAlbum
	holds   int
}

func (r *router) onUpdate(ctx context.Context, from origin, next tgctl.Update) {
	switch {
	case next.Message != nil:
		r.onMessage(ctx, from, next.Message)
	case next.CallbackQuery != nil:
		r.onCallback(ctx, from, next.CallbackQuery)
	}
}

func (r *router) accepts(from origin, next tgctl.Update) bool {
	switch {
	case next.Message != nil:
		return !r.voices.authoredByOwn(next.Message) && r.set.admits(next.Message)
	case next.CallbackQuery != nil:
		return from.house() && next.CallbackQuery.Message != nil
	case next.StoppedMessageGeneration != nil:
		stopped := next.StoppedMessageGeneration
		return stopped.Chat.Type == chatTypePrivate && r.set.authorizes(stopped.Chat.ID)
	}
	return false
}

func laneKeyOf(next tgctl.Update) string {
	if next.CallbackQuery != nil {
		return conversationOf(next.CallbackQuery.Message).String()
	}
	return conversationOf(next.Message).String()
}

type heldAlbum struct {
	group   string
	members []*tgctl.Message
	after   []*tgctl.Message
	gen     int
}

func albumKey(from origin, conv chatRef) string { return from.agent + "|" + conv.String() }

func (r *router) onMessage(ctx context.Context, from origin, msg *tgctl.Message) {
	key := albumKey(from, conversationOf(msg))
	r.mu.Lock()
	held, holding := r.albums[key]
	switch {
	case holding && msg.MediaGroupID == held.group:
		if len(held.members) < albumMaxItems {
			held.members = append(held.members, msg)
		}
		r.holds++
		held.gen = r.holds
		gen := held.gen
		r.mu.Unlock()
		r.scheduleFlush(ctx, from, msg, key, gen)
		return
	case holding && !r.isStop(from, msg):
		kept := len(held.after) < laneDepth
		if kept {
			held.after = append(held.after, msg)
		}
		r.mu.Unlock()
		if !kept {
			r.log.Warn("message dropped, too many wait behind an album", "chat", msg.Chat.ID)
		}
		return
	case !holding && msg.MediaGroupID != "":
		r.holds++
		gen := r.holds
		r.albums[key] = &heldAlbum{group: msg.MediaGroupID, members: []*tgctl.Message{msg}, gen: gen}
		r.mu.Unlock()
		r.scheduleFlush(ctx, from, msg, key, gen)
		return
	}
	r.mu.Unlock()
	r.handle(ctx, from, msg, []*tgctl.Message{msg})
}

func (r *router) isStop(from origin, msg *tgctl.Message) bool {
	return strings.EqualFold(stripCommandMention(strings.TrimSpace(msg.Text), from.self.Username), stopCommand)
}

func (r *router) scheduleFlush(ctx context.Context, from origin, msg *tgctl.Message, key string, gen int) {
	conv := address{chatRef: conversationOf(msg)}
	if !r.later(ctx, conv, func(ctx context.Context) { r.flushAlbum(ctx, from, key, gen) }) {
		r.flushAlbum(ctx, from, key, gen)
	}
}

func (r *router) flushAlbum(ctx context.Context, from origin, key string, gen int) {
	r.mu.Lock()
	held, holding := r.albums[key]
	if !holding || held.gen != gen {
		r.mu.Unlock()
		return
	}
	delete(r.albums, key)
	r.mu.Unlock()
	lead := held.members[0]
	for _, member := range held.members {
		if member.Caption != "" || member.Text != "" {
			lead = member
			break
		}
	}
	r.handle(ctx, from, lead, held.members)
	for _, next := range held.after {
		r.onMessage(ctx, from, next)
	}
}

func (r *router) heldMessages() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, held := range r.albums {
		count += len(held.members) + len(held.after)
	}
	return count
}

func (r *router) handle(ctx context.Context, from origin, msg *tgctl.Message, members []*tgctl.Message) {
	ctrl := r.ctrl()
	refusalShown := false
	for _, target := range r.route(from, msg, voicesOf(ctrl)) {
		r.answer(ctx, from, ctrl, msg, members, target, &refusalShown)
	}
}

func (r *router) answer(ctx context.Context, from origin, ctrl contracts.SessionControl, msg *tgctl.Message, members []*tgctl.Message, target addressee, refusalShown *bool) {
	conv, text := target.conv, target.text
	if ctrl == nil {
		r.post(ctx, conv, r.msgs.say(notReadyKey, nil))
		return
	}
	if strings.EqualFold(text, stopCommand) {
		r.stop(ctx, ctrl, conv)
		return
	}
	var attachments []contracts.Attachment
	offered := 0
	for _, member := range members {
		attachments = append(attachments, r.files.stage(ctx, from.api, conv, member)...)
		offered += len(filesOf(member))
	}
	if len(attachments) < offered && !*refusalShown {
		*refusalShown = true
		r.post(ctx, conv, r.msgs.say(fileRefusedKey, nil))
	}
	if text == "" && len(attachments) == 0 {
		return
	}
	inbound := contracts.Inbound{
		Conversation: contracts.Conversation{Gateway: gatewayKind, ID: conv.String()},
		Author:       displayName(msg.From),
		AuthorID:     formatID(msg.From.ID),
		Text:         text,
		Attachments:  attachments,
		MessageID:    contracts.MessageID(formatID(msg.MessageID)),
	}
	if r.deliver(ctx, ctrl, conv, inbound) {
		return
	}
	r.askRepo(ctx, ctrl, conv, msg.From.ID, inbound)
}

func reachesVoice(voice contracts.AgentVoice, raw string, base chatRef) bool {
	if namesMentioned(raw, voice.Agent) {
		return true
	}
	return voice.Reply == contracts.ReplyInChannels && channelHas(voice.Channels, base)
}

func voicesOf(ctrl contracts.SessionControl) []contracts.AgentVoice {
	if ctrl == nil {
		return nil
	}
	return directoryVoices(ctrl)
}

type addressee struct {
	conv address
	text string
}

func (r *router) route(from origin, msg *tgctl.Message, voices []contracts.AgentVoice) []addressee {
	base := conversationOf(msg)
	if !from.house() {
		return r.routeOwnVoice(from, msg, base, voices)
	}
	return r.routeHouse(from, msg, base, voices)
}

func displayName(from *tgctl.User) string {
	if from.Username != "" {
		return from.Username
	}
	return from.FirstName
}

func (r *router) deliver(ctx context.Context, ctrl contracts.SessionControl, conv address, inbound contracts.Inbound) bool {
	session, archived := sessionOf(ctrl, conv)
	if session == "" {
		return false
	}
	if archived {
		if err := ctrl.Resume(session); err != nil {
			r.log.Warn("resume the session of the conversation failed", "session", session, "err", err)
			return false
		}
	}
	messageID, idErr := parseMessageID(string(inbound.MessageID))
	if idErr != nil {
		return ctrl.Submit(session, inbound)
	}
	prompt := notedPrompt{id: messageID, author: inbound.Author, text: inbound.Text, note: r.notes.Add(1)}
	r.noteUser(ctx, conv, prompt)
	if ctrl.Submit(session, inbound) {
		r.accepted(ctx, conv, prompt)
		return true
	}
	r.forgetUser(ctx, conv, prompt)
	return false
}

func sessionOf(ctrl contracts.SessionControl, conv address) (string, bool) {
	wanted := conv.String()
	items := ctrl.Sessions()
	for index := range items {
		item := &items[index]
		if item.ChannelID == wanted && slices.Contains(item.Gateways, gatewayKind) {
			return item.Name, item.Archived
		}
	}
	return "", false
}

func (r *router) stop(ctx context.Context, ctrl contracts.SessionControl, conv address) {
	session, _ := sessionOf(ctrl, conv)
	if session == "" || !ctrl.Interrupt(session) {
		r.post(ctx, conv, r.msgs.say(stopNoneKey, nil))
		return
	}
	r.post(ctx, conv, r.msgs.say(stopSentKey, nil))
}

func (r *router) interrupt(conv address) {
	ctrl := r.ctrl()
	if ctrl == nil {
		return
	}
	if session, _ := sessionOf(ctrl, conv); session != "" {
		ctrl.Interrupt(session)
	}
}

func (r *router) askRepo(ctx context.Context, ctrl contracts.SessionControl, conv address, author int64, inbound contracts.Inbound) {
	repos, err := ctrl.Repos(ctx)
	if err != nil {
		r.log.Warn("list repos failed", "err", err)
	}
	if len(repos) == 0 {
		r.post(ctx, conv, r.msgs.say(repoNoneKey, nil))
		return
	}
	if len(repos) == 1 {
		r.bind(ctx, ctrl, conv, repoValue(repos[0]), inbound)
		return
	}
	shown := repos[:min(len(repos), repoMenuMax)]
	values, labels := make([]string, 0, len(shown)), make([]string, 0, len(shown))
	for _, repo := range shown {
		values, labels = append(values, repoValue(repo)), append(labels, repo.Name)
	}
	token := r.menus.put(conv.chatRef.String(), conv.String(), values, labels)
	r.mu.Lock()
	r.pending[conv.String()] = pendingBind{token: token, author: author, inbound: inbound, values: values}
	r.mu.Unlock()
	prompt := r.msgs.say(repoAskKey, nil)
	if len(repos) > len(shown) {
		prompt = r.msgs.say(repoCappedKey, map[string]any{"Question": prompt, "Shown": len(shown), "Count": len(repos)})
	}
	speak := r.voices.menuSpeakerAt(conv)
	part := speak.frame(plain(prompt))
	out := tgctl.OutgoingMessage{ChatID: conv.chat, ThreadID: conv.thread, Text: part.text, Entities: part.entities, ReplyMarkup: keyboard(bindPrefix, token, labels)}
	if _, err := speak.api.SendMessage(ctx, out); err != nil {
		r.log.Warn("repo menu failed", "err", err)
	}
}

func repoValue(repo contracts.RepoRef) string {
	if repo.Local {
		return localPrefix + repo.Name
	}
	return remotePrefix + repo.Name
}

func (r *router) bind(ctx context.Context, ctrl contracts.SessionControl, conv address, value string, inbound contracts.Inbound) {
	spec := contracts.CreateSession{
		Name:      conv.sessionName(),
		ChannelID: conv.String(),
		Gateways:  []string{gatewayKind},
		Agent:     r.agentFor(conv),
	}
	if target, local := strings.CutPrefix(value, localPrefix); local {
		spec.Project = target
	} else {
		spec.Clone = strings.TrimPrefix(value, remotePrefix)
	}
	if _, err := ctrl.Create(ctx, spec); err != nil {
		r.postFormatted(ctx, conv, r.msgs.noticeText(r.msgs.lang, contracts.NoticeOf(err)))
		return
	}
	if !r.deliver(ctx, ctrl, conv, inbound) {
		r.post(ctx, conv, r.msgs.say(lostKey, nil))
	}
}

func (r *router) agentFor(conv address) string {
	if conv.agent != "" {
		return conv.agent
	}
	return r.set.agent
}

func (r *router) onCallback(ctx context.Context, from origin, query *tgctl.CallbackQuery) {
	answer, then := r.pick(ctx, query)
	if err := from.api.AnswerCallbackQuery(ctx, tgctl.CallbackAnswer{CallbackQueryID: query.ID, Text: answer}); err != nil {
		r.log.Warn("answer button press failed", "err", err)
	}
	if then != nil {
		then(ctx)
	}
}

func (r *router) pick(ctx context.Context, query *tgctl.CallbackQuery) (string, job) {
	if !r.set.authorizes(query.From.ID) {
		return r.msgs.say(deniedKey, nil), nil
	}
	parsed, ok := parseCallback(query.Data)
	if !ok || query.Message == nil {
		return r.msgs.say(goneKey, nil), nil
	}
	ctrl := r.ctrl()
	if ctrl == nil {
		return r.msgs.say(notReadyKey, nil), nil
	}
	chat := conversationOf(query.Message)
	_, target, found := r.menus.lookup(parsed.token, chat.String(), parsed.index)
	if !found {
		return r.msgs.say(goneKey, nil), nil
	}
	conv, err := parseAddress(target)
	if err != nil {
		return r.msgs.say(goneKey, nil), nil
	}
	if parsed.kind == bindPrefix && !r.ownsBind(conv, parsed.token, query.From.ID) {
		return r.msgs.say(ownedKey, nil), nil
	}
	picked, _, _ := r.menus.take(parsed.token, chat.String(), parsed.index)
	r.closeMenu(ctx, query.Message)
	if parsed.kind == bindPrefix {
		return r.pickRepo(ctrl, conv, parsed.token, picked)
	}
	session, _ := sessionOf(ctrl, conv)
	if session == "" || !ctrl.Pick(session, picked.value) {
		return r.msgs.say(goneKey, nil), nil
	}
	return r.msgs.say(pickedKey, map[string]any{"Choice": picked.label}), nil
}

func (r *router) ownsBind(conv address, token string, from int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	waiting, found := r.pending[conv.String()]
	return !found || waiting.token != token || waiting.author == from
}

func (r *router) pickRepo(ctrl contracts.SessionControl, conv address, token string, picked choice) (string, job) {
	r.mu.Lock()
	waiting, found := r.pending[conv.String()]
	if found && waiting.token == token {
		delete(r.pending, conv.String())
	}
	r.mu.Unlock()
	if !found || waiting.token != token {
		return r.msgs.say(goneKey, nil), nil
	}
	bind := func(ctx context.Context) { r.bind(ctx, ctrl, conv, picked.value, waiting.inbound) }
	return r.msgs.say(repoDoneKey, map[string]any{"Repo": picked.label}), bind
}

func (r *router) closeMenu(ctx context.Context, msg *tgctl.Message) {
	edit := tgctl.EditMessageReplyMarkup{ChatID: msg.Chat.ID, MessageID: msg.MessageID, ReplyMarkup: tgctl.InlineKeyboardMarkup{Rows: [][]tgctl.InlineKeyboardButton{}}}
	if err := r.voices.house.EditMessageReplyMarkup(ctx, edit); err != nil {
		r.log.Warn("close the menu failed", "err", err)
	}
}

func (r *router) post(ctx context.Context, conv address, text string) {
	r.postFormatted(ctx, conv, plain(text))
}

func (r *router) postFormatted(ctx context.Context, conv address, text formatted) {
	speak := r.voices.speakerAt(conv)
	for _, part := range text.split(speak.budget()) {
		if _, err := speak.api.SendMessage(ctx, speak.message(conv, 0, part)); err != nil {
			r.log.Warn("post failed", "err", err)
		}
	}
}
