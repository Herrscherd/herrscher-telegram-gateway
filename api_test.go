package telegram

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAFloodRefusalCarriesItsRetryDelay(t *testing.T) {
	fake := newFakeTelegram(t)
	fake.fail["sendMessage"] = http.StatusTooManyRequests

	_, err := newBotAPI(fake.server.URL, testToken).sendMessage(t.Context(), outgoing{ChatID: 1, Text: "hi"})

	wait, limited := retryAfter(err)
	if !limited || wait != time.Second {
		t.Fatalf("retry after = %v, %v from %v", wait, limited, err)
	}
}

func TestATransportFailureNeverPrintsTheToken(t *testing.T) {
	_, err := newBotAPI("http://127.0.0.1:1", testToken).getMe(t.Context())

	if !errors.Is(err, errTransport) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestADownloadRefusesAPathThatLeavesTheFileTree(t *testing.T) {
	api := newBotAPI("http://127.0.0.1:1", testToken)
	for _, path := range []string{"", "/etc/passwd", "documents/../../x", "a//b", "a?b"} {
		if err := api.download(t.Context(), path, 1, nil); !errors.Is(err, errBadFilePath) {
			t.Errorf("download(%q) = %v", path, err)
		}
	}
}
