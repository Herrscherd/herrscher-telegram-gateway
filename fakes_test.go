package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	testToken    = "123456:secret-token"
	testBotID    = 777
	testBotName  = "herrscher_bot"
	testOwner    = 42
	testStranger = 99
	testGroup    = -100500
	waitLimit    = 3 * time.Second
)

type apiCall struct {
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

type fakeTelegram struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	calls   []apiCall
	updates []update
	nextID  int64
	files   map[string]string
	fail    map[string]int
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	fake := &fakeTelegram{t: t, nextID: 1000, files: make(map[string]string), fail: make(map[string]int)}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeTelegram) serve(writer http.ResponseWriter, request *http.Request) {
	if path, ok := strings.CutPrefix(request.URL.Path, "/file/bot"+testToken+"/"); ok {
		f.mu.Lock()
		content, found := f.files[path]
		f.mu.Unlock()
		if !found {
			http.NotFound(writer, request)
			return
		}
		f.write(writer, content)
		return
	}
	method, ok := strings.CutPrefix(request.URL.Path, "/bot"+testToken+"/")
	if !ok {
		writer.WriteHeader(http.StatusNotFound)
		f.write(writer, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Errorf("read the request: %v", err)
	}
	if method == "getUpdates" {
		f.answer(writer, f.drain(request.Context()))
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, apiCall{method: method, body: body})
	code := f.fail[method]
	f.mu.Unlock()
	if code != 0 {
		writer.WriteHeader(code)
		f.write(writer, `{"ok":false,"error_code":`+strconv.Itoa(code)+`,"description":"refused","parameters":{"retry_after":1}}`)
		return
	}
	f.answer(writer, f.result(method, body))
}

func (f *fakeTelegram) drain(ctx context.Context) []update {
	deadline := time.NewTimer(20 * time.Millisecond)
	defer deadline.Stop()
	select {
	case <-ctx.Done():
	case <-deadline.C:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	queued := f.updates
	f.updates = nil
	if queued == nil {
		return []update{}
	}
	return queued
}

func (f *fakeTelegram) result(method string, body []byte) any {
	var target struct {
		ChatID int64  `json:"chat_id"`
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal(body, &target); err != nil {
		f.t.Errorf("decode %s: %v", method, err)
	}
	switch method {
	case "getMe":
		return botIdentity{ID: testBotID, Username: testBotName}
	case "sendMessage":
		f.mu.Lock()
		f.nextID++
		sent := f.nextID
		f.mu.Unlock()
		return message{MessageID: sent, Chat: chat{ID: target.ChatID}}
	case "getFile":
		return remoteFile{FileID: target.FileID, FilePath: "documents/" + target.FileID + ".txt"}
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

func (f *fakeTelegram) push(next ...update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, next...)
}

func (f *fakeTelegram) callsTo(method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []apiCall
	for _, call := range f.calls {
		if call.method == method {
			found = append(found, call)
		}
	}
	return found
}

func (f *fakeTelegram) waitCalls(t *testing.T, method string, count int) []apiCall {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if found := f.callsTo(method); len(found) >= count {
			return found
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waited for %d %s calls, got %d", count, method, len(f.callsTo(method)))
	return nil
}

type fakeControl struct {
	mu          sync.Mutex
	sessions    []contracts.SessionInfo
	repos       []contracts.RepoRef
	created     []contracts.CreateSession
	submitted   map[string][]contracts.Inbound
	picks       []string
	interrupted []string
}

func newFakeControl() *fakeControl {
	return &fakeControl{submitted: make(map[string][]contracts.Inbound)}
}

func (c *fakeControl) live(name, conv string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, contracts.SessionInfo{Name: name, ChannelID: conv, Gateways: []string{gatewayKind}})
}

func (c *fakeControl) Dispatch(context.Context, []string) (contracts.Result, error) {
	return contracts.Result{}, nil
}

func (c *fakeControl) Create(_ context.Context, spec contracts.CreateSession) (contracts.Result, error) {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupted = append(c.interrupted, name)
	return true
}

func (c *fakeControl) Submit(name string, in contracts.Inbound) bool {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.picks = append(c.picks, name+"="+value)
	return true
}

func (c *fakeControl) Repos(context.Context) ([]contracts.RepoRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.repos, nil
}

func (c *fakeControl) snapshot(read func(c *fakeControl) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return read(c)
}

func (c *fakeControl) waitFor(t *testing.T, what string, read func(c *fakeControl) bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if c.snapshot(read) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waited for %s", what)
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

func privateText(updateID, messageID, from int64, text string) update {
	return update{UpdateID: updateID, Message: &message{
		MessageID: messageID, From: &user{ID: from, FirstName: "Ana", Username: "ana"},
		Chat: chat{ID: from, Type: chatTypePrivate}, Text: text,
	}}
}
