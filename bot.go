package telegram

import (
	"context"
	"io"
)

type bot interface {
	getMe(ctx context.Context) (botIdentity, error)
	getUpdates(ctx context.Context, offset int64) ([]update, error)
	sendMessage(ctx context.Context, out outgoing) (message, error)
	editMessageText(ctx context.Context, edit textEdit) error
	editMessageReplyMarkup(ctx context.Context, edit markupEdit) error
	sendMessageDraft(ctx context.Context, next draft) error
	setMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error
	answerCallbackQuery(ctx context.Context, answer callbackAnswer) error
	getFile(ctx context.Context, fileID string) (remoteFile, error)
	download(ctx context.Context, filePath string, limit int64, dst io.Writer) error
}

var _ bot = (*botAPI)(nil)
