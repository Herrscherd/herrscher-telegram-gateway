package telegram

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const (
	tokenSetting     = "token"
	botVariable      = "TELEGRAM_BOT_TOKEN"
	ownerSetting     = "owner"
	usersSetting     = "allowed_users"
	chatsSetting     = "allowed_chats"
	allowAllSetting  = "allow_all_users"
	agentSetting     = "agent"
	chatTypePrivate  = "private"
	chatTypeGroup    = "group"
	chatTypeSuper    = "supergroup"
	listSeparators   = ", \t\n"
	switchOnValues   = "1 true yes on"
	switchOffValues  = "0 false no off"
	settingKeyPrefix = "telegram.setting."
)

var (
	errNoToken     = errors.New("telegram gateway: no bot token, set " + botVariable)
	errNoAudience  = errors.New("telegram gateway: nobody may talk to the bot, set owner, allowed_users or allow_all_users")
	errBadID       = errors.New("telegram gateway: not a numeric telegram id")
	errBadSwitchOn = errors.New("telegram gateway: not an on or off value")
)

type idSet map[int64]bool

type settings struct {
	token    string
	owner    int64
	users    idSet
	chats    idSet
	allowAll bool
	agent    string
}

func settingSpecs() []contracts.Setting {
	return []contracts.Setting{
		{Key: tokenSetting, Env: botVariable, Help: settingKeyPrefix + tokenSetting, Required: true},
		{Key: ownerSetting, Env: "TELEGRAM_USER_ID", Help: settingKeyPrefix + ownerSetting},
		{Key: usersSetting, Env: "TELEGRAM_ALLOWED_USERS", Help: settingKeyPrefix + usersSetting},
		{Key: chatsSetting, Env: "TELEGRAM_ALLOWED_CHATS", Help: settingKeyPrefix + chatsSetting},
		{Key: allowAllSetting, Env: "TELEGRAM_ALLOW_ALL_USERS", Help: settingKeyPrefix + allowAllSetting},
		{Key: agentSetting, Env: "TELEGRAM_AGENT", Help: settingKeyPrefix + agentSetting},
	}
}

func parseSettings(cfg contracts.PluginConfig) (settings, error) {
	parsed := settings{token: strings.TrimSpace(cfg.Get(tokenSetting)), agent: strings.TrimSpace(cfg.Get(agentSetting))}
	if parsed.token == "" {
		return settings{}, errNoToken
	}
	var err error
	if parsed.owner, err = parseOptionalID(ownerSetting, cfg.Get(ownerSetting)); err != nil {
		return settings{}, err
	}
	if parsed.users, err = parseIDs(usersSetting, cfg.Get(usersSetting)); err != nil {
		return settings{}, err
	}
	if parsed.chats, err = parseIDs(chatsSetting, cfg.Get(chatsSetting)); err != nil {
		return settings{}, err
	}
	if parsed.allowAll, err = parseSwitch(allowAllSetting, cfg.Get(allowAllSetting)); err != nil {
		return settings{}, err
	}
	if parsed.owner == 0 && len(parsed.users) == 0 && !parsed.allowAll {
		return settings{}, errNoAudience
	}
	return parsed, nil
}

func parseOptionalID(key, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("%w: %s", errBadID, key)
	}
	return parsed, nil
}

func parseIDs(key, raw string) (idSet, error) {
	ids := make(idSet)
	for field := range strings.FieldsFuncSeq(raw, func(char rune) bool { return strings.ContainsRune(listSeparators, char) }) {
		parsed, err := parseOptionalID(key, field)
		if err != nil {
			return nil, err
		}
		ids[parsed] = true
	}
	return ids, nil
}

func parseSwitch(key, raw string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case value == "":
		return false, nil
	case containsWord(switchOnValues, value):
		return true, nil
	case containsWord(switchOffValues, value):
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", errBadSwitchOn, key)
}

func containsWord(words, word string) bool {
	for candidate := range strings.FieldsSeq(words) {
		if candidate == word {
			return true
		}
	}
	return false
}

func (s settings) authorizes(userID int64) bool {
	if userID == 0 {
		return false
	}
	return s.allowAll || userID == s.owner || s.users[userID]
}

func (s settings) admits(msg *tgctl.Message) bool {
	if msg.From == nil || msg.From.IsBot || !s.authorizes(msg.From.ID) {
		return false
	}
	switch msg.Chat.Type {
	case chatTypePrivate:
		return true
	case chatTypeGroup, chatTypeSuper:
		return s.chats[msg.Chat.ID]
	}
	return false
}
