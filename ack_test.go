package telegram

import (
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
