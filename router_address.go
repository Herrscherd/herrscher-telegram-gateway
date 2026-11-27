package telegram

import (
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/Herrscherd/tgctl"
)

func plainText(msg *tgctl.Message) string {
	raw, _ := textOf(msg)
	return strings.TrimSpace(raw)
}

func textOf(msg *tgctl.Message) (string, []tgctl.Entity) {
	if msg.Text != "" {
		return msg.Text, msg.Entities
	}
	return msg.Caption, msg.CaptionEntities
}

func addressedTo(self tgctl.User, msg *tgctl.Message) (string, bool) {
	raw, entities := textOf(msg)
	text := strings.TrimSpace(raw)
	if msg.Chat.Type == chatTypePrivate {
		return stripCommandMention(text, self.Username), true
	}
	if msg.ReplyTo != nil && msg.ReplyTo.From != nil && msg.ReplyTo.From.ID == self.ID {
		return stripCommandMention(text, self.Username), true
	}
	stripped, mentioned := withoutMention(raw, entities, self)
	if !mentioned {
		return "", false
	}
	return stripCommandMention(strings.TrimSpace(stripped), self.Username), true
}

func mentionedBy(text string, entities []tgctl.Entity, self tgctl.User) bool {
	_, ok := withoutMention(text, entities, self)
	return ok
}

func withoutMention(text string, entities []tgctl.Entity, self tgctl.User) (string, bool) {
	units := utf16.Encode([]rune(text))
	for _, candidate := range entities {
		end := candidate.Offset + candidate.Length
		if candidate.Offset < 0 || end > len(units) {
			continue
		}
		if !mentions(candidate, string(utf16.Decode(units[candidate.Offset:end])), self) {
			continue
		}
		if candidate.Type == entityCommand {
			return text, true
		}
		rest := slices.Concat(units[:candidate.Offset], units[end:])
		return string(utf16.Decode(rest)), true
	}
	return "", false
}

func mentions(candidate tgctl.Entity, covered string, self tgctl.User) bool {
	switch candidate.Type {
	case entityMention:
		return strings.EqualFold(strings.TrimPrefix(covered, "@"), self.Username)
	case entityUserLink:
		return candidate.User != nil && candidate.User.ID == self.ID
	case entityCommand:
		_, target, found := strings.Cut(covered, "@")
		return found && strings.EqualFold(target, self.Username)
	}
	return false
}

func stripCommandMention(text, username string) string {
	if !strings.HasPrefix(text, "/") || username == "" {
		return text
	}
	command, rest, _ := strings.Cut(text, " ")
	name, target, found := strings.Cut(command, "@")
	if !found || !strings.EqualFold(target, username) {
		return text
	}
	return strings.TrimSpace(name + " " + rest)
}
