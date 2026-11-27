package telegram

import (
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestTheAckIsClearedWhenAPromptEndsWithoutATurn(t *testing.T) {
	cases := map[string]struct {
		event contracts.Event
		late  bool
	}{
		"abandoned before the ack":  {event: contracts.Event{T: eventAbandoned}},
		"reply done before the ack": {event: contracts.Event{T: eventReply, Done: true, Text: "done"}},
		"abandoned after the ack":   {event: contracts.Event{T: eventAbandoned}, late: true},
		"reply done after the ack":  {event: contracts.Event{T: eventReply, Done: true, Text: "done"}, late: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeTelegram(t)
			ctrl := &emittingControl{fakeControl: newFakeControl()}
			ctrl.live("tg-42", "42")
			gateway := startGateway(t, fake, ctrl, nil)
			conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
			ctrl.during = func() {}
			if !tc.late {
				ctrl.during = func() { gateway.EmitTo(conv, tc.event) }
			}

			fake.push(privateText(1, 10, testOwner, "go"))
			fake.waitCalls(t, "setMessageReaction", 1)
			if tc.late {
				gateway.EmitTo(conv, tc.event)
			}
			settle(t, gateway, conv.ID)
			assertAckCleared(t, fake, 10)
		})
	}
}

func assertAckCleared(t *testing.T, fake *fakeTelegram, messageID int64) {
	t.Helper()
	calls := fake.callsTo("setMessageReaction")
	if len(calls) == 0 {
		t.Fatal("no reaction was set on the message")
	}
	last := decodeCall[reactionCall](t, calls[len(calls)-1])
	if last.MessageID != messageID || len(last.Reaction) != 0 {
		t.Fatalf("the last reaction on message %d is %+v, want the ack cleared", messageID, last)
	}
}

type refusingControl struct {
	*fakeControl

	refusing atomic.Bool
}

func (c *refusingControl) Submit(name string, in contracts.Inbound) bool {
	if c.refusing.Load() {
		return false
	}
	return c.fakeControl.Submit(name, in)
}

func TestARefusedNoteIsWithdrawnAndALaterMessageStillBinds(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := &refusingControl{fakeControl: newFakeControl()}
	ctrl.live("tg-42", "42")
	ctrl.refusing.Store(true)
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	target := gateway.sinkFor(address{chatRef: chatRef{chat: testOwner}})

	gateway.EmitTo(conv, humanAsking("go"))
	settle(t, gateway, conv.ID)
	fake.push(privateText(1, 10, testOwner, "go"))
	fake.waitCalls(t, "sendMessage", 1)
	settle(t, gateway, conv.ID)
	if len(target.prompts) != 0 || target.turn == nil || target.turn.prompt != 0 {
		t.Fatalf("a refused note is still bound: prompts %+v, turn %+v", target.prompts, target.turn)
	}

	ctrl.refusing.Store(false)
	fake.push(privateText(2, 11, testOwner, "go"))
	ctrl.waitFor(t, "the second message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	reaction := decodeCall[reactionCall](t, fake.waitCalls(t, "setMessageReaction", 1)[0])
	if reaction.MessageID != 11 || len(reaction.Reaction) != 1 || reaction.Reaction[0].Emoji != ackEmoji {
		t.Fatalf("the accepted message did not bind: reaction %+v", reaction)
	}
}

func TestARefusedMessageNeverGetsTheAckEmoji(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := &refusingControl{fakeControl: newFakeControl()}
	ctrl.live("tg-42", "42")
	ctrl.refusing.Store(true)
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	gateway.EmitTo(conv, humanAsking("go"))
	settle(t, gateway, conv.ID)
	fake.push(privateText(1, 10, testOwner, "go"))
	fake.waitCalls(t, "sendMessage", 1)
	settle(t, gateway, conv.ID)
	for _, call := range fake.callsTo("setMessageReaction") {
		if reaction := decodeCall[reactionCall](t, call); len(reaction.Reaction) == 1 && reaction.Reaction[0].Emoji == ackEmoji {
			t.Fatalf("a refused message got the ack emoji: %+v", reaction)
		}
	}
}

type emittingControl struct {
	*fakeControl

	during func()
}

func (c *emittingControl) Submit(name string, in contracts.Inbound) bool {
	c.during()
	return c.fakeControl.Submit(name, in)
}

func TestTheAckArrivesWhenTheTurnEndsBeforeTheAcceptedJobRuns(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := &emittingControl{fakeControl: newFakeControl()}
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	ctrl.during = func() {
		gateway.EmitTo(conv, humanAsking("go"))
		gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "done"})
	}

	fake.push(privateText(1, 10, testOwner, "go"))
	calls := fake.waitCalls(t, "setMessageReaction", 2)
	ack := decodeCall[reactionCall](t, calls[0])
	if len(ack.Reaction) != 1 || ack.Reaction[0].Emoji != ackEmoji || ack.MessageID != 10 {
		t.Fatalf("the ack went %+v, want %s on message 10", ack, ackEmoji)
	}
	swap := decodeCall[reactionCall](t, calls[1])
	if len(swap.Reaction) != 0 || swap.MessageID != 10 {
		t.Fatalf("the ack was not cleared after the turn ended: %+v", swap)
	}
}

func TestTheFailureEmojiReplacesTheAckWhenTheTurnFailsBeforeTheAcceptedJobRuns(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := &emittingControl{fakeControl: newFakeControl()}
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	ctrl.during = func() {
		gateway.EmitTo(conv, humanAsking("go"))
		gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "done", Outcome: contracts.TurnFailed})
	}

	fake.push(privateText(1, 10, testOwner, "go"))
	calls := fake.waitCalls(t, "setMessageReaction", 2)
	swap := decodeCall[reactionCall](t, calls[1])
	if len(swap.Reaction) != 1 || swap.Reaction[0].Emoji != failedEmoji || swap.MessageID != 10 {
		t.Fatalf("the outcome after the ack went %+v, want %s on message 10", swap, failedEmoji)
	}
}

func TestTheAckIsClearedWhenTheTurnIsAbandonedBeforeTheAcceptedJobRuns(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := &emittingControl{fakeControl: newFakeControl()}
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	ctrl.during = func() {
		gateway.EmitTo(conv, humanAsking("go"))
		gateway.EmitTo(conv, contracts.Event{T: eventAbandoned})
	}

	fake.push(privateText(1, 10, testOwner, "go"))
	calls := fake.waitCalls(t, "setMessageReaction", 2)
	swap := decodeCall[reactionCall](t, calls[1])
	if len(swap.Reaction) != 0 || swap.MessageID != 10 {
		t.Fatalf("the ack after an abandoned turn was not cleared: %+v", swap)
	}
}
