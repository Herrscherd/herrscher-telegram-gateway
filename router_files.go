package telegram

import (
	"context"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

func forwarded(target addressee) bool {
	return !strings.EqualFold(target.text, stopCommand)
}

func (r *router) stageOnce(ctx context.Context, from origin, msg *tgctl.Message, members []*tgctl.Message) []contracts.Attachment {
	house := address{chatRef: conversationOf(msg)}
	var attachments []contracts.Attachment
	offered := 0
	for _, member := range members {
		attachments = append(attachments, r.files.stage(ctx, from.api, house, member)...)
		offered += len(filesOf(member))
	}
	if len(attachments) < offered {
		r.post(ctx, house, r.msgs.say(fileRefusedKey, nil))
	}
	return attachments
}
