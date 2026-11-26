package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	telegramAPIBase   = "https://api.telegram.org"
	responseMaxBytes  = 8 << 20
	pollTimeoutSecond = 50
	requestSlack      = 20 * time.Second
	reactionEmojiType = "emoji"
	notModifiedReason = "message is not modified"
)

var (
	errRequest      = errors.New("telegram request could not be built")
	errTransport    = errors.New("telegram unreachable")
	errBadResponse  = errors.New("telegram answered with an unreadable response")
	errBadFilePath  = errors.New("telegram gave a file path that is not a relative path")
	errFileTooLarge = errors.New("file is bigger than the cap")
	errFileStatus   = errors.New("telegram refused the file download")
)

type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return "telegram " + e.Method + ": " + strconv.Itoa(e.Code) + " " + e.Description
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type botAPI struct {
	base   string
	token  string
	client *http.Client
}

func newBotAPI(base, token string) *botAPI {
	return &botAPI{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: &http.Client{Timeout: pollTimeoutSecond*time.Second + requestSlack},
	}
}

func (b *botAPI) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/bot"+b.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	data, err := b.fetch(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	var envelope apiResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("telegram %s: %w", method, errBadResponse)
	}
	if !envelope.OK {
		return envelope.failure(method)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("telegram %s: %w", method, errBadResponse)
	}
	return nil
}

func (b *botAPI) fetch(req *http.Request) ([]byte, error) {
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, b.transportFailure(err)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseMaxBytes))
	err = errors.Join(err, resp.Body.Close())
	if err != nil {
		return nil, b.transportFailure(err)
	}
	return data, nil
}

func (b *botAPI) transportFailure(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errTransport, err)
	}
	return fmt.Errorf("%w: %s", errTransport, b.redact(err.Error()))
}

func (b *botAPI) redact(text string) string {
	if b.token == "" {
		return text
	}
	return strings.ReplaceAll(text, b.token, "[redacted]")
}

func (r apiResponse) failure(method string) error {
	failure := &apiError{Method: method, Code: r.ErrorCode, Description: r.Description}
	if r.Parameters != nil && r.Parameters.RetryAfter > 0 {
		failure.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
	}
	return failure
}

func unmodified(err error) bool {
	failure, ok := errors.AsType[*apiError](err)
	return ok && failure.Code == http.StatusBadRequest && strings.Contains(failure.Description, notModifiedReason)
}

func retryAfter(err error) (time.Duration, bool) {
	failure, ok := errors.AsType[*apiError](err)
	if !ok || failure.RetryAfter <= 0 {
		return 0, false
	}
	return failure.RetryAfter, true
}

func (b *botAPI) getMe(ctx context.Context) (botIdentity, error) {
	var identity botIdentity
	err := b.call(ctx, "getMe", struct{}{}, &identity)
	return identity, err
}

func (b *botAPI) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var updates []update
	request := updatesRequest{
		Offset:         offset,
		Limit:          updatesPerPoll,
		Timeout:        pollTimeoutSecond,
		AllowedUpdates: []string{"message", "callback_query", "stopped_message_generation"},
	}
	err := b.call(ctx, "getUpdates", request, &updates)
	return updates, err
}

func (b *botAPI) sendMessage(ctx context.Context, out outgoing) (message, error) {
	var sent message
	err := b.call(ctx, "sendMessage", out, &sent)
	return sent, err
}

func (b *botAPI) editMessageText(ctx context.Context, edit textEdit) error {
	return b.call(ctx, "editMessageText", edit, nil)
}

func (b *botAPI) editMessageReplyMarkup(ctx context.Context, edit markupEdit) error {
	return b.call(ctx, "editMessageReplyMarkup", edit, nil)
}

func (b *botAPI) sendMessageDraft(ctx context.Context, next draft) error {
	return b.call(ctx, "sendMessageDraft", next, nil)
}

func (b *botAPI) setMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	request := reaction{ChatID: chatID, MessageID: messageID, Reaction: []reactionType{}}
	if emoji != "" {
		request.Reaction = []reactionType{{Type: reactionEmojiType, Emoji: emoji}}
	}
	return b.call(ctx, "setMessageReaction", request, nil)
}

func (b *botAPI) answerCallbackQuery(ctx context.Context, answer callbackAnswer) error {
	return b.call(ctx, "answerCallbackQuery", answer, nil)
}

func (b *botAPI) getFile(ctx context.Context, fileID string) (remoteFile, error) {
	var file remoteFile
	err := b.call(ctx, "getFile", fileRequest{FileID: fileID}, &file)
	return file, err
}

func (b *botAPI) download(ctx context.Context, filePath string, limit int64, dst io.Writer) error {
	if !relativeFilePath(filePath) {
		return errBadFilePath
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/file/bot"+b.token+"/"+filePath, http.NoBody)
	if err != nil {
		return errRequest
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return b.transportFailure(err)
	}
	copyErr := b.copyCapped(resp, limit, dst)
	return errors.Join(copyErr, resp.Body.Close())
}

func (b *botAPI) copyCapped(resp *http.Response, limit int64, dst io.Writer) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d", errFileStatus, resp.StatusCode)
	}
	written, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return b.transportFailure(err)
	}
	if written > limit {
		return errFileTooLarge
	}
	return nil
}

func relativeFilePath(filePath string) bool {
	if filePath == "" || strings.HasPrefix(filePath, "/") || strings.ContainsAny(filePath, "?#\\") {
		return false
	}
	for segment := range strings.SplitSeq(filePath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
