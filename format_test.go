package telegram

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestMarkdownBecomesEntitiesCountedInUTF16(t *testing.T) {
	got := formatMarkdown("# Plan\n🙂 **bold** and `code`, see [docs](https://example.com) or snake_case_name\n```go\nx := 1\n```")

	wantText := "Plan\n🙂 bold and code, see docs or snake_case_name\nx := 1"
	if got.text != wantText {
		t.Fatalf("text = %q", got.text)
	}
	units := utf16.Encode([]rune(got.text))
	covered := make([]string, 0, len(got.entities))
	for _, span := range got.entities {
		covered = append(covered, span.Type+"="+string(utf16.Decode(units[span.Offset:span.Offset+span.Length])))
	}
	want := "bold=Plan bold=bold code=code text_link=docs pre=x := 1"
	if strings.Join(covered, " ") != want {
		t.Fatalf("entities = %v", covered)
	}
	if got.entities[3].URL != "https://example.com" || got.entities[4].Language != "go" {
		t.Fatalf("entity details = %+v", got.entities)
	}
}

func TestAnUnderscoreInsideAWordStaysLiteral(t *testing.T) {
	for _, text := range []string{"file_name_ and snake_case_name", "/srv/a/x_private_/y", "a__b__c", "é_x_ y", "x_é_", "a _x_é b"} {
		if got := formatMarkdown(text); got.text != text || len(got.entities) != 0 {
			t.Errorf("formatMarkdown(%q) = %q with %d entities", text, got.text, len(got.entities))
		}
	}
	if got := formatMarkdown("an _aside_ here"); got.text != "an aside here" || len(got.entities) != 1 {
		t.Fatalf("a standalone underscore pair = %q, %+v", got.text, got.entities)
	}
}

func TestALongAnswerSplitsAtLinesAndKeepsEachEntityInsideItsPart(t *testing.T) {
	line := strings.Repeat("a", 3000)
	text := formatMarkdown(line + "\n**" + strings.Repeat("b", 3000) + "**")

	parts := text.split(messageMaxUnits)

	if len(parts) != 2 || parts[0].text != line+"\n" {
		t.Fatalf("parts = %d, first ends %q", len(parts), parts[0].text[len(parts[0].text)-3:])
	}
	if len(parts[0].entities) != 0 || len(parts[1].entities) != 1 || parts[1].entities[0].Offset != 0 || parts[1].entities[0].Length != 3000 {
		t.Fatalf("entities = %+v / %+v", parts[0].entities, parts[1].entities)
	}
}

func TestASplitNeverCutsASurrogatePair(t *testing.T) {
	text := plain(strings.Repeat("a", messageMaxUnits-1) + "🙂tail")

	parts := text.split(messageMaxUnits)

	if len(parts) != 2 || !strings.HasPrefix(parts[1].text, "🙂") {
		t.Fatalf("parts = %d, second = %q", len(parts), parts[1].text)
	}
}
