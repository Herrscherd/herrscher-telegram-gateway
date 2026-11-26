package telegram

type update struct {
	UpdateID          int64              `json:"update_id"`
	Message           *message           `json:"message,omitempty"`
	CallbackQuery     *callbackQuery     `json:"callback_query,omitempty"`
	StoppedGeneration *generationStopped `json:"stopped_message_generation,omitempty"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	ThreadID        int64       `json:"message_thread_id,omitempty"`
	From            *user       `json:"from,omitempty"`
	Chat            chat        `json:"chat"`
	Text            string      `json:"text,omitempty"`
	Entities        []entity    `json:"entities,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	CaptionEntities []entity    `json:"caption_entities,omitempty"`
	ReplyTo         *message    `json:"reply_to_message,omitempty"`
	IsTopicMessage  bool        `json:"is_topic_message,omitempty"`
	Document        *document   `json:"document,omitempty"`
	Photo           []photoSize `json:"photo,omitempty"`
}

type user struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	IsForum bool   `json:"is_forum,omitempty"`
}

type entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
	User     *user  `json:"user,omitempty"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size,omitempty"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type generationStopped struct {
	Chat     chat  `json:"chat"`
	ThreadID int64 `json:"message_thread_id,omitempty"`
	DraftID  int64 `json:"draft_id"`
}

type remoteFile struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size,omitempty"`
	FilePath string `json:"file_path,omitempty"`
}

type outgoing struct {
	ChatID          int64            `json:"chat_id"`
	ThreadID        int64            `json:"message_thread_id,omitempty"`
	Text            string           `json:"text"`
	Entities        []entity         `json:"entities,omitempty"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup     *inlineKeyboard  `json:"reply_markup,omitempty"`
	LinkPreview     *linkPreview     `json:"link_preview_options,omitempty"`
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

type inlineKeyboard struct {
	Rows [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type textEdit struct {
	ChatID      int64           `json:"chat_id"`
	MessageID   int64           `json:"message_id"`
	Text        string          `json:"text"`
	Entities    []entity        `json:"entities,omitempty"`
	LinkPreview *linkPreview    `json:"link_preview_options,omitempty"`
	ReplyMarkup *inlineKeyboard `json:"reply_markup,omitempty"`
}

type markupEdit struct {
	ChatID      int64          `json:"chat_id"`
	MessageID   int64          `json:"message_id"`
	ReplyMarkup inlineKeyboard `json:"reply_markup"`
}

type draft struct {
	ChatID   int64    `json:"chat_id"`
	ThreadID int64    `json:"message_thread_id,omitempty"`
	DraftID  int64    `json:"draft_id"`
	Text     string   `json:"text"`
	Entities []entity `json:"entities,omitempty"`
	CanStop  bool     `json:"can_stop"`
}

type reactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

type reaction struct {
	ChatID    int64          `json:"chat_id"`
	MessageID int64          `json:"message_id"`
	Reaction  []reactionType `json:"reaction"`
}

type callbackAnswer struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

type updatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type fileRequest struct {
	FileID string `json:"file_id"`
}

type botIdentity struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
