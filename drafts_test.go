package telegram

import (
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

func TestADraftOfAFreshProcessNeverReusesAnIdOfAnEarlierOne(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	started := time.Now()
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	gateway.EmitTo(conv, humanAsking("go"))
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "working"})

	drafted := decodeCall[tgctl.MessageDraft](t, fake.waitCalls(t, "sendMessageDraft", 1)[0])
	if drafted.DraftID < started.UnixMilli() {
		t.Fatalf("draft id %d restarts from a counter, want one drawn from the clock at %d", drafted.DraftID, started.UnixMilli())
	}
}
