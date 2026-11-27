package telegram

import (
	"context"
	"log/slog"
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

func lastReaction(t *testing.T, fake *fakeTelegram) reactionCall {
	t.Helper()
	calls := fake.callsTo("setMessageReaction")
	if len(calls) == 0 {
		t.Fatal("no reaction was set on the message")
	}
	return decodeCall[reactionCall](t, calls[len(calls)-1])
}

func TestTheAckStaysWhileAnotherAddresseeOfTheMessageIsStillPending(t *testing.T) {
	fake, gateway := sharedAddresseeGateway(t)
	if got := lastReaction(t, fake); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("the shared ack is %+v, want %s once", got, ackEmoji)
	}
	if posted := len(fake.callsTo("setMessageReaction")); posted != 1 {
		t.Fatalf("the ack was set %d times, want once for the message", posted)
	}

	endAddressee(gateway, sharedBobConv, "")
	settle(t, gateway, sharedBobConv)
	if got := lastReaction(t, fake); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
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
	if got := lastReaction(t, fake); len(got.Reaction) != 1 || got.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("bob's failure touched the ack the house still needs: %+v", got)
	}

	endAddressee(gateway, sharedHouseConv, "")
	settle(t, gateway, sharedHouseConv)
	if got := lastReaction(t, fake); len(got.Reaction) != 1 || got.Reaction[0].Emoji != failedEmoji || got.MessageID != sharedMessageID {
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
