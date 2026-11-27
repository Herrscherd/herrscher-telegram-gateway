package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

func TestASinkOpenedBeforeItsVoiceBindsSpeaksThroughTheVoiceOnceBound(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, nil, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman, Who: "ana", Text: "ask"})
	gateway.BindSessionControl(ctrl)
	fake.waitVoicePoll(t)
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "hi"})

	fake.waitCalls(t, "sendMessage", 1)
	if house := fake.callsWith(testToken, "sendMessage"); len(house) != 0 {
		t.Fatalf("the house bot sent %d messages for a voice with a valid token", len(house))
	}
	own := fake.callsWith(voiceToken, "sendMessage")
	if len(own) != 1 || decodeCall[tgctl.OutgoingMessage](t, own[0]).Text != "hi" {
		t.Fatalf("own bot sent %+v", own)
	}
}

func TestAVoiceWhoseTokenCannotSignInSpeaksThroughTheHouseUnderItsHeader(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ghost", refusedToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)

	gateway.EmitTo(contracts.Conversation{Gateway: gatewayKind, ID: "42@ghost"}, contracts.Event{T: eventReply, Done: true, Text: "hi"})

	sent := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if sent.Text != "ghost\nhi" {
		t.Fatalf("house sent %q", sent.Text)
	}
}

func TestAStopInOnePrivateChatInterruptsOnlyTheTokenlessVoiceThatDrafted(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42-a-ana", "42@ana")
	ctrl.live("tg-42-bob", "42@bob")
	ctrl.voices = []contracts.AgentVoice{
		voiceOf(t, "ana", "", contracts.ReplyOnMention),
		voiceOf(t, "bob", "", contracts.ReplyOnMention),
	}
	gateway := startGateway(t, fake, ctrl, nil)
	ana := contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}
	bob := contracts.Conversation{Gateway: gatewayKind, ID: "42@bob"}
	gateway.EmitTo(ana, contracts.Event{T: eventChunk, Text: "one"})
	gateway.EmitTo(bob, contracts.Event{T: eventChunk, Text: "two"})
	anaDraft := draftOf(t, fake.waitCalls(t, "sendMessageDraft", 2), "ana")

	before := fake.pollsWith(testToken)
	fake.push(tgctl.Update{UpdateID: 1, StoppedMessageGeneration: &tgctl.GenerationStopped{Chat: tgctl.Chat{ID: testOwner, Type: chatTypePrivate}, DraftID: anaDraft}})
	waitUntil(t, "the house poll after the stop", fake.change, func() bool { return fake.pollsWith(testToken) >= before+2 })
	flushLane(t, gateway, ana.ID)
	flushLane(t, gateway, bob.ID)

	if got := ctrl.interrupted; !slices.Equal(got, []string{"tg-42-a-ana"}) {
		t.Fatalf("interrupted %v, want only the ana session", got)
	}
}

func flushLane(t *testing.T, gateway *Gateway, address string) {
	t.Helper()
	done := make(chan struct{})
	if !gateway.lanes.push(t.Context(), outboundLane+address, func(context.Context) { close(done) }) {
		t.Fatalf("lane of %s refused the barrier", address)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatalf("lane of %s never ran the barrier", address)
	}
}

func draftOf(t *testing.T, drafts []apiCall, agent string) int64 {
	t.Helper()
	for _, call := range drafts {
		if draft := decodeCall[tgctl.MessageDraft](t, call); strings.HasPrefix(draft.Text, agent+"\n") {
			return draft.DraftID
		}
	}
	t.Fatalf("no draft for %s among %d", agent, len(drafts))
	return 0
}
