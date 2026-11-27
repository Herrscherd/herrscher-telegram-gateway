package telegram

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const ghostBotName = "ghost_bot"

func voiceRouterFixture(t *testing.T) (*router, []contracts.AgentVoice, origin) {
	t.Helper()
	bots := newVoiceBots(nil, tgctl.User{ID: testBotID, Username: testBotName})
	bots.admit("ana", nil, tgctl.User{ID: voiceBotID, Username: voiceBotName})
	bots.admit("ghost", nil, tgctl.User{ID: 111, IsBot: true, Username: ghostBotName})
	voices := []contracts.AgentVoice{
		voiceOf(t, "ana", voiceToken, contracts.ReplyInChannels, "-100500"),
		voiceOf(t, "ghost", refusedToken, contracts.ReplyOnMention),
		voiceOf(t, "bob", "", contracts.ReplyOnMention),
	}
	from := origin{agent: "ana", self: tgctl.User{ID: voiceBotID, Username: voiceBotName}}
	return &router{voices: bots}, voices, from
}

func groupText(text string, entities ...tgctl.Entity) tgctl.Message {
	return tgctl.Message{
		MessageID: 10, Chat: tgctl.Chat{ID: testGroup, Type: chatTypeSuper},
		From: &tgctl.User{ID: testOwner, FirstName: "Ana"}, Text: text, Entities: entities,
	}
}

func mentionOf(username string) tgctl.Entity {
	return tgctl.Entity{Type: entityMention, Offset: 0, Length: len("@" + username)}
}

func TestAChannelsAgentWithItsOwnBotLeavesAMessageNamingAnotherAddressee(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	cases := map[string]tgctl.Message{
		"house bot mentioned":            groupText("@"+testBotName+" take this", mentionOf(testBotName)),
		"other agent bot mentioned":      groupText("@"+ghostBotName+" take this", mentionOf(ghostBotName)),
		"token-less agent named in text": groupText("@bob take this"),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if len(router.route(from, &msg, voices)) > 0 {
				t.Fatalf("ana captured %q, which names another addressee", msg.Text)
			}
		})
	}
}

func TestAChannelsAgentDoesNotCountItsOwnMentionAsAnotherAddressee(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	msg := groupText("@"+voiceBotName+" take this", mentionOf(voiceBotName))

	if router.namesOtherAddressee(from, &msg, voices) {
		t.Fatal("the agent's own mention was counted as naming another addressee")
	}
}

func TestAChannelsAgentWithItsOwnBotCapturesAPlainMessageOfItsChat(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	msg := groupText("a plain message")

	targets := router.route(from, &msg, voices)
	if len(targets) != 1 || targets[0].conv.agent != "ana" || targets[0].text != "a plain message" {
		t.Fatalf("route = %+v", targets)
	}
}

func TestAnExplicitMentionOfTheChannelsAgentItselfIsStillRouted(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	msg := groupText("@"+voiceBotName+" take this", mentionOf(voiceBotName))

	targets := router.route(from, &msg, voices)
	if len(targets) != 1 || targets[0].conv.agent != "ana" {
		t.Fatalf("route = %+v", targets)
	}
}

func houseRouter() (*router, origin) {
	bots := newVoiceBots(nil, tgctl.User{ID: testBotID, Username: testBotName})
	return &router{voices: bots}, origin{self: tgctl.User{ID: testBotID, Username: testBotName}}
}

func replyToHouse(text string) tgctl.Message {
	msg := groupText(text)
	msg.ReplyTo = &tgctl.Message{MessageID: 5, From: &tgctl.User{ID: testBotID, IsBot: true}}
	return msg
}

func TestAReplyToTheHouseIsCapturedByAReadyTokenlessChannelsAgentAsOnDiscord(t *testing.T) {
	voices := []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyInChannels, "-100500")}
	router, house := houseRouter()
	msg := replyToHouse("a reply to the house")

	targets := router.route(house, &msg, voices)
	if len(targets) != 1 || targets[0].conv.agent != "bob" {
		t.Fatalf("route = %+v, want the channels agent bob to capture the reply alone", targets)
	}
}

func TestAReplyToATokenlessVoicesHeaderGoesToTheHouseAsOnDiscord(t *testing.T) {
	voices := []contracts.AgentVoice{voiceOf(t, "bob", "", contracts.ReplyOnMention)}
	router, house := houseRouter()
	msg := replyToHouse("and the details?")

	targets := router.route(house, &msg, voices)
	if len(targets) != 1 || targets[0].conv.agent != "" || targets[0].text != "and the details?" {
		t.Fatalf("route = %+v, want the house to answer a reply to bob's header", targets)
	}
}

func TestAChannelsAgentLeavesAMessageNamingANeverOrSigningInAgent(t *testing.T) {
	cases := map[string]struct {
		reply     contracts.ReplyRule
		signingIn bool
	}{
		"never agent":      {reply: contracts.ReplyNever},
		"signing-in agent": {reply: contracts.ReplyOnMention, signingIn: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			router, voices, from := voiceRouterFixture(t)
			voices[2] = voiceOf(t, "bob", "", tc.reply)
			if tc.signingIn {
				router.voices.expect("bob")
			}
			msg := groupText("@bob hi")
			if captured := router.route(from, &msg, voices); len(captured) != 0 {
				t.Fatalf("ana captured %q, which names bob: %+v", msg.Text, captured)
			}
		})
	}
}
