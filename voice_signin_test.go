package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

type hangingSignIn struct {
	bot
}

func (hangingSignIn) GetMe(ctx context.Context) (tgctl.User, error) {
	<-ctx.Done()
	return tgctl.User{}, ctx.Err()
}

type stuckSignIn struct {
	bot

	entered chan<- struct{}
	release <-chan struct{}
}

func (s stuckSignIn) GetMe(context.Context) (tgctl.User, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return tgctl.User{}, errors.New("released")
}

func unboundGateway(t *testing.T, fake *fakeTelegram, dial func(token string) bot) *Gateway {
	t.Helper()
	cfg := contracts.PluginConfig{Settings: map[string]string{tokenSetting: testToken, ownerSetting: "42"}}
	set, err := newGatewaySet(t.Context(), cfg, fake.server.URL, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	gateway, ok := set.Gateway.(*Gateway)
	if !ok {
		t.Fatalf("gateway is %T", set.Gateway)
	}
	gateway.dial = dial
	return gateway
}

func TestTheHouseIsServedWhileAnAgentTokenIsStillSigningIn(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := unboundGateway(t, fake, func(token string) bot {
		return hangingSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL))}
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	go gateway.BindSessionControl(ctrl)

	fake.push(privateText(1, 10, testOwner, "marker"))
	ctrl.waitFor(t, "the house message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
}

func TestCloseReturnsAtItsDeadlineWhileASignInIgnoresItsContext(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gateway := unboundGateway(t, fake, func(token string) bot {
		return stuckSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL)), entered: entered, release: release}
	})
	go gateway.BindSessionControl(ctrl)
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("the sign-in never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- gateway.Close(ctx) }()
	select {
	case err := <-closed:
		if elapsed := time.Since(start); err == nil || elapsed > 2*time.Second {
			t.Fatalf("close returned %v after %s, want its deadline error within 2s", err, elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not return while a sign-in hung")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestASignInFailureNeverPutsTheTokenInTheLogs(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ghost", refusedToken, contracts.ReplyOnMention)}
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachable.Close()
	var logs syncBuffer
	cfg := contracts.PluginConfig{Settings: map[string]string{tokenSetting: testToken, ownerSetting: "42"}}
	set, err := newGatewaySet(t.Context(), cfg, fake.server.URL, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("build the gateway: %v", err)
	}
	gateway, ok := set.Gateway.(*Gateway)
	if !ok {
		t.Fatalf("gateway is %T", set.Gateway)
	}
	gateway.dial = func(token string) bot { return tgctl.New(token, tgctl.WithBaseURL(unreachable.URL)) }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	gateway.BindSessionControl(ctrl)

	if !strings.Contains(logs.String(), "agent bot skipped") {
		t.Fatalf("the failed sign-in was not logged: %q", logs.String())
	}
	for _, secret := range []string{refusedToken, "refused-token"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries the token %q: %s", secret, logs.String())
		}
	}
}
