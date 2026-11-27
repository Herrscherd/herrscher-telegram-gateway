package telegram

import (
	"strings"
	"testing"
)

func TestAHeaderNamesTheAgentInBoldBeforeTheBody(t *testing.T) {
	header := agentHeader("ana")
	if header.text != "ana\n" || len(header.entities) != 1 {
		t.Fatalf("header = %+v", header)
	}
	bold := header.entities[0]
	if bold.Type != entityBold || bold.Offset != 0 || bold.Length != len("ana") {
		t.Fatalf("bold = %+v", bold)
	}
	framed := plain("hi").prepend(header)
	if framed.text != "ana\nhi" {
		t.Fatalf("framed = %q", framed.text)
	}
}

func TestAHeaderCountsAgainstTheSplitLimit(t *testing.T) {
	speak := speaker{header: agentHeader("ana")}
	parts := plain(strings.Repeat("a", 2*messageMaxUnits)).split(speak.budget())
	if len(parts) < 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	for index, part := range parts {
		if framed := speak.frame(part); framed.units() > messageMaxUnits {
			t.Fatalf("part %d is %d units with its header, over %d", index, framed.units(), messageMaxUnits)
		}
	}
}
