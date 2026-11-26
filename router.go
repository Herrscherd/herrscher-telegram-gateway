package telegram

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	contracts "github.com/Herrscherd/herrscher-contracts"
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
	api      bot
	set      settings
	self     botIdentity
	msgs     messages
	log      *slog.Logger
	menus    *menuTable
	files    stager
	ctrl     func() contracts.SessionControl
	noteUser func(ctx context.Context, conv chatRef, noted notedPrompt)
	later    func(ctx context.Context, conv chatRef, next job) bool

	mu      sync.Mutex
	pending map[string]pendingBind
	albums  map[string]*heldAlbum
}

func (r *router) onUpdate(ctx context.Context, next update) {
	switch {
	case next.Message != nil:
		r.onMessage(ctx, next.Message)
	case next.CallbackQuery != nil:
		r.onCallback(ctx, next.CallbackQuery)
	}
}

func (r *router) accepts(next update) bool {
	switch {
	case next.Message != nil:
		return r.set.admits(next.Message)
	case next.CallbackQuery != nil:
		return next.CallbackQuery.Message != nil
	case next.StoppedGeneration != nil:
		stopped := next.StoppedGeneration
		return stopped.Chat.Type == chatTypePrivate && r.set.authorizes(stopped.Chat.ID)
	}
	return false
}

func laneKeyOf(next update) string {
	if next.CallbackQuery != nil {
		return conversationOf(next.CallbackQuery.Message).String()
	}
	return conversationOf(next.Message).String()
}

type heldAlbum struct {
	group   string
	members []*message
	after   []*message
	gen     int
}

func (r *router) onMessage(ctx context.Context, msg *message) {
	conv := conversationOf(msg).String()
	r.mu.Lock()
	held, holding := r.albums[conv]
	switch {
	case holding && msg.MediaGroupID == held.group:
		if len(held.members) < albumMaxItems {
			held.members = append(held.members, msg)
		}
		held.gen++
		r.mu.Unlock()
		r.scheduleFlush(ctx, msg, held.gen)
		return
	case holding:
		if len(held.after) < laneDepth {
			held.after = append(held.after, msg)
		}
		r.mu.Unlock()
		return
	case msg.MediaGroupID != "":
		r.albums[conv] = &heldAlbum{group: msg.MediaGroupID, members: []*message{msg}}
		r.mu.Unlock()
		r.scheduleFlush(ctx, msg, 0)
		return
	}
	r.mu.Unlock()
	r.handle(ctx, msg, []*message{msg})
}

func (r *router) scheduleFlush(ctx context.Context, msg *message, gen int) {
	conv := conversationOf(msg)
	if !r.later(ctx, conv, func(ctx context.Context) { r.flushAlbum(ctx, conv.String(), gen) }) {
		r.flushAlbum(ctx, conv.String(), gen)
	}
}

func (r *router) flushAlbum(ctx context.Context, conv string, gen int) {
	r.mu.Lock()
	held, holding := r.albums[conv]
	if !holding || held.gen != gen {
		r.mu.Unlock()
		return
	}
	delete(r.albums, conv)
	r.mu.Unlock()
	lead := held.members[0]
	for _, member := range held.members {
		if member.Caption != "" || member.Text != "" {
			lead = member
			break
		}
	}
	r.handle(ctx, lead, held.members)
	for _, next := range held.after {
		r.onMessage(ctx, next)
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

func (r *router) handle(ctx context.Context, msg *message, members []*message) {
	text, addressed := r.addressed(msg)
	if !addressed {
		return
	}
	conv := conversationOf(msg)
	ctrl := r.ctrl()
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
		attachments = append(attachments, r.files.stage(ctx, conv, member)...)
		offered += len(filesOf(member))
	}
	if len(attachments) < offered {
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

func (r *router) deliver(ctx context.Context, ctrl contracts.SessionControl, conv chatRef, inbound contracts.Inbound) bool {
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
	if !ctrl.Submit(session, inbound) {
		return false
	}
	if messageID, err := parseMessageID(string(inbound.MessageID)); err == nil {
		r.noteUser(ctx, conv, notedPrompt{id: messageID, author: inbound.Author, text: inbound.Text})
	}
	return true
}

func sessionOf(ctrl contracts.SessionControl, conv chatRef) (string, bool) {
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

func displayName(from *user) string {
	if from.Username != "" {
		return from.Username
	}
	return from.FirstName
}

func (r *router) addressed(msg *message) (string, bool) {
	raw, entities := msg.Text, msg.Entities
	if raw == "" {
		raw, entities = msg.Caption, msg.CaptionEntities
	}
	text := strings.TrimSpace(raw)
	if msg.Chat.Type == chatTypePrivate {
		return stripCommandMention(text, r.self.Username), true
	}
	if msg.ReplyTo != nil && msg.ReplyTo.From != nil && msg.ReplyTo.From.ID == r.self.ID {
		return stripCommandMention(text, r.self.Username), true
	}
	stripped, mentioned := r.withoutMention(raw, entities)
	if !mentioned {
		return "", false
	}
	return stripCommandMention(strings.TrimSpace(stripped), r.self.Username), true
}

func (r *router) withoutMention(text string, entities []entity) (string, bool) {
	units := utf16.Encode([]rune(text))
	for _, candidate := range entities {
		end := candidate.Offset + candidate.Length
		if candidate.Offset < 0 || end > len(units) {
			continue
		}
		if !r.mentions(candidate, string(utf16.Decode(units[candidate.Offset:end]))) {
			continue
		}
		if candidate.Type == entityCommand {
			return text, true
		}
		rest := slices.Concat(units[:candidate.Offset], units[end:])
		return string(utf16.Decode(rest)), true
	}
	return "", false
}

func (r *router) mentions(candidate entity, covered string) bool {
	switch candidate.Type {
	case entityMention:
		return strings.EqualFold(strings.TrimPrefix(covered, "@"), r.self.Username)
	case entityUserLink:
		return candidate.User != nil && candidate.User.ID == r.self.ID
	case entityCommand:
		_, target, found := strings.Cut(covered, "@")
		return found && strings.EqualFold(target, r.self.Username)
	}
	return false
}

func stripCommandMention(text, username string) string {
	if !strings.HasPrefix(text, "/") || username == "" {
		return text
	}
	command, rest, _ := strings.Cut(text, " ")
	name, target, found := strings.Cut(command, "@")
	if !found || !strings.EqualFold(target, username) {
		return text
	}
	return strings.TrimSpace(name + " " + rest)
}

func (r *router) stop(ctx context.Context, ctrl contracts.SessionControl, conv chatRef) {
	session, _ := sessionOf(ctrl, conv)
	if session == "" || !ctrl.Interrupt(session) {
		r.post(ctx, conv, r.msgs.say(stopNoneKey, nil))
		return
	}
	r.post(ctx, conv, r.msgs.say(stopSentKey, nil))
}

func (r *router) interrupt(conv chatRef) {
	ctrl := r.ctrl()
	if ctrl == nil {
		return
	}
	if session, _ := sessionOf(ctrl, conv); session != "" {
		ctrl.Interrupt(session)
	}
}

func (r *router) askRepo(ctx context.Context, ctrl contracts.SessionControl, conv chatRef, author int64, inbound contracts.Inbound) {
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
	token := r.menus.put(conv.String(), values, labels)
	r.mu.Lock()
	r.pending[conv.String()] = pendingBind{token: token, author: author, inbound: inbound, values: values}
	r.mu.Unlock()
	prompt := r.msgs.say(repoAskKey, nil)
	if len(repos) > len(shown) {
		prompt = r.msgs.say(repoCappedKey, map[string]any{"Question": prompt, "Shown": len(shown), "Count": len(repos)})
	}
	out := outgoing{ChatID: conv.chat, ThreadID: conv.thread, Text: prompt, ReplyMarkup: keyboard(bindPrefix, token, labels)}
	if _, err := r.api.sendMessage(ctx, out); err != nil {
		r.log.Warn("repo menu failed", "err", err)
	}
}

func repoValue(repo contracts.RepoRef) string {
	if repo.Local {
		return localPrefix + repo.Name
	}
	return remotePrefix + repo.Name
}

func (r *router) bind(ctx context.Context, ctrl contracts.SessionControl, conv chatRef, value string, inbound contracts.Inbound) {
	spec := contracts.CreateSession{
		Name:      conv.sessionName(),
		ChannelID: conv.String(),
		Gateways:  []string{gatewayKind},
		Agent:     r.set.agent,
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

func (r *router) onCallback(ctx context.Context, query *callbackQuery) {
	answer, then := r.pick(ctx, query)
	if err := r.api.answerCallbackQuery(ctx, callbackAnswer{CallbackQueryID: query.ID, Text: answer}); err != nil {
		r.log.Warn("answer button press failed", "err", err)
	}
	if then != nil {
		then(ctx)
	}
}

func (r *router) pick(ctx context.Context, query *callbackQuery) (string, job) {
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
	conv := conversationOf(query.Message)
	if parsed.kind == bindPrefix && !r.ownsBind(conv, parsed.token, query.From.ID) {
		return r.msgs.say(ownedKey, nil), nil
	}
	picked, found := r.menus.take(parsed.token, conv.String(), parsed.index)
	if !found {
		return r.msgs.say(goneKey, nil), nil
	}
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

func (r *router) ownsBind(conv chatRef, token string, from int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	waiting, found := r.pending[conv.String()]
	return !found || waiting.token != token || waiting.author == from
}

func (r *router) pickRepo(ctrl contracts.SessionControl, conv chatRef, token string, picked choice) (string, job) {
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

func (r *router) closeMenu(ctx context.Context, msg *message) {
	edit := markupEdit{ChatID: msg.Chat.ID, MessageID: msg.MessageID, ReplyMarkup: inlineKeyboard{Rows: [][]inlineButton{}}}
	if err := r.api.editMessageReplyMarkup(ctx, edit); err != nil {
		r.log.Warn("close the menu failed", "err", err)
	}
}

func (r *router) post(ctx context.Context, conv chatRef, text string) {
	r.postFormatted(ctx, conv, plain(text))
}

func (r *router) postFormatted(ctx context.Context, conv chatRef, text formatted) {
	for _, part := range text.split(messageMaxUnits) {
		out := outgoing{ChatID: conv.chat, ThreadID: conv.thread, Text: part.text, Entities: part.entities, LinkPreview: noPreview()}
		if _, err := r.api.sendMessage(ctx, out); err != nil {
			r.log.Warn("post failed", "err", err)
		}
	}
}
