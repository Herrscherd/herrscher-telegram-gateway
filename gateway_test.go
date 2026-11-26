package telegram

import (
	"os"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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

func TestStrangersBotsAndUnlistedGroupsAreIgnored(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.live("tg-n100500", "-100500")
	startGateway(t, fake, ctrl, nil)

	group := privateText(3, 12, testOwner, "@"+testBotName+" hi")
	group.Message.Chat = chat{ID: testGroup, Type: chatTypeSuper}
	group.Message.Entities = []entity{{Type: entityMention, Offset: 0, Length: len(testBotName) + 1}}
	robot := privateText(2, 11, testOwner, "hi")
	robot.Message.From.IsBot = true
	fake.push(privateText(1, 10, testStranger, "hi"), robot, group, privateText(4, 13, testOwner, "last"))

	ctrl.waitFor(t, "the owner's last message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if got := ctrl.submitted["tg-42"][0].Text; got != "last" {
		t.Fatalf("submitted %q, want only the owner's private message", got)
	}
	if len(ctrl.submitted["tg-n100500"]) != 0 {
		t.Fatal("a group outside allowed_chats reached its session")
	}
}

func TestAGroupAnswersOnlyWhenTheBotIsAddressed(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	inGroup := func(updateID, messageID int64, text string, entities []entity) update {
		next := privateText(updateID, messageID, testOwner, text)
		next.Message.Chat = chat{ID: testGroup, Type: chatTypeSuper}
		next.Message.Entities = entities
		return next
	}
	mention := "@" + testBotName
	reply := inGroup(3, 12, "and this", nil)
	reply.Message.ReplyTo = &message{MessageID: 5, From: &user{ID: testBotID, IsBot: true}}
	fake.push(
		inGroup(1, 10, "chatter between people", nil),
		inGroup(2, 11, "é "+mention+" look", []entity{{Type: entityMention, Offset: 2, Length: len(mention)}}),
		reply,
	)

	ctrl.waitFor(t, "two addressed messages", func(c *fakeControl) bool { return len(c.submitted["tg-n100500"]) == 2 })
	got := ctrl.submitted["tg-n100500"]
	if got[0].Text != "é  look" || got[1].Text != "and this" {
		t.Fatalf("texts = %q, %q", got[0].Text, got[1].Text)
	}
}

func TestANewChatPicksItsRepositoryThenGetsTheBufferedMessage(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "alpha", Local: true}, {Name: "org/beta"}}
	startGateway(t, fake, ctrl, map[string]string{usersSetting: "99"})

	fake.push(privateText(1, 10, testOwner, "start here"))
	menu := decodeCall[outgoing](t, fake.waitCalls(t, "sendMessage", 1)[0])
	second := menu.ReplyMarkup.Rows[1][0]
	data := second.CallbackData
	if second.Text != "org/beta" || len(data) > 64 {
		t.Fatalf("second button = %+v", second)
	}

	pressed := &message{MessageID: 1001, Chat: chat{ID: testOwner, Type: chatTypePrivate}}
	fake.push(update{UpdateID: 2, CallbackQuery: &callbackQuery{ID: "q1", From: user{ID: testStranger}, Message: pressed, Data: data}})
	answer := decodeCall[callbackAnswer](t, fake.waitCalls(t, "answerCallbackQuery", 1)[0])
	if answer.Text != "Only the person who asked can pick." {
		t.Fatalf("someone else's press answered %+v", answer)
	}

	fake.push(update{UpdateID: 3, CallbackQuery: &callbackQuery{ID: "q2", From: user{ID: testOwner}, Message: pressed, Data: data}})
	ctrl.waitFor(t, "the buffered message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	spec := ctrl.created[0]
	if spec.Name != "tg-42" || spec.ChannelID != "42" || spec.Clone != "org/beta" || spec.Gateways[0] != gatewayKind {
		t.Fatalf("created %+v", spec)
	}
	if got := ctrl.submitted["tg-42"][0].Text; got != "start here" {
		t.Fatalf("buffered text = %q", got)
	}
}

func TestAMenuPressIsHandedToTheSessionAsAPick(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)

	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}
	if err := gateway.Menu(t.Context(), conv, "10", "Run the migration?", []contracts.Choice{{Label: "Yes", Value: "allow"}, {Label: "No", Value: "deny"}}); err != nil {
		t.Fatalf("menu: %v", err)
	}
	data := decodeCall[outgoing](t, fake.waitCalls(t, "sendMessage", 1)[0]).ReplyMarkup.Rows[1][0].CallbackData

	pressed := &message{MessageID: 1001, Chat: chat{ID: testOwner, Type: chatTypePrivate}}
	fake.push(update{UpdateID: 1, CallbackQuery: &callbackQuery{ID: "q", From: user{ID: testOwner}, Message: pressed, Data: data}})
	ctrl.waitFor(t, "the pick", func(c *fakeControl) bool { return len(c.picks) == 1 })
	if ctrl.picks[0] != "tg-42=deny" {
		t.Fatalf("picks = %v", ctrl.picks)
	}
	if len(fake.waitCalls(t, "editMessageReplyMarkup", 1)) != 1 {
		t.Fatal("the menu kept its buttons")
	}
}

func TestStopCommandAndStopButtonInterruptTheTurn(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	startGateway(t, fake, ctrl, nil)

	fake.push(
		privateText(1, 10, testOwner, "/stop@"+testBotName),
		update{UpdateID: 2, StoppedGeneration: &generationStopped{Chat: chat{ID: testOwner, Type: chatTypePrivate}, DraftID: 1}},
	)

	ctrl.waitFor(t, "two interrupts", func(c *fakeControl) bool { return len(c.interrupted) == 2 })
	if len(ctrl.submitted["tg-42"]) != 0 {
		t.Fatal("/stop was submitted as a prompt")
	}
}

func TestAPrivateTurnStreamsADraftThenRepliesWithEntities(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	gateway := startGateway(t, fake, ctrl, nil)
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "42"}

	fake.push(privateText(1, 10, testOwner, "go"))
	ctrl.waitFor(t, "the message", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "working on **it**"})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Text: "Done with **it**"})

	drafted := decodeCall[draft](t, fake.waitCalls(t, "sendMessageDraft", 1)[0])
	if drafted.Text != "working on it" || !drafted.CanStop || drafted.DraftID == 0 {
		t.Fatalf("draft = %+v", drafted)
	}
	answer := decodeCall[outgoing](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if answer.Text != "Done with it" || len(answer.Entities) != 1 || answer.Entities[0] != (entity{Type: entityBold, Offset: 10, Length: 2}) {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.ReplyParameters == nil || answer.ReplyParameters.MessageID != 10 {
		t.Fatalf("answer is not a reply to the prompt: %+v", answer)
	}
	calls := fake.waitCalls(t, "setMessageReaction", 2)
	acked, cleared := decodeCall[reaction](t, calls[0]), decodeCall[reaction](t, calls[1])
	if len(acked.Reaction) != 1 || acked.Reaction[0].Emoji != ackEmoji || acked.MessageID != 10 || cleared.Reaction == nil || len(cleared.Reaction) != 0 {
		t.Fatalf("reactions = %+v then %+v", acked, cleared)
	}
}

func TestAGroupTurnEditsOneLiveMessageAndSaysWhenItFailed(t *testing.T) {
	fake := newFakeTelegram(t)
	gateway := startGateway(t, fake, nil, map[string]string{chatsSetting: "-100500"})
	conv := contracts.Conversation{Gateway: gatewayKind, ID: "-100500:7"}

	gateway.EmitTo(conv, contracts.Event{T: eventHuman})
	gateway.EmitTo(conv, contracts.Event{T: eventChunk, Text: "partial"})
	gateway.EmitTo(conv, contracts.Event{T: eventReply, Done: true, Outcome: contracts.TurnFailed, Text: "half an answer"})

	live := decodeCall[outgoing](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if live.Text != "partial" || live.ThreadID != 7 || live.ChatID != testGroup {
		t.Fatalf("live message = %+v", live)
	}
	final := decodeCall[textEdit](t, fake.waitCalls(t, "editMessageText", 1)[0])
	if final.Text != "half an answer\n\nThe turn failed." || final.MessageID != 1001 {
		t.Fatalf("final edit = %+v", final)
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
	next.Message.Document = &document{FileID: "doc1", FileName: "../notes.txt", MimeType: "text/plain", FileSize: 5}
	big := privateText(2, 11, testOwner, "too big")
	big.Message.Document = &document{FileID: "doc2", FileName: "big.txt", FileSize: attachmentMaxBytes + 1}
	fake.push(next, big)

	ctrl.waitFor(t, "two messages", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 2 })
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
	if len(ctrl.submitted["tg-42"][1].Attachments) != 0 || len(fake.callsTo("getFile")) != 1 {
		t.Fatal("a file over the cap was fetched")
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
	if got := decodeCall[reaction](t, sent[0]); len(got.Reaction) != 1 || got.Reaction[0].Emoji != "👍" {
		t.Fatalf("reaction = %+v", got)
	}
}
