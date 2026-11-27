package telegram

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Herrscherd/tgctl"
)

const (
	gatewayKind     = "telegram"
	threadSeparator = ":"
	sessionPrefix   = "tg-"
	negativeMark    = "n"
)

var errBadConversation = errors.New("not a telegram conversation id")

type chatRef struct {
	chat   int64
	thread int64
}

func conversationOf(msg *tgctl.Message) chatRef {
	ref := chatRef{chat: msg.Chat.ID}
	if msg.IsTopicMessage && msg.ThreadID != 0 {
		ref.thread = msg.ThreadID
	}
	return ref
}

func parseChatRef(raw string) (chatRef, error) {
	chatPart, threadPart, threaded := strings.Cut(raw, threadSeparator)
	chatID, err := strconv.ParseInt(chatPart, 10, 64)
	if err != nil || chatID == 0 {
		return chatRef{}, fmt.Errorf("%w: %q", errBadConversation, raw)
	}
	ref := chatRef{chat: chatID}
	if !threaded {
		return ref, nil
	}
	threadID, err := strconv.ParseInt(threadPart, 10, 64)
	if err != nil || threadID <= 0 {
		return chatRef{}, fmt.Errorf("%w: %q", errBadConversation, raw)
	}
	ref.thread = threadID
	return ref, nil
}

func (c chatRef) String() string {
	text := strconv.FormatInt(c.chat, 10)
	if c.thread == 0 {
		return text
	}
	return text + threadSeparator + strconv.FormatInt(c.thread, 10)
}

func (c chatRef) private() bool { return c.chat > 0 }

func (c chatRef) sessionName() string {
	name := strings.ReplaceAll(c.String(), threadSeparator, "-")
	if rest, negative := strings.CutPrefix(name, "-"); negative {
		name = negativeMark + rest
	}
	return sessionPrefix + name
}

func parseMessageID(raw string) (int64, error) {
	messageID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || messageID <= 0 {
		return 0, fmt.Errorf("%w: message %q", errBadConversation, raw)
	}
	return messageID, nil
}

func formatID(id int64) string { return strconv.FormatInt(id, 10) }
