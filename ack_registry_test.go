package telegram

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const (
	sharedHouseConv = "-100500"
	sharedBobConv   = "-100500@bob"
	sharedMessageID = 10
)

func sharedAddresseeGateway(t *testing.T) (*fakeTelegram, *Gateway) {
	t.Helper()
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", sharedHouseConv)
	ctrl.live("tg-n100500-a-bob", sharedBobConv)
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyInChannels, sharedHouseConv)}
	gateway := startGateway(t, fake, ctrl, map[string]string{chatsSetting: sharedHouseConv})

	mention := "@" + testBotName
	addressed := inGroup(1, sharedMessageID, testOwner, mention+" what changed")
	addressed.Message.Entities = []tgctl.Entity{{Type: entityMention, Offset: 0, Length: len(mention)}}
	fake.push(addressed)
	ctrl.waitFor(t, "both addressees submitted", func(c *fakeControl) bool {
		return len(c.submitted["tg-n100500-a-bob"]) == 1 && len(c.submitted["tg-n100500"]) == 1
	})
	settleSharedMessage(t, gateway)
	return fake, gateway
}

func settleSharedMessage(t *testing.T, gateway *Gateway) {
	t.Helper()
	reached := make(chan struct{})
	if !gateway.lanes.push(gateway.life(), inboundLane+sharedHouseConv, func(context.Context) { close(reached) }) {
		t.Fatal("the inbound lane is closed")
	}
	select {
	case <-reached:
	case <-time.After(waitLimit):
		t.Fatal("the inbound lane never reached the barrier")
	}
	settle(t, gateway, sharedHouseConv)
	settle(t, gateway, sharedBobConv)
}

func endAddressee(gateway *Gateway, conv string, outcome contracts.TurnOutcome) {
	gateway.EmitTo(contracts.Conversation{Gateway: gatewayKind, ID: conv}, contracts.Event{T: eventReply, Done: true, Text: "done", Outcome: outcome})
}

func TestTheAckStaysWhileAnotherAddresseeOfTheMessageIsStillPending(t *testing.T) {
	fake, gateway := sharedAddresseeGateway(t)
	if got := lastReactionOn(t, fake, sharedMessageID); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("the shared ack is %+v, want %s once", got, ackEmoji)
	}
	if posted := len(fake.callsTo("setMessageReaction")); posted != 1 {
		t.Fatalf("the ack was set %d times, want once for the message", posted)
	}

	endAddressee(gateway, sharedBobConv, "")
	settle(t, gateway, sharedBobConv)
	if got := lastReactionOn(t, fake, sharedMessageID); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("bob's Done cleared the ack the house still needs: %+v", got)
	}

	endAddressee(gateway, sharedHouseConv, "")
	settle(t, gateway, sharedHouseConv)
	assertAckCleared(t, fake, sharedMessageID)
}

func TestTheFailureEmojiReplacesTheSharedAckWhenAnyAddresseeFailed(t *testing.T) {
	fake, gateway := sharedAddresseeGateway(t)

	endAddressee(gateway, sharedBobConv, contracts.TurnFailed)
	settle(t, gateway, sharedBobConv)
	if got := lastReactionOn(t, fake, sharedMessageID); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("bob's failure touched the ack the house still needs: %+v", got)
	}

	endAddressee(gateway, sharedHouseConv, "")
	settle(t, gateway, sharedHouseConv)
	if got := lastReactionOn(t, fake, sharedMessageID); len(got.Reaction) != 1 || got.Reaction[0].Emoji != failedEmoji || got.MessageID != sharedMessageID {
		t.Fatalf("the shared ack ends as %+v, want %s on message %d", got, failedEmoji, sharedMessageID)
	}
}

type stalledBot struct {
	bot

	armed   atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (b *stalledBot) SetMessageReaction(context.Context, int64, int64, string) error {
	if b.armed.Load() {
		close(b.started)
		<-b.release
	}
	return nil
}

type quietBot struct{ bot }

func (quietBot) SetMessageReaction(context.Context, int64, int64, string) error { return nil }

func TestAStalledReactionInOneConversationDoesNotBlockAnother(t *testing.T) {
	cases := map[string]func(reg *ackRegistry, stalled *stalledBot) <-chan struct{}{
		"open": func(reg *ackRegistry, stalled *stalledBot) <-chan struct{} {
			stalled.armed.Store(true)
			return goCall(func() { reg.open(context.Background(), ackKey{chat: 1, message: 10}, stalled) })
		},
		"close": func(reg *ackRegistry, stalled *stalledBot) <-chan struct{} {
			reg.open(context.Background(), ackKey{chat: 1, message: 10}, stalled)
			stalled.armed.Store(true)
			return goCall(func() { reg.close(context.Background(), ackKey{chat: 1, message: 10}, "") })
		},
	}
	for name, stall := range cases {
		t.Run(name, func(t *testing.T) {
			reg := newAckRegistry(slog.New(slog.DiscardHandler))
			stalled := &stalledBot{started: make(chan struct{}), release: make(chan struct{})}
			stalledDone := stall(reg, stalled)
			t.Cleanup(func() {
				close(stalled.release)
				<-stalledDone
			})
			select {
			case <-stalled.started:
			case <-time.After(waitLimit):
				t.Fatal("the stalled reaction never started")
			}

			otherDone := goCall(func() { reg.open(context.Background(), ackKey{chat: 2, message: 20}, quietBot{}) })
			select {
			case <-otherDone:
			case <-time.After(waitLimit):
				t.Fatal("conversation 2's ack waited for conversation 1's stalled Telegram call")
			}
		})
	}
}

func goCall(call func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	return done
}

type firstReactionBlockingBot struct {
	bot

	mu      sync.Mutex
	posted  []string
	entered chan struct{}
	release chan struct{}
}

func (b *firstReactionBlockingBot) SetMessageReaction(_ context.Context, _, _ int64, emoji string) error {
	b.mu.Lock()
	b.posted = append(b.posted, emoji)
	first := len(b.posted) == 1
	b.mu.Unlock()
	if first {
		close(b.entered)
		<-b.release
	}
	return nil
}

func (b *firstReactionBlockingBot) reactions() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.posted)
}

func TestAnOutcomeQueuesBehindTheAckInFlightOnItsMessage(t *testing.T) {
	reg := newAckRegistry(slog.New(slog.DiscardHandler))
	key := ackKey{chat: 1, message: sharedMessageID}
	bot := &firstReactionBlockingBot{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-bot.release:
		default:
			close(bot.release)
		}
	})

	openDone := goCall(func() { reg.open(context.Background(), key, bot) })
	select {
	case <-bot.entered:
	case <-time.After(waitLimit):
		t.Fatal("the ack never reached Telegram")
	}

	closeDone := goCall(func() { reg.close(context.Background(), key, "") })
	select {
	case <-closeDone:
	case <-time.After(waitLimit):
		t.Fatal("close waited on the ack in flight instead of queuing behind it")
	}
	if got := bot.reactions(); !slices.Equal(got, []string{ackEmoji}) {
		t.Fatalf("while the ack is in flight Telegram saw %q, want the ack alone", got)
	}

	close(bot.release)
	<-openDone
	if got := bot.reactions(); !slices.Equal(got, []string{ackEmoji, ""}) {
		t.Fatalf("message %d reacts %q, want the ack then its clear", sharedMessageID, got)
	}
}

type countingReactionBot struct {
	bot

	mu         sync.Mutex
	inFlight   map[int64]int
	overlapped bool
	posted     map[int64][]string
}

func (b *countingReactionBot) SetMessageReaction(_ context.Context, _, message int64, emoji string) error {
	b.mu.Lock()
	b.inFlight[message]++
	if b.inFlight[message] > 1 {
		b.overlapped = true
	}
	b.posted[message] = append(b.posted[message], emoji)
	b.mu.Unlock()

	runtime.Gosched()

	b.mu.Lock()
	b.inFlight[message]--
	b.mu.Unlock()
	return nil
}

const (
	concurrentSeeds   = 20
	concurrentWorkers = 8
	concurrentRounds  = 60
)

func TestTheReactionsOfOneMessageNeverOverlapAndKeepTheirOrder(t *testing.T) {
	for seed := range concurrentSeeds {
		reg, bot := hammerAckRegistry(uint64(seed))
		requireSerialReactions(t, seed, reg, bot)
	}
}

func hammerAckRegistry(seed uint64) (*ackRegistry, *countingReactionBot) {
	reg := newAckRegistry(slog.New(slog.DiscardHandler))
	bot := &countingReactionBot{inFlight: make(map[int64]int), posted: make(map[int64][]string)}
	var wg sync.WaitGroup
	for worker := range concurrentWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			playAckRounds(reg, bot, rand.New(rand.NewPCG(seed, uint64(worker))))
		}()
	}
	wg.Wait()
	return reg, bot
}

func playAckRounds(reg *ackRegistry, bot *countingReactionBot, rng *rand.Rand) {
	for range concurrentRounds {
		key := ackKey{chat: 1, message: int64(rng.IntN(3))}
		reg.open(context.Background(), key, bot)
		outcomes := [...]string{"", failedEmoji}
		reg.close(context.Background(), key, outcomes[rng.IntN(len(outcomes))])
	}
}

func requireSerialReactions(t *testing.T, seed int, reg *ackRegistry, bot *countingReactionBot) {
	t.Helper()
	if bot.overlapped {
		t.Fatalf("seed %d: two reactions on one message were in flight at once", seed)
	}
	if len(reg.entries) != 0 {
		t.Fatalf("seed %d: %d registry entries survive after every message went idle", seed, len(reg.entries))
	}
	for message, reactions := range bot.posted {
		requireAckThenOutcomes(t, seed, message, reactions)
	}
}

func requireAckThenOutcomes(t *testing.T, seed int, message int64, reactions []string) {
	t.Helper()
	for index, emoji := range reactions {
		if isAck := emoji == ackEmoji; isAck != (index%2 == 0) {
			t.Fatalf("seed %d message %d: reactions %q do not alternate ack then outcome", seed, message, reactions)
		}
	}
	if len(reactions)%2 != 0 {
		t.Fatalf("seed %d message %d: ack without its outcome in %q", seed, message, reactions)
	}
}
