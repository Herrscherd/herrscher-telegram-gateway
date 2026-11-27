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
	bots.register("ana", nil, tgctl.User{ID: voiceBotID, Username: voiceBotName})
	bots.register("ghost", nil, tgctl.User{ID: 111, IsBot: true, Username: ghostBotName})
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
			if _, _, addressed := router.route(from, &msg, voices); addressed {
				t.Fatalf("ana captured %q, which names another addressee", msg.Text)
			}
		})
	}
}

func TestAChannelsAgentWithItsOwnBotCapturesAPlainMessageOfItsChat(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	msg := groupText("a plain message")

	conv, text, addressed := router.route(from, &msg, voices)
	if !addressed || conv.agent != "ana" || text != "a plain message" {
		t.Fatalf("route = %+v %q %v", conv, text, addressed)
	}
}

func TestAnExplicitMentionOfTheChannelsAgentItselfIsStillRouted(t *testing.T) {
	router, voices, from := voiceRouterFixture(t)
	msg := groupText("@"+voiceBotName+" take this", mentionOf(voiceBotName))

	conv, _, addressed := router.route(from, &msg, voices)
	if !addressed || conv.agent != "ana" {
		t.Fatalf("route = %+v addressed=%v", conv, addressed)
	}
}
