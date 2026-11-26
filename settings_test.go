package telegram

import (
	"errors"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestABotNobodyMayTalkToRefusesToStart(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		want     error
	}{
		{"no token", map[string]string{ownerSetting: "1"}, errNoToken},
		{"no audience", map[string]string{tokenSetting: "t"}, errNoAudience},
		{"bad owner", map[string]string{tokenSetting: "t", ownerSetting: "@ana"}, errBadID},
		{"bad switch", map[string]string{tokenSetting: "t", allowAllSetting: "maybe"}, errBadSwitchOn},
		{"allow all", map[string]string{tokenSetting: "t", allowAllSetting: "on"}, nil},
		{"users only", map[string]string{tokenSetting: "t", usersSetting: "5, 6"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSettings(contracts.PluginConfig{Settings: tc.settings})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAConversationIDRoundTripsAndNamesItsSession(t *testing.T) {
	ref, err := parseChatRef("-100500:7")
	if err != nil || ref != (chatRef{chat: -100500, thread: 7}) || ref.String() != "-100500:7" || ref.sessionName() != "tg-n100500-7" {
		t.Fatalf("ref = %+v, %v", ref, err)
	}
	for _, bad := range []string{"", "0", "abc", "5:0", "5:x"} {
		if _, err := parseChatRef(bad); !errors.Is(err, errBadConversation) {
			t.Errorf("parseChatRef(%q) = %v", bad, err)
		}
	}
}
