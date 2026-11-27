package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
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

type deafSignIn struct {
	bot

	entered chan<- struct{}
	release <-chan struct{}
}

func (s deafSignIn) GetMe(context.Context) (tgctl.User, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return tgctl.User{}, errors.New("released")
}

type stuckSignIn struct {
	bot

	entered chan<- struct{}
	release <-chan struct{}
}

func (s stuckSignIn) GetMe(ctx context.Context) (tgctl.User, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return tgctl.User{}, ctx.Err()
	case <-s.release:
		return tgctl.User{}, errors.New("released")
	}
}

func unboundGateway(t *testing.T, fake *fakeTelegram, dial func(token string) bot, extra map[string]string) *Gateway {
	t.Helper()
	values := map[string]string{tokenSetting: testToken, ownerSetting: "42"}
	maps.Copy(values, extra)
	cfg := contracts.PluginConfig{Settings: values}
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

func closeAtCleanup(t *testing.T, gateway *Gateway) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
}

func TestTheHouseIsServedWhileAnAgentTokenIsStillSigningIn(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := unboundGateway(t, fake, func(token string) bot {
		return hangingSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL))}
	}, nil)
	closeAtCleanup(t, gateway)
	go gateway.BindSessionControl(ctrl)

	fake.push(privateText(1, 10, testOwner, "marker"))
	ctrl.waitFor(t, "the house message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
}

func TestBindSessionControlDoesNotWaitForASignIn(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := unboundGateway(t, fake, func(token string) bot {
		return hangingSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL))}
	}, nil)
	gateway.signInWait = 2 * time.Second
	closeAtCleanup(t, gateway)

	started := time.Now()
	gateway.BindSessionControl(ctrl)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the binding waited %s for a sign-in", elapsed)
	}
}

func TestASignInPastItsWindowIsRefusedAndTheAgentFallsBackToTheHouse(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.live("tg-42-a-ana", "42@ana")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := unboundGateway(t, fake, func(token string) bot {
		return hangingSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL))}
	}, nil)
	gateway.signInWait = 20 * time.Millisecond
	closeAtCleanup(t, gateway)
	gateway.BindSessionControl(ctrl)
	awaitSettled(t, gateway, "ana")

	if gateway.voices.hasOwn("ana") {
		t.Fatal("a sign-in past its window was admitted")
	}
	fake.push(privateText(1, 10, testOwner, "@ana review"))
	ctrl.waitFor(t, "the message routed to ana's session by the house", func(c *fakeControl) bool { return len(c.submitted["tg-42-a-ana"]) == 1 })
}

func TestCloseDuringASignInReturnsPromptlyAndStopsTheVoice(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gateway := unboundGateway(t, fake, func(token string) bot {
		return stuckSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL)), entered: entered, release: release}
	}, nil)
	go gateway.BindSessionControl(ctrl)
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("the sign-in never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- gateway.Close(ctx) }()
	select {
	case err := <-closed:
		if elapsed := time.Since(start); err != nil || elapsed > time.Second {
			t.Fatalf("close returned %v after %s during a sign-in, want nil within 1s", err, elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not return while a sign-in was pending")
	}
	if gateway.voices.hasOwn("ana") {
		t.Fatal("a voice was admitted after close")
	}
	if polls := fake.callsWith(voiceToken, "getUpdates"); len(polls) != 0 {
		t.Fatalf("the voice polled %d times after close", len(polls))
	}
}

func TestCloseReturnsAtItsDeadlineWhileASignInIgnoresItsContext(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gateway := unboundGateway(t, fake, func(token string) bot {
		return deafSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL)), entered: entered, release: release}
	}, nil)
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

func TestAnAddressedMessageInAnotherChatIsSubmittedWhileAnAgentSignInHangs(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := unboundGateway(t, fake, func(token string) bot {
		return hangingSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL))}
	}, map[string]string{chatsSetting: "-100500"})
	closeAtCleanup(t, gateway)
	go gateway.BindSessionControl(ctrl)

	batch := make([]tgctl.Update, 0, laneDepth+3)
	for index := range laneDepth + 2 {
		batch = append(batch, inGroup(int64(index+1), int64(index+10), testOwner, "@ana review"))
	}
	batch = append(batch, privateText(1000, 2000, testOwner, "marker"))
	started := time.Now()
	fake.push(batch...)
	ctrl.waitFor(t, "the marker in another chat", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a message in another chat waited %s behind an agent sign-in", elapsed)
	}
}

type pendingSignIn struct {
	gateway *Gateway
	release func()
}

func signingInGateway(t *testing.T, fake *fakeTelegram, ctrl *fakeControl, voices []contracts.AgentVoice, settings map[string]string) pendingSignIn {
	t.Helper()
	ctrl.voices = voices
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	gateway := unboundGateway(t, fake, func(token string) bot {
		return stuckSignIn{bot: tgctl.New(token, tgctl.WithBaseURL(fake.server.URL)), entered: entered, release: release}
	}, settings)
	closeAtCleanup(t, gateway)
	releaseSignIn := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSignIn)
	go gateway.BindSessionControl(ctrl)
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("the sign-in never started")
	}
	return pendingSignIn{gateway: gateway, release: releaseSignIn}
}

func namingAgentWhileSigningIn(t *testing.T) (*fakeTelegram, *fakeControl, *Gateway, func()) {
	t.Helper()
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.live("tg-42-a-ana", "42@ana")
	signing := signingInGateway(t, fake, ctrl, []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}, nil)
	return fake, ctrl, signing.gateway, signing.release
}

func awaitSettled(t *testing.T, gateway *Gateway, agent string) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for gateway.voices.signingIn(agent) {
		if time.Now().After(deadline) {
			t.Fatalf("the sign-in of %s never settled", agent)
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitLog(t *testing.T, logs *syncBuffer, substring string, count int) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for strings.Count(logs.String(), substring) < count {
		if time.Now().After(deadline) {
			t.Fatalf("the log never carried %d %q: %s", count, substring, logs.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAMessageNamingAnAgentStillSigningInIsDroppedWithoutASessionOrHeader(t *testing.T) {
	fake, ctrl, _, _ := namingAgentWhileSigningIn(t)

	fake.push(privateText(1, 10, testOwner, "@ana review"), privateText(2, 11, testOwner, "marker"))
	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if submitted := ctrl.snapshot(func(c *fakeControl) bool { return len(c.submitted["tg-42-a-ana"]) > 0 }); submitted {
		t.Fatal("a message naming an agent still signing in reached its session")
	}
	for _, call := range fake.callsTo("sendMessage") {
		if sent := decodeCall[tgctl.OutgoingMessage](t, call); strings.HasPrefix(sent.Text, "ana\n") {
			t.Fatalf("the house answered for ana under her header while her bot was signing in: %q", sent.Text)
		}
	}
}

func TestAMessageNamingAnAgentWhoseSignInFailedIsAnsweredByTheHouseUnderItsHeader(t *testing.T) {
	fake, ctrl, gateway, releaseSignIn := namingAgentWhileSigningIn(t)

	fake.push(privateText(1, 10, testOwner, "@ana review"), privateText(2, 11, testOwner, "marker"))
	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	releaseSignIn()
	awaitSettled(t, gateway, "ana")

	fake.push(privateText(3, 12, testOwner, "@ana review again"))
	ctrl.waitFor(t, "the message after the failed sign-in", func(c *fakeControl) bool { return len(c.submitted["tg-42-a-ana"]) == 1 })
	gateway.EmitTo(contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}, contracts.Event{T: eventReply, Done: true, Text: "hi"})

	sent := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if sent.Text != "ana\nhi" {
		t.Fatalf("house sent %q, want the answer under ana's header", sent.Text)
	}
}

func pendingGroupGateway(t *testing.T, voices []contracts.AgentVoice) (*fakeTelegram, *fakeControl) {
	t.Helper()
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.live("tg-g", "-100500")
	ctrl.live("tg-g-a-ana", "-100500@ana")
	ctrl.live("tg-g-a-bob", "-100500@bob")
	signingInGateway(t, fake, ctrl, voices, map[string]string{chatsSetting: "-100500"})
	return fake, ctrl
}

func TestAReadyTokenlessAgentAnswersAMessageThatAlsoNamesAPendingAgent(t *testing.T) {
	fake, ctrl := pendingGroupGateway(t, []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention),
		voiceOf(t, "bob", "", contracts.ReplyOnMention),
	})

	fake.push(inGroup(1, 10, testOwner, "@ana @bob hello"))
	ctrl.waitFor(t, "bob answers the message that also names ana", func(c *fakeControl) bool { return len(c.submitted["tg-g-a-bob"]) == 1 })
}

func TestAnExplicitHouseMentionIsAnsweredInAChannelHeldByAPendingAgent(t *testing.T) {
	fake, ctrl := pendingGroupGateway(t, []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyInChannels, "-100500"),
	})

	fake.push(inGroup(1, 10, testOwner, "@"+testBotName+" hello"))
	ctrl.waitFor(t, "the house answers an explicit mention", func(c *fakeControl) bool { return len(c.submitted["tg-g"]) == 1 })
}

func TestAReadyChannelAgentCapturesAChatThatAPendingAgentAlsoHolds(t *testing.T) {
	fake, ctrl := pendingGroupGateway(t, []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyInChannels, "-100500"),
		voiceOf(t, "bob", "", contracts.ReplyInChannels, "-100500"),
	})

	fake.push(inGroup(1, 10, testOwner, "hello there"))
	ctrl.waitFor(t, "bob captures the channel", func(c *fakeControl) bool { return len(c.submitted["tg-g-a-bob"]) == 1 })
}

func TestAPendingAgentWithReplyNeverHoldsNothingBack(t *testing.T) {
	fake, ctrl := pendingGroupGateway(t, []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyNever, "-100500"),
	})

	reply := inGroup(1, 10, testOwner, "@ana hello there")
	reply.Message.ReplyTo = &tgctl.Message{MessageID: 5, From: &tgctl.User{ID: testBotID, IsBot: true}}
	fake.push(reply)
	ctrl.waitFor(t, "the house answers the reply", func(c *fakeControl) bool { return len(c.submitted["tg-g"]) == 1 })
}

func TestTwoAgentsSharingATokenOrTheHouseBotSkipTheDuplicateOnce(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention),
		voiceOf(t, "bob", voiceToken, contracts.ReplyOnMention),
		voiceOf(t, "carl", testToken, contracts.ReplyOnMention),
	}
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
	closeAtCleanup(t, gateway)

	go gateway.BindSessionControl(ctrl)
	awaitLog(t, &logs, "agent bot skipped", 2)

	if gateway.voices.hasOwn("ana") == gateway.voices.hasOwn("bob") {
		t.Fatalf("two voices sharing one token: ana=%v bob=%v, want exactly one", gateway.voices.hasOwn("ana"), gateway.voices.hasOwn("bob"))
	}
	if gateway.voices.hasOwn("carl") {
		t.Fatal("a voice whose token is the house bot's was registered")
	}
	if skipped := strings.Count(logs.String(), "agent bot skipped"); skipped != 2 {
		t.Fatalf("skipped %d agent bots, want the two duplicates logged once each: %s", skipped, logs.String())
	}
	for _, secret := range []string{voiceToken, testToken} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries a token: %s", logs.String())
		}
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
	closeAtCleanup(t, gateway)

	go gateway.BindSessionControl(ctrl)
	awaitLog(t, &logs, "agent bot skipped", 1)

	for _, secret := range []string{refusedToken, "refused-token"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries the token %q: %s", secret, logs.String())
		}
	}
}
