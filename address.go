package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	voiceSeparator   = "@"
	sessionNameMax   = 64
	sessionHashChars = 16
	agentNamePattern = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`
)

var (
	agentName     = regexp.MustCompile(agentNamePattern)
	errBadAddress = errors.New("not a telegram conversation address")
)

type address struct {
	chatRef

	agent string
}

func parseAddress(raw string) (address, error) {
	chatPart, agentPart, voiced := strings.Cut(raw, voiceSeparator)
	ref, err := parseChatRef(chatPart)
	if err != nil {
		return address{}, err
	}
	if !voiced {
		return address{chatRef: ref}, nil
	}
	if !validAgent(agentPart) {
		return address{}, fmt.Errorf("%w: %q", errBadAddress, raw)
	}
	return address{chatRef: ref, agent: agentPart}, nil
}

func validAgent(name string) bool { return agentName.MatchString(name) }

func (a address) String() string {
	if a.agent == "" {
		return a.chatRef.String()
	}
	return a.chatRef.String() + voiceSeparator + a.agent
}

func (a address) sessionName() string {
	base := a.chatRef.sessionName()
	if a.agent == "" {
		return base
	}
	name := base + "-" + a.agent
	if len(name) <= sessionNameMax {
		return name
	}
	sum := sha256.Sum256([]byte(a.String()))
	return sessionPrefix + hex.EncodeToString(sum[:])[:sessionHashChars]
}
