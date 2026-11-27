package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const (
	testToken    = "123456:secret-token"
	testBotID    = 777
	testBotName  = "herrscher_bot"
	testOwner    = 42
	testStranger = 99
	testGroup    = -100500
	waitLimit    = 3 * time.Second
	voiceToken   = "654321:voice-token"
	voiceBotID   = 888
	voiceBotName = "ana_bot"
	refusedToken = "111111:refused-token"
)

type signal struct {
	mu      sync.Mutex
	changed chan struct{}
}

func newSignal() *signal { return &signal{changed: make(chan struct{})} }

func (s *signal) bump() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *signal) next() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func waitUntil(t *testing.T, what string, change *signal, done func() bool) {
	t.Helper()
	deadline := time.NewTimer(waitLimit)
	defer deadline.Stop()
	for {
		next := change.next()
		if done() {
			return
		}
		select {
		case <-next:
		case <-deadline.C:
			t.Fatalf("waited for %s", what)
		}
	}
}

type apiCall struct {
	token  string
	method string
	body   []byte
}

func decodeCall[T any](t *testing.T, call apiCall) T {
	t.Helper()
	var decoded T
	if err := json.Unmarshal(call.body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", call.method, err)
	}
	return decoded
}

type reactionCall struct {
	ChatID    int64                `json:"chat_id"`
	MessageID int64                `json:"message_id"`
	Reaction  []tgctl.ReactionType `json:"reaction"`
}

type fakeTelegram struct {
	t          *testing.T
	server     *httptest.Server
	mu         sync.Mutex
	calls      []apiCall
	updates    map[string][]tgctl.Update
	nextID     int64
	files      map[string]string
	fail       map[string]int
	refused    map[string]bool
	signIns    map[string]int
	polls      map[string]int
	identities map[string]tgctl.User
	texts      map[int64]string
	conflict   string
	change     *signal
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	fake := &fakeTelegram{
		t: t, nextID: 1000, files: make(map[string]string), fail: make(map[string]int),
		refused: map[string]bool{refusedToken: true}, updates: make(map[string][]tgctl.Update),
		signIns: make(map[string]int), polls: make(map[string]int),
		identities: map[string]tgctl.User{
			testToken:    {ID: testBotID, IsBot: true, FirstName: "Herrscher", Username: testBotName},
			voiceToken:   {ID: voiceBotID, IsBot: true, FirstName: "Ana", Username: voiceBotName},
			refusedToken: {ID: 111, IsBot: true, FirstName: "Ghost", Username: "ghost_bot"},
		},
		texts: make(map[int64]string), change: newSignal(),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeTelegram) serve(writer http.ResponseWriter, request *http.Request) {
	if rest, ok := strings.CutPrefix(request.URL.Path, "/file/bot"); ok {
		f.serveFile(writer, request, rest)
		return
	}
	rest, ok := strings.CutPrefix(request.URL.Path, "/bot")
	if !ok {
		writer.WriteHeader(http.StatusNotFound)
		f.write(writer, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		return
	}
	token, method, _ := strings.Cut(rest, "/")
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Errorf("read the request: %v", err)
	}
	f.serveCall(writer, request, token, method, body)
}

func (f *fakeTelegram) serveFile(writer http.ResponseWriter, request *http.Request, rest string) {
	_, path, _ := strings.Cut(rest, "/")
	f.mu.Lock()
	content, found := f.files[path]
	f.mu.Unlock()
	if !found {
		http.NotFound(writer, request)
		return
	}
	f.write(writer, content)
}

func (f *fakeTelegram) serveCall(writer http.ResponseWriter, request *http.Request, token, method string, body []byte) {
	f.mu.Lock()
	_, known := f.identities[token]
	refused := method == "getMe" && f.refused[token]
	if method == "getMe" {
		f.signIns[token]++
	}
	f.mu.Unlock()
	f.change.bump()
	if refused || !known {
		writer.WriteHeader(http.StatusUnauthorized)
		f.write(writer, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	if method == "getUpdates" {
		f.serveUpdates(writer, request, token)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, apiCall{token: token, method: method, body: body})
	code := f.fail[method]
	f.mu.Unlock()
	defer f.change.bump()
	if method == "editMessageText" && f.unmodified(body) {
		writer.WriteHeader(http.StatusBadRequest)
		f.write(writer, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`)
		return
	}
	if code != 0 {
		writer.WriteHeader(code)
		f.write(writer, `{"ok":false,"error_code":`+strconv.Itoa(code)+`,"description":"refused","parameters":{"retry_after":1}}`)
		return
	}
	f.answer(writer, f.result(method, token, body))
}

func (f *fakeTelegram) serveUpdates(writer http.ResponseWriter, request *http.Request, token string) {
	f.mu.Lock()
	code := f.fail["getUpdates"]
	f.polls[token]++
	f.mu.Unlock()
	f.change.bump()
	if code != 0 {
		defer f.change.bump()
		writer.WriteHeader(code)
		raw, err := json.Marshal(f.conflict)
		if err != nil {
			f.t.Errorf("encode the conflict: %v", err)
		}
		f.write(writer, `{"ok":false,"error_code":`+strconv.Itoa(code)+`,"description":`+string(raw)+`}`)
		return
	}
	f.answer(writer, f.drain(request.Context(), token))
}

func (f *fakeTelegram) unmodified(body []byte) bool {
	var edit tgctl.EditMessageText
	if err := json.Unmarshal(body, &edit); err != nil {
		f.t.Errorf("decode the edit: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.texts[edit.MessageID] == edit.Text && len(edit.Entities) == 0 {
		return true
	}
	f.texts[edit.MessageID] = edit.Text
	return false
}

func (f *fakeTelegram) drain(ctx context.Context, token string) []tgctl.Update {
	pushed := f.change.next()
	f.mu.Lock()
	waiting := len(f.updates[token]) > 0
	f.mu.Unlock()
	if !waiting {
		select {
		case <-ctx.Done():
		case <-pushed:
		case <-time.After(50 * time.Millisecond):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	queued := f.updates[token]
	delete(f.updates, token)
	if queued == nil {
		return []tgctl.Update{}
	}
	return queued
}

func (f *fakeTelegram) result(method, token string, body []byte) any {
	var target struct {
		ChatID int64  `json:"chat_id"`
		FileID string `json:"file_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(body, &target); err != nil {
		f.t.Errorf("decode %s: %v", method, err)
	}
	switch method {
	case "getMe":
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.identities[token]
	case "sendMessage":
		f.mu.Lock()
		f.nextID++
		sent := f.nextID
		f.texts[sent] = target.Text
		f.mu.Unlock()
		return tgctl.Message{MessageID: sent, Chat: tgctl.Chat{ID: target.ChatID}}
	case "getFile":
		return tgctl.File{FileID: target.FileID, FilePath: "documents/" + target.FileID + ".txt"}
	}
	return true
}

func (f *fakeTelegram) write(writer io.Writer, text string) {
	if _, err := io.WriteString(writer, text); err != nil {
		f.t.Errorf("write the fake answer: %v", err)
	}
}

func (f *fakeTelegram) answer(writer http.ResponseWriter, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		f.t.Errorf("encode fake result: %v", err)
	}
	writer.Header().Set("Content-Type", "application/json")
	f.write(writer, `{"ok":true,"result":`+string(raw)+`}`)
}

func (f *fakeTelegram) push(next ...tgctl.Update) {
	f.pushTo(testToken, next...)
}

func (f *fakeTelegram) pushTo(token string, next ...tgctl.Update) {
	f.mu.Lock()
	f.updates[token] = append(f.updates[token], next...)
	f.mu.Unlock()
	f.change.bump()
}

func (f *fakeTelegram) callsTo(method string) []apiCall {
	return f.callsWith("", method)
}

func (f *fakeTelegram) callsWith(token, method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []apiCall
	for _, call := range f.calls {
		if call.method == method && (token == "" || call.token == token) {
			found = append(found, call)
		}
	}
	return found
}

func (f *fakeTelegram) waitCalls(t *testing.T, method string, count int) []apiCall {
	t.Helper()
	waitUntil(t, method, f.change, func() bool { return len(f.callsTo(method)) >= count })
	return f.callsTo(method)
}

type fakeControl struct {
	mu          sync.Mutex
	sessions    []contracts.SessionInfo
	repos       []contracts.RepoRef
	voices      []contracts.AgentVoice
	created     []contracts.CreateSession
	submitted   map[string][]contracts.Inbound
	picks       []string
	interrupted []string
	repoCalls   int
	change      *signal
}

func newFakeControl() *fakeControl {
	return &fakeControl{submitted: make(map[string][]contracts.Inbound), change: newSignal()}
}

func (c *fakeControl) live(name, conv string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, contracts.SessionInfo{Name: name, ChannelID: conv, Gateways: []string{gatewayKind}})
}

func (c *fakeControl) AgentVoices(gateway string) []contracts.AgentVoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gateway != gatewayKind {
		return nil
	}
	out := make([]contracts.AgentVoice, len(c.voices))
	for index, voice := range c.voices {
		out[index] = voice
		out[index].Channels = slices.Clone(voice.Channels)
	}
	return out
}

func (c *fakeControl) Dispatch(context.Context, []string) (contracts.Result, error) {
	return contracts.Result{}, nil
}

func (c *fakeControl) Create(_ context.Context, spec contracts.CreateSession) (contracts.Result, error) {
	defer c.change.bump()
	c.mu.Lock()
	c.created = append(c.created, spec)
	c.mu.Unlock()
	c.live(spec.Name, spec.ChannelID)
	return contracts.Result{}, nil
}

func (c *fakeControl) Close(context.Context, string, bool) (contracts.Result, error) {
	return contracts.Result{}, nil
}

func (c *fakeControl) Sessions() []contracts.SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]contracts.SessionInfo(nil), c.sessions...)
}

func (c *fakeControl) Scrollback(string) []contracts.ScrollbackLine { return nil }

func (c *fakeControl) Resume(string) error { return nil }

func (c *fakeControl) Interrupt(name string) bool {
	defer c.change.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupted = append(c.interrupted, name)
	return true
}

func (c *fakeControl) Submit(name string, in contracts.Inbound) bool {
	defer c.change.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.sessions {
		if c.sessions[index].Name == name {
			c.submitted[name] = append(c.submitted[name], in)
			return true
		}
	}
	return false
}

func (c *fakeControl) Pick(name, value string) bool {
	defer c.change.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.picks = append(c.picks, name+"="+value)
	return true
}

func (c *fakeControl) Repos(context.Context) ([]contracts.RepoRef, error) {
	defer c.change.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repoCalls++
	return c.repos, nil
}

func (c *fakeControl) snapshot(read func(c *fakeControl) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return read(c)
}

func (c *fakeControl) waitFor(t *testing.T, what string, read func(c *fakeControl) bool) {
	t.Helper()
	waitUntil(t, what, c.change, func() bool { return c.snapshot(read) })
}

func startGateway(t *testing.T, fake *fakeTelegram, ctrl contracts.SessionControl, settings map[string]string) *Gateway {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	cfg := map[string]string{tokenSetting: testToken, ownerSetting: "42"}
	for key, value := range settings {
		cfg[key] = value
	}
	set, err := newGatewaySet(t.Context(), contracts.PluginConfig{Settings: cfg}, fake.server.URL, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	gateway, ok := set.Gateway.(*Gateway)
	if !ok {
		t.Fatalf("gateway is %T", set.Gateway)
	}
	if ctrl != nil {
		gateway.BindSessionControl(ctrl)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return gateway
}

func privateText(updateID, messageID, from int64, text string) tgctl.Update {
	return tgctl.Update{UpdateID: updateID, Message: &tgctl.Message{
		MessageID: messageID, From: &tgctl.User{ID: from, FirstName: "Ana", Username: "ana"},
		Chat: tgctl.Chat{ID: from, Type: chatTypePrivate}, Text: text,
	}}
}

func (f *fakeTelegram) waitSignIn(t *testing.T, token string) {
	t.Helper()
	waitUntil(t, "a sign in with the token", f.change, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.signIns[token] > 0
	})
}

func (f *fakeTelegram) waitVoicePoll(t *testing.T) {
	t.Helper()
	waitUntil(t, "a poll of the voice bot", f.change, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.polls[voiceToken] > 0
	})
}

func (f *fakeTelegram) pollsWith(token string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls[token]
}
