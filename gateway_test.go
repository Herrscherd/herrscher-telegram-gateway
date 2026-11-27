package telegram

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

func inGroup(updateID, messageID, from int64, text string) tgctl.Update {
	next := privateText(updateID, messageID, from, text)
	next.Message.Chat = tgctl.Chat{ID: testGroup, Type: chatTypeSuper}
	if mention, found := strings.CutPrefix(text, "@"+testBotName); found {
		next.Message.Text = "@" + testBotName + mention
		next.Message.Entities = []tgctl.Entity{{Type: entityMention, Offset: 0, Length: len(testBotName) + 1}}
	}
	return next
}

func pressOn(updateID, from int64, on *tgctl.Message, data string) tgctl.Update {
	return tgctl.Update{UpdateID: updateID, CallbackQuery: &tgctl.CallbackQuery{ID: "q" + formatID(updateID), From: tgctl.User{ID: from}, Message: on, Data: data}}
}

func humanAsking(text string) contracts.Event {
	return contracts.Event{T: eventHuman, Who: "ana", Text: text}
}

func laneKeys(gateway *Gateway) []string {
	gateway.lanes.mu.Lock()
	defer gateway.lanes.mu.Unlock()
	keys := make([]string, 0, len(gateway.lanes.queues))
	for key := range gateway.lanes.queues {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestAMessageFromTheOwnerReachesTheSessionOfItsChat(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	startGateway(t, fake, ctrl, nil)

	fake.push(privateText(1, 10, testOwner, "refactor the loader"))

	ctrl.waitFor(t, "the submitted message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	got := ctrl.submitted["tg-42"][0]
	if got.Text != "refactor the loader" || got.AuthorID != "42" || got.MessageID != "10" || got.Conversation.ID != "42" {
		t.Fatalf("inbound = %+v", got)
	}
}

func TestARefusedMessageCostsNothingAndReachesNoSession(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "alpha", Local: true}, {Name: "beta", Local: true}}
	ctrl.live("tg-n100500", "-100500")
	ctrl.live("tg-n100600", "-100600")
	gateway := startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	robot := privateText(2, 11, testOwner, "hi")
	robot.Message.From.IsBot = true
	unlisted := inGroup(3, 12, testOwner, "@"+testBotName+" hi")
	unlisted.Message.Chat.ID = -100600
	fake.push(
		privateText(1, 10, testStranger, "hi"),
		robot,
		unlisted,
		inGroup(4, 13, testStranger, "@"+testBotName+" run this"),
		inGroup(5, 14, testOwner, "@"+testBotName+" last"),
	)

	ctrl.waitFor(t, "the owner's group message", func(c *fakeControl) bool { return len(c.submitted["tg-n100500"]) > 0 })
	if got := ctrl.submitted["tg-n100500"]; len(got) != 1 || got[0].Text != "last" {
		t.Fatalf("group session got %+v", got)
	}
	if len(ctrl.submitted["tg-n100600"]) != 0 || ctrl.repoCalls != 0 || len(ctrl.created) != 0 {
		t.Fatalf("a refused message did work: repos %d, created %v", ctrl.repoCalls, ctrl.created)
	}
	if keys := laneKeys(gateway); !slices.Equal(keys, []string{"in/-100500", "out/-100500"}) {
		t.Fatalf("lanes = %v, a refused message was queued", keys)
	}
	if sent := fake.callsTo("sendMessage"); len(sent) != 0 {
		t.Fatalf("a refused message was answered: %d messages", len(sent))
	}
}

func TestAGroupAnswersOnlyWhenTheBotIsAddressed(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	mention := "@" + testBotName
	middle := inGroup(2, 11, testOwner, "é "+mention+" look")
	middle.Message.Entities = []tgctl.Entity{{Type: entityMention, Offset: 2, Length: len(mention)}}
	reply := inGroup(3, 12, testOwner, "and this")
	reply.Message.ReplyTo = &tgctl.Message{MessageID: 5, From: &tgctl.User{ID: testBotID, IsBot: true}}
	fake.push(inGroup(1, 10, testOwner, "chatter between people"), middle, reply)

	ctrl.waitFor(t, "two addressed messages", func(c *fakeControl) bool { return len(c.submitted["tg-n100500"]) == 2 })
	got := ctrl.submitted["tg-n100500"]
	if got[0].Text != "é  look" || got[1].Text != "and this" {
		t.Fatalf("texts = %q, %q", got[0].Text, got[1].Text)
	}
}

func TestAMessageWithNothingToSayStartsNoTurn(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	startGateway(t, fake, ctrl, nil)

	sticker := privateText(1, 10, testOwner, "")
	refused := privateText(2, 11, testOwner, "")
	refused.Message.Document = &tgctl.Document{FileID: "big", FileName: "big.txt", FileSize: attachmentMaxBytes + 1}
	fake.push(sticker, refused, privateText(3, 12, testOwner, "next"))

	ctrl.waitFor(t, "the text message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if got := ctrl.submitted["tg-42"][0].Text; got != "next" {
		t.Fatalf("submitted %q", got)
	}
	told := fake.callsTo("sendMessage")
	if len(told) != 1 || !strings.Contains(decodeCall[tgctl.OutgoingMessage](t, told[0]).Text, "not accepted") || len(fake.callsTo("getFile")) != 0 {
		t.Fatalf("the refused file was not explained once: %d messages", len(told))
	}
}

func TestANewChatPicksItsRepositoryThenGetsTheBufferedMessage(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "alpha", Local: true}, {Name: "org/beta"}}
	startGateway(t, fake, ctrl, map[string]string{usersSetting: "99"})

	fake.push(privateText(1, 10, testOwner, "start here"))
	menu := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	second := menu.ReplyMarkup.Rows[1][0]
	if second.Text != "org/beta" || len(second.CallbackData) > 64 {
		t.Fatalf("second button = %+v", second)
	}

	pressed := &tgctl.Message{MessageID: 1001, Chat: tgctl.Chat{ID: testOwner, Type: chatTypePrivate}}
	fake.push(pressOn(2, testStranger, pressed, second.CallbackData))
	answer := decodeCall[tgctl.CallbackAnswer](t, fake.waitCalls(t, "answerCallbackQuery", 1)[0])
	if answer.Text != "Only the person who asked can pick." {
		t.Fatalf("someone else's press answered %+v", answer)
	}

	fake.push(pressOn(3, testOwner, pressed, second.CallbackData))
	ctrl.waitFor(t, "the buffered message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	spec := ctrl.created[0]
	if spec.Name != "tg-42" || spec.ChannelID != "42" || spec.Clone != "org/beta" || spec.Gateways[0] != gatewayKind {
		t.Fatalf("created %+v", spec)
	}
	if got := ctrl.submitted["tg-42"][0].Text; got != "start here" {
		t.Fatalf("buffered text = %q", got)
	}
	if answers := fake.callsTo("answerCallbackQuery"); len(answers) != 2 {
		t.Fatalf("%d presses answered", len(answers))
	}
}

func TestAMenuPressIsAPickOnlyFromAnAllowedUser(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	gateway := startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	conv := contracts.Conversation{Gateway: gatewayKind, ID: "-100500"}
	if err := gateway.Menu(t.Context(), conv, "10", "Run the migration?", []contracts.Choice{{Label: "Yes", Value: "allow"}, {Label: "No", Value: "deny"}}); err != nil {
		t.Fatalf("menu: %v", err)
	}
	rows := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0]).ReplyMarkup.Rows
	elsewhere := &tgctl.Message{MessageID: 1001, Chat: tgctl.Chat{ID: testOwner, Type: chatTypePrivate}}
	fake.push(pressOn(1, testOwner, elsewhere, rows[0][0].CallbackData))
	if got := decodeCall[tgctl.CallbackAnswer](t, fake.waitCalls(t, "answerCallbackQuery", 1)[0]).Text; got != "This choice is no longer waiting." {
		t.Fatalf("a press from another chat was answered %q", got)
	}
	pressed := &tgctl.Message{MessageID: 1001, Chat: tgctl.Chat{ID: testGroup, Type: chatTypeSuper}}
	fake.push(pressOn(2, testStranger, pressed, rows[0][0].CallbackData), pressOn(3, testOwner, pressed, rows[1][0].CallbackData))

	ctrl.waitFor(t, "the pick", func(c *fakeControl) bool { return len(c.picks) > 0 })
	if !slices.Equal(ctrl.picks, []string{"tg-n100500=deny"}) {
		t.Fatalf("picks = %v", ctrl.picks)
	}
	answers := fake.waitCalls(t, "answerCallbackQuery", 3)
	if got := decodeCall[tgctl.CallbackAnswer](t, answers[1]).Text; got != "You are not allowed to answer this." {
		t.Fatalf("the stranger was answered %q", got)
	}
	if len(fake.callsTo("editMessageReplyMarkup")) != 1 {
		t.Fatal("the menu kept its buttons")
	}
}

func TestStopInterruptsOnlyTheTurnStillRunning(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, map[string]string{usersSetting: "99"})
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	stop := func(updateID, chatID, draftID int64) tgctl.Update {
		return tgctl.Update{UpdateID: updateID, StoppedMessageGeneration: &tgctl.GenerationStopped{Chat: tgctl.Chat{ID: chatID, Type: chatTypePrivate}, DraftID: draftID}}
	}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	fake.push(stop(1, testOwner, 5), stop(2, 7, 1), stop(3, testOwner, 1))
	ctrl.waitFor(t, "the interrupt", func(c *fakeControl) bool { return len(c.interrupted) > 0 })
	fake.push(privateText(4, 10, testOwner, "/stop@"+testBotName))
	if got := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0]).Text; got != "Stopping the turn." {
		t.Fatalf("/stop answered %q", got)
	}

	if !slices.Equal(ctrl.interrupted, []string{"tg-42", "tg-42"}) || len(ctrl.submitted["tg-42"]) != 0 {
		t.Fatalf("interrupted %v, submitted %v", ctrl.interrupted, ctrl.submitted)
	}
}

func TestEachTurnRepliesToTheMessageThatStartedIt(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	fake.push(privateText(1, 10, testOwner, "first"))
	ctrl.waitFor(t, "the first message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	gateway.EmitTo(conv, humanAsking("first"))
	fake.push(privateText(2, 11, testOwner, "refused by the budget"), privateText(3, 12, testOwner, "second"), privateText(4, 13, testOwner, "third"))
	ctrl.waitFor(t, "the queued messages", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 4 })
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "answer one"})
	for _, asked := range []string{"second", "from the terminal", "third"} {
		gateway.EmitTo(conv, humanAsking(asked))
		gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "answer to " + asked})
	}

	sent := fake.waitCalls(t, "sendMessage", 4)
	for index, want := range []int64{10, 12, 0, 13} {
		out := decodeCall[tgctl.OutgoingMessage](t, sent[index])
		if want == 0 {
			if out.ReplyParameters != nil {
				t.Fatalf("answer %d to a prompt from elsewhere replies to %d", index, out.ReplyParameters.MessageID)
			}
			continue
		}
		if out.ReplyParameters == nil || out.ReplyParameters.MessageID != want {
			t.Fatalf("answer %d = %+v, want a reply to %d", index, out, want)
		}
	}
}

func TestARetypedPromptAfterAnAbandonedCopyRepliesToTheNewOne(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	fake.push(privateText(1, 10, testOwner, "continue"), privateText(2, 11, testOwner, "continue"))
	ctrl.waitFor(t, "both copies", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 2 })
	settle(t, gateway, conv.ID)
	gateway.EmitTo(conv, contracts.Event{T: eventAbandoned})
	gateway.EmitTo(conv, humanAsking("continue"))
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "going on"})

	answer := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if answer.ReplyParameters == nil || answer.ReplyParameters.MessageID != 11 {
		t.Fatalf("the answer replies to %+v, want 11", answer.ReplyParameters)
	}
}

func TestAPrivateTurnStreamsADraftThenRepliesWithEntities(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	gateway.EmitTo(conv, humanAsking("go"))
	settle(t, gateway, conv.ID)
	fake.push(privateText(1, 10, testOwner, "go"))
	ctrl.waitFor(t, "the message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "working on **it**"})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "Done with **it**"})

	drafted := decodeCall[tgctl.MessageDraft](t, fake.waitCalls(t, "sendMessageDraft", 1)[0])
	if drafted.Text != "working on it" || !drafted.CanStop || drafted.DraftID == 0 {
		t.Fatalf("draft = %+v", drafted)
	}
	answer := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if answer.Text != "Done with it" || len(answer.Entities) != 1 || answer.Entities[0] != (tgctl.Entity{Type: entityBold, Offset: 10, Length: 2}) {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.ReplyParameters == nil || answer.ReplyParameters.MessageID != 10 {
		t.Fatalf("a turn that began before its message was noted does not reply to it: %+v", answer)
	}
	calls := fake.waitCalls(t, "setMessageReaction", 2)
	acked, cleared := decodeCall[reactionCall](t, calls[0]), decodeCall[reactionCall](t, calls[1])
	if len(acked.Reaction) != 1 || acked.Reaction[0].Emoji != ackEmoji || acked.MessageID != 10 || cleared.Reaction == nil || len(cleared.Reaction) != 0 {
		t.Fatalf("reactions = %+v then %+v", acked, cleared)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(step)
}

func settle(t *testing.T, gateway *Gateway, conv string) {
	t.Helper()
	reached := make(chan struct{})
	if !gateway.lanes.push(gateway.life(), outboundLane+conv, func(context.Context) { close(reached) }) {
		t.Fatal("the lane is closed")
	}
	select {
	case <-reached:
	case <-time.After(waitLimit):
		t.Fatal("the lane never reached the barrier")
	}
}

func TestADraftIsSentAtMostOncePerInterval(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, nil)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	gateway.now = clock.read
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "a"})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "b"})
	settle(t, gateway, conv.ID)
	clock.advance(draftInterval - time.Millisecond)
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "c"})
	settle(t, gateway, conv.ID)
	clock.advance(time.Millisecond)
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "d"})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "abcd"})
	fake.waitCalls(t, "sendMessage", 1)

	drafts := fake.callsTo("sendMessageDraft")
	if len(drafts) != 2 || decodeCall[tgctl.MessageDraft](t, drafts[0]).Text != "a" || decodeCall[tgctl.MessageDraft](t, drafts[1]).Text != "abcd" {
		t.Fatalf("%d drafts", len(drafts))
	}
}

func TestAGroupTurnEditsOneLiveMessageAndSaysWhenItFailed(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, map[string]string{chatsSetting: "-100500"})
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "-100500:7"}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "partial"})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Outcome: contracts.TurnFailed, Text: "half an answer"})

	live := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if live.Text != "partial" || live.ThreadID != 7 || live.ChatID != testGroup {
		t.Fatalf("live message = %+v", live)
	}
	final := decodeCall[tgctl.EditMessageText](t, fake.waitCalls(t, "editMessageText", 1)[0])
	if final.Text != "half an answer\n\nThe turn failed." || final.MessageID != 1001 {
		t.Fatalf("final edit = %+v", final)
	}
}

func TestAGroupAnswerAlreadyShownIsNotPostedTwice(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, map[string]string{chatsSetting: "-100500"})
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "-100500"}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "Done."})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "Done."})
	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "Second."})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "Second"})
	fake.waitCalls(t, "editMessageText", 1)

	var texts []string
	for _, call := range fake.callsTo("sendMessage") {
		texts = append(texts, decodeCall[tgctl.OutgoingMessage](t, call).Text)
	}
	if !slices.Equal(texts, []string{"Done.", "Second."}) {
		t.Fatalf("posted %q", texts)
	}
}

func TestADocumentIsStagedLocallyWithoutTheToken(t *testing.T) {
	fake := newFakeTelegram(t)
	fake.files["documents/doc1.txt"] = "notes"
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	startGateway(t, fake, ctrl, nil)

	next := privateText(1, 10, testOwner, "")
	next.Message.Caption = "read this"
	next.Message.Document = &tgctl.Document{FileID: "doc1", FileName: "../notes.txt", MimeType: "text/plain", FileSize: 5}
	fake.push(next)

	ctrl.waitFor(t, "the message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	staged := ctrl.submitted["tg-42"][0]
	if staged.Text != "read this" || len(staged.Attachments) != 1 {
		t.Fatalf("inbound = %+v", staged)
	}
	location := staged.Attachments[0].URL
	path, ok := strings.CutPrefix(location, "file://")
	if !ok || strings.Contains(location, testToken) || !strings.HasPrefix(path, stagingRoot()+"/telegram/tg-42/") || strings.Contains(path, "..") {
		t.Fatalf("attachment url = %q", location)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "notes" {
		t.Fatalf("staged content = %q, %v", content, err)
	}
}

func TestAReactionOutsideTheTelegramSetIsMappedOrRefused(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	if err := gateway.React(t.Context(), conv, "10", "✅"); err != nil {
		t.Fatalf("react: %v", err)
	}
	if err := gateway.React(t.Context(), conv, "10", "🧪"); err == nil {
		t.Fatal("an emoji Telegram does not offer was accepted")
	}
	sent := fake.callsTo("setMessageReaction")
	if len(sent) != 1 {
		t.Fatalf("%d reactions sent", len(sent))
	}
	if got := decodeCall[reactionCall](t, sent[0]); len(got.Reaction) != 1 || got.Reaction[0].Emoji != "👍" {
		t.Fatalf("reaction = %+v", got)
	}
}

func TestARefusedPollFailsTheLivenessProbeWithItsCause(t *testing.T) {
	cases := []struct {
		name        string
		description string
		want        error
	}{
		{"webhook", "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first", errWebhookSet},
		{"second poller", "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", errOtherPoller},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTelegram(t)
			fake.fail["getUpdates"] = http.StatusConflict
			fake.conflict = tc.description
			gateway := startGateway(t, fake, newFakeControl(), nil)

			waitUntil(t, "the poll fault", fake.change, func() bool { return gateway.currentFault() != nil })
			if _, err := gateway.Probe(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("probe = %v", err)
			}
		})
	}
}

func TestAFileTheAgentCannotReadIsNotFetched(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	startGateway(t, fake, ctrl, nil)

	archive := privateText(1, 10, testOwner, "")
	archive.Message.Caption = "see attached"
	archive.Message.Document = &tgctl.Document{FileID: "zip", FileName: "bundle.zip", MimeType: "application/zip", FileSize: 100}
	fake.push(archive)

	ctrl.waitFor(t, "the caption", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if got := ctrl.submitted["tg-42"][0]; got.Text != "see attached" || len(got.Attachments) != 0 {
		t.Fatalf("inbound = %+v", got)
	}
	if len(fake.callsTo("getFile")) != 0 || len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("an unreadable file was fetched or not explained")
	}
}

func albumPhoto(updateID, messageID int64, fileID, caption string) tgctl.Update {
	next := privateText(updateID, messageID, testOwner, "")
	next.Message.MediaGroupID = "album-1"
	next.Message.Caption = caption
	next.Message.Photo = []tgctl.PhotoSize{{FileID: fileID, Width: 800, Height: 600, FileSize: 10}}
	return next
}

func TestAnAlbumIsOneTurnWithAllItsFiles(t *testing.T) {
	fake := newFakeTelegram(t)
	for _, id := range []string{"p1", "p2", "p3"} {
		fake.files["documents/"+id+".txt"] = "jpeg bytes of " + id
	}
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	gateway.album = 10 * time.Millisecond

	fake.push(albumPhoto(1, 10, "p1", ""), albumPhoto(2, 11, "p2", "compare these"), albumPhoto(3, 12, "p3", ""))

	ctrl.waitFor(t, "the album", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) > 0 })
	settle(t, gateway, "42")
	got := ctrl.submitted["tg-42"]
	if len(got) != 1 || got[0].Text != "compare these" || len(got[0].Attachments) != 3 || got[0].MessageID != "11" {
		t.Fatalf("submitted %+v", got)
	}
}

func TestAnAbandonedInputFromElsewhereKeepsTheTelegramPrompt(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	fake.push(privateText(1, 10, testOwner, "first"))
	ctrl.waitFor(t, "the message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	settle(t, gateway, conv.ID)
	gateway.EmitTo(conv, contracts.Event{T: eventAbandoned, Who: "shan", Text: "typed in the terminal"})
	gateway.EmitTo(conv, humanAsking("first"))
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "done"})

	answer := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if answer.ReplyParameters == nil || answer.ReplyParameters.MessageID != 10 {
		t.Fatalf("the answer replies to %+v, want 10", answer.ReplyParameters)
	}
}

func TestAMessageSentAfterAnAlbumReachesTheSessionAfterIt(t *testing.T) {
	fake := newFakeTelegram(t)
	fake.files["documents/p1.txt"], fake.files["documents/p2.txt"] = "one", "two"
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	gateway.album = 20 * time.Millisecond

	fake.push(albumPhoto(1, 10, "p1", "compare these"), albumPhoto(2, 11, "p2", ""), privateText(3, 12, testOwner, "which one is better?"))

	ctrl.waitFor(t, "the album and the question", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 2 })
	got := ctrl.submitted["tg-42"]
	if got[0].Text != "compare these" || len(got[0].Attachments) != 2 || got[1].Text != "which one is better?" {
		t.Fatalf("submitted %q with %d files, then %q", got[0].Text, len(got[0].Attachments), got[1].Text)
	}
}

func TestEachAlbumItemRestartsTheWindow(t *testing.T) {
	fake := newFakeTelegram(t)
	fake.files["documents/p1.txt"], fake.files["documents/p2.txt"], fake.files["documents/p3.txt"] = "one", "two", "three"
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	gateway.album = 600 * time.Millisecond

	fake.push(albumPhoto(1, 10, "p1", "slow upload"))
	gap := time.NewTimer(400 * time.Millisecond)
	<-gap.C
	fake.push(albumPhoto(2, 11, "p2", ""))
	gap.Reset(400 * time.Millisecond)
	<-gap.C
	fake.push(albumPhoto(3, 12, "p3", ""))

	ctrl.waitFor(t, "the album", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) > 0 })
	settle(t, gateway, "42")
	gap.Reset(700 * time.Millisecond)
	<-gap.C
	if got := ctrl.submitted["tg-42"]; len(got) != 1 || len(got[0].Attachments) != 3 {
		t.Fatalf("submitted %d turns, the first with %d files", len(got), len(got[0].Attachments))
	}
}

func TestStopIsNotHeldBehindAnAlbum(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	gateway.album = time.Minute

	fake.push(albumPhoto(1, 10, "p1", "look"), privateText(2, 11, testOwner, "/stop@otherbot"), privateText(3, 12, testOwner, "/stop@"+testBotName))

	ctrl.waitFor(t, "the interrupt", func(c *fakeControl) bool { return len(c.interrupted) == 1 })
	if len(ctrl.submitted["tg-42"]) != 0 {
		t.Fatal("a message went past the held album")
	}
}

func TestAPostFromTheHostIsFormattedNotLiteral(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, nil)

	if _, err := gateway.Post(t.Context(), contracts.Conversation{Gateway: gatewayKind, ID: "42"}, "session **tg-42** started in `~/work`"); err != nil {
		t.Fatalf("post: %v", err)
	}

	sent := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if sent.Text != "session tg-42 started in ~/work" || len(sent.Entities) != 2 || sent.Entities[0].Type != entityBold || sent.Entities[1].Type != entityCode {
		t.Fatalf("sent %+v", sent)
	}
}
