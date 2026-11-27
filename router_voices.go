package telegram

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
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
		if voice.Reply == contracts.ReplyNever || !validAgent(voice.Agent) || !bots.tokenlessReady(voice.Agent) {
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
		if voice.Reply != contracts.ReplyInChannels || !validAgent(voice.Agent) || !bots.tokenlessReady(voice.Agent) {
			continue
		}
		if channelHas(voice.Channels, conv) {
			return voice.Agent, true
		}
	}
	return "", false
}

func capturingAgent(voices []contracts.AgentVoice, bots *voiceBots, text string, conv chatRef) (string, bool) {
	if agent, ok := tokenlessMention(voices, bots, text); ok {
		return agent, true
	}
	return tokenlessChannel(voices, bots, conv)
}

func pendingHold(voices []contracts.AgentVoice, bots *voiceBots, text string, conv chatRef) (string, bool) {
	for _, voice := range voices {
		if voice.Reply == contracts.ReplyNever || !bots.signingIn(voice.Agent) {
			continue
		}
		if reachesVoice(voice, text, conv) {
			return voice.Agent, true
		}
	}
	return "", false
}

func (r *router) routeOwnVoice(from origin, msg *tgctl.Message, base chatRef, voices []contracts.AgentVoice) []addressee {
	voice, ok := voiceNamed(voices, from.agent)
	if !ok || voice.Reply == contracts.ReplyNever {
		return nil
	}
	conv := address{chatRef: base, agent: from.agent}
	if text, addressed := addressedTo(from.self, msg); addressed {
		return []addressee{{conv: conv, text: text}}
	}
	if voice.Reply == contracts.ReplyInChannels && channelHas(voice.Channels, base) && !r.namesOtherAddressee(from, msg, voices) {
		return []addressee{{conv: conv, text: plainText(msg)}}
	}
	return nil
}

func (r *router) namesOtherAddressee(from origin, msg *tgctl.Message, voices []contracts.AgentVoice) bool {
	raw, entities := textOf(msg)
	return mentionedBy(raw, entities, r.voices.self) || r.voices.mentionsOtherAgentBot(raw, entities, from.self) || namedTokenless(voices, r.voices, raw)
}

func namedTokenless(voices []contracts.AgentVoice, bots *voiceBots, text string) bool {
	for _, voice := range voices {
		if validAgent(voice.Agent) && !bots.hasOwn(voice.Agent) && namesMentioned(text, voice.Agent) {
			return true
		}
	}
	return false
}

func (r *router) routeHouse(from origin, msg *tgctl.Message, base chatRef, voices []contracts.AgentVoice) []addressee {
	raw, entities := textOf(msg)
	houseMentioned := mentionedBy(raw, entities, from.self)
	if !houseMentioned && r.voices.mentionsAgentBot(raw, entities) {
		return nil
	}
	var targets []addressee
	if agent, ok := capturingAgent(voices, r.voices, raw, base); ok {
		capture := addressee{conv: address{chatRef: base, agent: agent}, text: textWithoutHouse(msg, from.self)}
		if !houseMentioned {
			return []addressee{capture}
		}
		targets = append(targets, capture)
	}
	if !houseMentioned {
		if agent, held := pendingHold(voices, r.voices, raw, base); held {
			r.log.Info("message dropped, its agent is still signing in", "agent", agent)
			return nil
		}
	}
	if text, addressed := addressedTo(from.self, msg); addressed {
		targets = append(targets, addressee{conv: address{chatRef: base}, text: text})
	}
	return targets
}
