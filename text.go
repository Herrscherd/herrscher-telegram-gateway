package telegram

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

//go:embed locales
var localeFS embed.FS

const (
	operatorLanguageEnv = "HERRSCHER_LANG"
	detailArg           = "Detail"
	detailMaxRunes      = 3000
	unexpectedKey       = "telegram.notice.unexpected"
	noDetailKey         = "telegram.notice.no_detail"
)

var errUndeclaredKey = errors.New("undeclared message key")

type localeRoot struct{}

func (localeRoot) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	file, err := localeFS.Open(path.Join("locales", name))
	if err != nil {
		return nil, fmt.Errorf("telegram locales: %w", err)
	}
	return file, nil
}

type messages struct {
	catalog  *i18n.Catalog
	renderer *contracts.Renderer
	lang     i18n.Language
	log      *slog.Logger
}

func newMessages(lang i18n.Language, log *slog.Logger) (messages, error) {
	catalog, err := i18n.NewCatalog(log, localeRoot{})
	if err != nil {
		return messages{}, fmt.Errorf("telegram gateway: message catalog: %w", err)
	}
	return messages{catalog: catalog, renderer: contracts.NewRenderer(), lang: lang, log: log}, nil
}

func operatorLanguage(raw string, log *slog.Logger) i18n.Language {
	if raw == "" {
		return i18n.English
	}
	lang, err := i18n.ParseLanguage(raw)
	if err != nil {
		log.Warn("HERRSCHER_LANG is not a supported language, the gateway's texts stay in English", "value", raw, "err", err)
		return i18n.English
	}
	return lang
}

func operatorMessages(log *slog.Logger) (messages, error) {
	return newMessages(operatorLanguage(os.Getenv(operatorLanguageEnv), log), log)
}

func (m messages) resolve(lang i18n.Language) i18n.Language {
	if lang == 0 {
		return m.lang
	}
	return lang
}

func (m messages) say(key string, params map[string]any) string {
	return m.sayIn(m.lang, key, params)
}

func (m messages) sayIn(lang i18n.Language, key string, params map[string]any) string {
	if !m.catalog.Has(key) {
		m.log.Warn("message not rendered", "key", key, "err", errUndeclaredKey)
		return key
	}
	text, err := m.catalog.Message(lang, key, params)
	if err != nil {
		m.log.Warn("message not rendered", "key", key, "err", err)
		return key
	}
	return text
}

func (m messages) notice(lang i18n.Language, notice contracts.Notice) (formatted, error) {
	if notice.Code == contracts.CodeUnexpected {
		return m.unexpected(lang, detailOf(notice)), nil
	}
	text, err := m.renderer.Render(lang, notice)
	if err != nil {
		return formatted{}, fmt.Errorf("render notice %s: %w", notice.Code.Key(), err)
	}
	return plain(text), nil
}

func (m messages) noticeText(lang i18n.Language, notice contracts.Notice) formatted {
	text, err := m.notice(lang, notice)
	if err != nil {
		m.log.Warn("notice not rendered", "code", notice.Code.Key(), "err", err)
		return m.unexpected(lang, err.Error())
	}
	return text
}

func (m messages) unexpected(lang i18n.Language, detail string) formatted {
	var out formatter
	out.write(m.sayIn(lang, unexpectedKey, nil) + "\n")
	if detail == "" {
		out.write(m.sayIn(lang, noDetailKey, nil))
		return out.result()
	}
	out.span(entityPre, clipRunes(detail, detailMaxRunes), entity{})
	return out.result()
}

func detailOf(notice contracts.Notice) string {
	for _, arg := range notice.Args {
		if arg.Name() == detailArg {
			return fmt.Sprint(arg.Value())
		}
	}
	return ""
}

func clipRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
