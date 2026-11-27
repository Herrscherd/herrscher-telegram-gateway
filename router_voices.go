package telegram

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

var nameMention = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@-])@([A-Za-z0-9_-]+)`)

func directoryVoices(ctrl contracts.SessionControl) []contracts.AgentVoice {
	directory, ok := ctrl.(contracts.AgentDirectory)
	if !ok {
		return nil
	}
	return directory.AgentVoices(gatewayKind)
}

func voiceNamed(voices []contracts.AgentVoice, agent string) (contracts.AgentVoice, bool) {
	for _, voice := range voices {
		if voice.Agent == agent {
			return voice, true
		}
	}
	return contracts.AgentVoice{}, false
}

func channelHas(channels []string, conv chatRef) bool {
	return slices.Contains(channels, conv.String()) || slices.Contains(channels, strconv.FormatInt(conv.chat, 10))
}

func namesMentioned(text, name string) bool {
	for _, hit := range nameMention.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(hit[1], name) {
			return true
		}
	}
	return false
}

func tokenlessMention(voices []contracts.AgentVoice, bots *voiceBots, text string) (string, bool) {
	for _, voice := range voices {
		if voice.Reply == contracts.ReplyNever || !validAgent(voice.Agent) || bots.hasOwn(voice.Agent) {
			continue
		}
		if namesMentioned(text, voice.Agent) {
			return voice.Agent, true
		}
	}
	return "", false
}

func tokenlessChannel(voices []contracts.AgentVoice, bots *voiceBots, conv chatRef) (string, bool) {
	for _, voice := range voices {
		if voice.Reply != contracts.ReplyInChannels || !validAgent(voice.Agent) || bots.hasOwn(voice.Agent) {
			continue
		}
		if channelHas(voice.Channels, conv) {
			return voice.Agent, true
		}
	}
	return "", false
}
