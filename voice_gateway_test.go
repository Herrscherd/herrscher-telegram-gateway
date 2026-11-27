package telegram

import (
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

func voiceOf(t *testing.T, agent, token string, reply contracts.ReplyRule, channels ...string) contracts.AgentVoice {
	t.Helper()
	voice := contracts.AgentVoice{Agent: agent, Reply: reply, Channels: channels}
	if token != "" {
		secret, err := contracts.NewBotToken(token)
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		voice.Token = secret
	}
	return voice
}

func agentMention(agent string) tgctl.Entity {
	return tgctl.Entity{Type: entityMention, Offset: 0, Length: len("@" + agent)}
}

func TestAnAgentWithItsOwnTokenPostsThroughItsOwnBot(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)
	fake.waitVoicePoll(t)

	if _, err := gateway.Post(t.Context(), contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}, "hi"); err != nil {
		t.Fatalf("post: %v", err)
	}

	sent := fake.callsWith(voiceToken, "sendMessage")
	if len(sent) != 1 || decodeCall[tgctl.OutgoingMessage](t, sent[0]).Text != "hi" {
		t.Fatalf("own bot sent %+v, house sent %d", sent, len(fake.callsWith(testToken, "sendMessage")))
	}
}

func TestATokenlessAgentPostsThroughTheHouseBotUnderItsHeader(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", "", contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)

	if _, err := gateway.Post(t.Context(), contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}, "hi"); err != nil {
		t.Fatalf("post: %v", err)
	}

	sent := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if sent.Text != "ana\nhi" || len(sent.Entities) != 1 || sent.Entities[0].Type != entityBold || sent.Entities[0].Length != len("ana") {
		t.Fatalf("house sent %+v", sent)
	}
}

func TestAnAgentReactionGoesThroughItsOwnBot(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)
	fake.waitVoicePoll(t)

	if err := gateway.React(t.Context(), contracts.Conversation{Gateway: gatewayKind, ID: "42@ana"}, "5", "👍"); err != nil {
		t.Fatalf("react: %v", err)
	}

	if got := fake.callsWith(voiceToken, "setMessageReaction"); len(got) != 1 {
		t.Fatalf("own bot reactions = %d", len(got))
	}
}

func TestAVoiceWithItsOwnTokenReceivesItsOwnPrivateMessages(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	ctrl.live("tg-42-a-ana", "42@ana")
	startGateway(t, fake, ctrl, nil)

	fake.pushTo(voiceToken, privateText(1, 10, testOwner, "hello ana"))

	ctrl.waitFor(t, "the voice message", func(c *fakeControl) bool { return len(c.submitted["tg-42-a-ana"]) == 1 })
	if got := ctrl.submitted["tg-42-a-ana"][0]; got.Conversation.ID != "42@ana" || got.Text != "hello ana" {
		t.Fatalf("inbound = %+v", got)
	}
}

func TestAFailingAgentTokenIsSkippedAndItsPollNeverRuns(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{
		voiceOf(t, "ghost", refusedToken, contracts.ReplyOnMention),
		voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention),
	}
	startGateway(t, fake, ctrl, nil)

	fake.waitSignIn(t, refusedToken)
	fake.pushTo(voiceToken, privateText(1, 10, testOwner, "marker"))
	ctrl.live("tg-42-a-ana", "42@ana")
	fake.waitVoicePoll(t)
	if polls := fake.pollsWith(refusedToken); polls != 0 {
		t.Fatalf("a voice with a refused token was polled %d times", polls)
	}
}

func TestATextMentionOfATokenlessAgentRoutesToItsSessionAsThatAgent(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", "", contracts.ReplyOnMention)}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	fake.push(inGroup(1, 10, testOwner, "@ana please review"))

	ctrl.waitFor(t, "the created session", func(c *fakeControl) bool { return len(c.created) == 1 })
	if got := ctrl.created[0]; got.Agent != "ana" || got.ChannelID != "-100500@ana" || got.Name != "tg-n100500-a-ana" {
		t.Fatalf("created = %+v", got)
	}
}

func TestANeverAgentIsNotRoutedByText(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", "", contracts.ReplyNever)}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	fake.push(inGroup(1, 10, testOwner, "@ana please review"))
	fake.push(inGroup(2, 11, testOwner, "@"+testBotName+" marker"))
	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.created) == 1 })
	if got := ctrl.created[0]; got.Agent != "" || got.ChannelID != "-100500" {
		t.Fatalf("a never agent was routed: %+v", got)
	}
}

func TestAChannelsAgentTakesEveryMessageOfItsChats(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", "", contracts.ReplyInChannels, "-100500")}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	fake.push(inGroup(1, 10, testOwner, "a plain message"))

	ctrl.waitFor(t, "the created session", func(c *fakeControl) bool { return len(c.created) == 1 })
	if got := ctrl.created[0]; got.Agent != "ana" || got.ChannelID != "-100500@ana" {
		t.Fatalf("created = %+v", got)
	}
}

func TestAnExplicitMentionBeatsAChannelCapture(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{
		voiceOf(t, "ana", "", contracts.ReplyInChannels, "-100500"),
		voiceOf(t, "bob", "", contracts.ReplyOnMention),
	}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	fake.push(inGroup(1, 10, testOwner, "@bob take this"))

	ctrl.waitFor(t, "the created session", func(c *fakeControl) bool { return len(c.created) == 1 })
	if got := ctrl.created[0]; got.Agent != "bob" || got.ChannelID != "-100500@bob" {
		t.Fatalf("created = %+v", got)
	}
}

func TestAnExplicitHouseMentionIsAnsweredAlongsideAReadyChannelAgent(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	ctrl.live("tg-n100500-a-bob", "-100500@bob")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyInChannels, "-100500")}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	mention := "@" + testBotName
	addressed := inGroup(1, 10, testOwner, mention+" what changed")
	addressed.Message.Entities = []tgctl.Entity{{Type: entityMention, Offset: 0, Length: len(mention)}}
	fake.push(addressed)

	ctrl.waitFor(t, "bob captures the chat and the house answers its mention", func(c *fakeControl) bool {
		return len(c.submitted["tg-n100500-a-bob"]) == 1 && len(c.submitted["tg-n100500"]) == 1
	})
}

func TestAnExplicitHouseMentionAlsoAnswersANamedTokenlessAgentAsOnDiscord(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	ctrl.live("tg-n100500-a-bob", "-100500@bob")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyOnMention)}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	house := "@" + testBotName
	named := inGroup(1, 10, testOwner, house+" @bob what changed")
	named.Message.Entities = []tgctl.Entity{
		{Type: entityMention, Offset: 0, Length: len(house)},
		{Type: entityMention, Offset: len(house) + 1, Length: len("@bob")},
	}
	fake.push(named)

	ctrl.waitFor(t, "bob answers its mention and the house answers its own", func(c *fakeControl) bool {
		return len(c.submitted["tg-n100500-a-bob"]) == 1 && len(c.submitted["tg-n100500"]) == 1
	})
}

func TestAFileRefusedAcrossSeveralTargetsIsExplainedOnce(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500", "-100500")
	ctrl.live("tg-n100500-a-bob", "-100500@bob")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyInChannels, "-100500")}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	mention := "@" + testBotName
	addressed := inGroup(1, 10, testOwner, mention+" see the file")
	addressed.Message.Entities = []tgctl.Entity{{Type: entityMention, Offset: 0, Length: len(mention)}}
	addressed.Message.Document = &tgctl.Document{FileID: "big", FileName: "big.txt", FileSize: attachmentMaxBytes + 1}
	fake.push(addressed)

	ctrl.waitFor(t, "bob and the house both receive the message", func(c *fakeControl) bool {
		return len(c.submitted["tg-n100500-a-bob"]) == 1 && len(c.submitted["tg-n100500"]) == 1
	})
	refusals := 0
	for _, call := range fake.callsTo("sendMessage") {
		out := decodeCall[tgctl.OutgoingMessage](t, call)
		if !strings.Contains(out.Text, "not accepted") {
			continue
		}
		refusals++
		if call.token != testToken || out.ChatID != testGroup || strings.HasPrefix(out.Text, "bob\n") {
			t.Fatalf("the refusal went through token %q to chat %d under an agent header: %q", call.token, out.ChatID, out.Text)
		}
	}
	if refusals != 1 {
		t.Fatalf("the refused file was explained %d times, want once", refusals)
	}
}

func TestAHouseDropsAMessageThatMentionsOnlyAnAgentBot(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})

	fake.waitVoicePoll(t)
	next := inGroup(1, 10, testOwner, "@"+voiceBotName+" hi")
	next.Message.Entities = []tgctl.Entity{agentMention(voiceBotName)}
	fake.push(next)
	fake.push(inGroup(2, 11, testOwner, "@"+testBotName+" marker"))
	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.submitted["tg-n100500"]) == 1 })
	if got := ctrl.submitted["tg-n100500"]; len(got) != 1 || got[0].Text != "marker" {
		t.Fatalf("house routed the agent-bot message: submitted %+v", got)
	}
}

func TestAnAgentBotAuthoredMessageNeverReachesTheRouter(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42", "42")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	startGateway(t, fake, ctrl, map[string]string{allowAllSetting: "1"})
	fake.waitVoicePoll(t)

	echo := privateText(1, 10, voiceBotID, "my own echo")
	echo.Message.Chat = tgctl.Chat{ID: testOwner, Type: chatTypePrivate}
	fake.push(echo)
	marker := privateText(2, 11, testOwner, "marker")
	fake.push(marker)
	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.submitted["tg-42"]) == 1 })
	if got := ctrl.submitted["tg-42"]; got[0].Text != "marker" {
		t.Fatalf("an agent bot's own message was routed: %+v", got)
	}
}

func TestAVoiceKeyboardIsPostedByTheHouseAndItsPressRoutesToTheVoice(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-n100500-a-ana", "-100500@ana")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)

	if err := gateway.Menu(t.Context(), contracts.Conversation{Gateway: gatewayKind, ID: "-100500@ana"}, "", "pick one", []contracts.Choice{{Value: "v", Label: "first"}}); err != nil {
		t.Fatalf("menu: %v", err)
	}

	menu := decodeCall[tgctl.OutgoingMessage](t, fake.waitCalls(t, "sendMessage", 1)[0])
	if menu.Text != "ana\npick one" || len(menu.ReplyMarkup.Rows) != 1 {
		t.Fatalf("menu = %+v", menu)
	}
	data := menu.ReplyMarkup.Rows[0][0].CallbackData
	fake.pushTo(testToken, tgctl.Update{UpdateID: 2, CallbackQuery: &tgctl.CallbackQuery{
		ID: "q2", From: tgctl.User{ID: testOwner}, Data: data,
		Message: &tgctl.Message{MessageID: 9, Chat: tgctl.Chat{ID: testGroup, Type: chatTypeSuper}},
	}})

	ctrl.waitFor(t, "the pick", func(c *fakeControl) bool { return len(c.picks) == 1 })
	if got := ctrl.picks[0]; got != "tg-n100500-a-ana=v" {
		t.Fatalf("pick = %q", got)
	}
}

func TestClosingTheGatewayStopsEveryAgentPoller(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	gateway := startGateway(t, fake, ctrl, nil)

	fake.waitVoicePoll(t)
	if err := gateway.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestAnAgentBotsOwnEchoOnItsOwnPollNeverReachesItsSession(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.live("tg-42-a-ana", "42@ana")
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ana", voiceToken, contracts.ReplyOnMention)}
	startGateway(t, fake, ctrl, map[string]string{allowAllSetting: "1"})
	fake.waitVoicePoll(t)

	echo := privateText(1, 10, voiceBotID, "my own echo")
	echo.Message.Chat = tgctl.Chat{ID: testOwner, Type: chatTypePrivate}
	fake.pushTo(voiceToken, echo)
	fake.pushTo(voiceToken, privateText(2, 11, testOwner, "marker"))

	ctrl.waitFor(t, "the marker", func(c *fakeControl) bool { return len(c.submitted["tg-42-a-ana"]) == 1 })
	if got := ctrl.submitted["tg-42-a-ana"]; got[0].Text != "marker" {
		t.Fatalf("an agent bot's own echo was routed: %+v", got)
	}
}

func TestARejectedTokenChannelsAgentCapturesThroughTheHouse(t *testing.T) {
	fake := newFakeTelegram(t)
	ctrl := newFakeControl()
	ctrl.repos = []contracts.RepoRef{{Name: "herrscher", Local: true}}
	ctrl.voices = []contracts.AgentVoice{voiceOf(t, "ghost", refusedToken, contracts.ReplyInChannels, "-100500")}
	gateway := startGateway(t, fake, ctrl, map[string]string{chatsSetting: "-100500"})
	awaitSettled(t, gateway, "ghost")

	fake.push(inGroup(1, 10, testOwner, "a plain message"))

	ctrl.waitFor(t, "the created session", func(c *fakeControl) bool { return len(c.created) == 1 })
	if got := ctrl.created[0]; got.Agent != "ghost" || got.ChannelID != "-100500@ghost" {
		t.Fatalf("created = %+v", got)
	}
}
