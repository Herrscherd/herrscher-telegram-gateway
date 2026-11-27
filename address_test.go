package telegram

import (
	"regexp"
	"strings"
	"testing"
)

func TestAVoicedAddressRoundTripsThroughItsText(t *testing.T) {
	for _, raw := range []string{"42", "-100500", "-100500:7", "42@ana", "-100500:7@ana-bot_2"} {
		parsed, err := parseAddress(raw)
		if err != nil {
			t.Fatalf("parseAddress(%q): %v", raw, err)
		}
		if parsed.String() != raw {
			t.Fatalf("round trip of %q gave %q", raw, parsed.String())
		}
	}
}

func TestAnAddressWithAMalformedAgentOrChatIsRefused(t *testing.T) {
	for _, raw := range []string{"@ana", "42@", "42@bad name", "42@a@b", "42@-ana", "42@a*b", "42:0@ana", "x@ana"} {
		if _, err := parseAddress(raw); err == nil {
			t.Errorf("parseAddress(%q) accepted a malformed address", raw)
		}
	}
}

func TestAVoicedSessionNameFitsTheAgentPatternAndIsStable(t *testing.T) {
	pattern := regexp.MustCompile(agentNamePattern)
	short, err := parseAddress("-100500:7@ana")
	if err != nil {
		t.Fatal(err)
	}
	if got := short.sessionName(); got != "tg-n100500-7-ana" {
		t.Fatalf("short session name = %q", got)
	}
	long, err := parseAddress("-100500@" + strings.Repeat("a", 60))
	if err != nil {
		t.Fatal(err)
	}
	first, second := long.sessionName(), long.sessionName()
	if first != second || len(first) > sessionNameMax || !pattern.MatchString(first) {
		t.Fatalf("long session name = %q (%d), second %q", first, len(first), second)
	}
}
