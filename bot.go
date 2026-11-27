package telegram

import (
	"context"
	"io"
	"time"

	"github.com/Herrscherd/tgctl"
)

type bot interface {
	GetMe(ctx context.Context) (tgctl.User, error)
	GetUpdates(ctx context.Context, offset int64, timeout ...time.Duration) ([]tgctl.Update, error)
	SendMessage(ctx context.Context, message tgctl.OutgoingMessage) (tgctl.Message, error)
	EditMessageText(ctx context.Context, edit tgctl.EditMessageText) error
	EditMessageReplyMarkup(ctx context.Context, edit tgctl.EditMessageReplyMarkup) error
	SendMessageDraft(ctx context.Context, draft tgctl.MessageDraft) error
	SetMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error
	AnswerCallbackQuery(ctx context.Context, answer tgctl.CallbackAnswer) error
	GetFile(ctx context.Context, fileID string) (tgctl.File, error)
	Download(ctx context.Context, filePath string, limit int64, dst io.Writer) error
}

var _ bot = (*tgctl.Client)(nil)
