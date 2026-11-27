package telegram

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Herrscherd/tgctl"
)

const (
	messageMaxUnits = 4096
	fence           = "```"
	entityBold      = "bold"
	entityItalic    = "italic"
	entityStrike    = "strikethrough"
	entityCode      = "code"
	entityPre       = "pre"
	entityTextLink  = "text_link"
	entityMention   = "mention"
	entityCommand   = "bot_command"
	entityUserLink  = "text_mention"
	headingMaxDepth = 6
	lowSurrogate    = 0xdc00
)

type formatted struct {
	text     string
	entities []tgctl.Entity
}

type formatter struct {
	pieces   []string
	units    int
	entities []tgctl.Entity
}

func unitsOf(text string) int { return len(utf16.Encode([]rune(text))) }

func (f *formatter) write(text string) {
	f.pieces = append(f.pieces, text)
	f.units += unitsOf(text)
}

func (f *formatter) span(kind, text string, extra tgctl.Entity) {
	if text == "" {
		return
	}
	extra.Type, extra.Offset, extra.Length = kind, f.units, unitsOf(text)
	f.entities = append(f.entities, extra)
	f.write(text)
}

func (f *formatter) result() formatted {
	return formatted{text: strings.Join(f.pieces, ""), entities: f.entities}
}

func plain(text string) formatted { return formatted{text: text} }

func formatMarkdown(source string) formatted {
	var out formatter
	lines := strings.Split(source, "\n")
	for index := 0; index < len(lines); index++ {
		if index > 0 {
			out.write("\n")
		}
		line := lines[index]
		if language, opens := strings.CutPrefix(strings.TrimSpace(line), fence); opens {
			index = out.codeBlock(lines, index+1, strings.TrimSpace(language))
			continue
		}
		out.line(line)
	}
	return out.result()
}

func (f *formatter) codeBlock(lines []string, start int, language string) int {
	end := start
	for end < len(lines) && strings.TrimSpace(lines[end]) != fence {
		end++
	}
	f.span(entityPre, strings.Join(lines[start:end], "\n"), tgctl.Entity{Language: language})
	return end
}

func (f *formatter) line(line string) {
	if heading, ok := headingText(line); ok {
		f.span(entityBold, heading, tgctl.Entity{})
		return
	}
	f.inline(line)
}

func headingText(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, "#")
	depth := len(line) - len(trimmed)
	if depth == 0 || depth > headingMaxDepth || !strings.HasPrefix(trimmed, " ") {
		return "", false
	}
	return strings.TrimSpace(trimmed), true
}

type inlineMark struct {
	open string
	kind string
}

func inlineMarks() []inlineMark {
	return []inlineMark{
		{open: "`", kind: entityCode},
		{open: "**", kind: entityBold},
		{open: "__", kind: entityBold},
		{open: "~~", kind: entityStrike},
		{open: "*", kind: entityItalic},
		{open: "_", kind: entityItalic},
	}
}

func (f *formatter) inline(text string) {
	insideWord := false
	for text != "" {
		consumed := f.link(text)
		if consumed == 0 {
			consumed = f.marked(text, insideWord)
		}
		if consumed == 0 {
			next := nextSpecial(text[1:]) + 1
			f.write(text[:next])
			consumed = next
		}
		last, _ := utf8.DecodeLastRuneInString(text[:consumed])
		insideWord = isWordRune(last)
		text = text[consumed:]
	}
}

func nextSpecial(text string) int {
	index := strings.IndexAny(text, "`*_~[")
	if index < 0 {
		return len(text)
	}
	return index
}

func (f *formatter) marked(text string, insideWord bool) int {
	for _, mark := range inlineMarks() {
		if !strings.HasPrefix(text, mark.open) {
			continue
		}
		rest := text[len(mark.open):]
		closing := strings.Index(rest, mark.open)
		if closing <= 0 || !tight(rest[:closing]) {
			continue
		}
		if mark.kind != entityCode && wordBoundaryNeeded(mark.open) && (insideWord || !boundaryAfter(rest[closing+len(mark.open):])) {
			continue
		}
		f.span(mark.kind, rest[:closing], tgctl.Entity{})
		return len(mark.open) + closing + len(mark.open)
	}
	return 0
}

func tight(inner string) bool {
	return strings.TrimSpace(inner) == inner
}

func wordBoundaryNeeded(open string) bool { return open == "_" || open == "__" }

func boundaryAfter(rest string) bool {
	if rest == "" {
		return true
	}
	next, _ := utf8.DecodeRuneInString(rest)
	return !isWordRune(next)
}

func isWordRune(char rune) bool {
	return char == '_' || unicode.IsLetter(char) || unicode.IsDigit(char)
}

func (f *formatter) link(text string) int {
	if !strings.HasPrefix(text, "[") {
		return 0
	}
	label, rest, found := strings.Cut(text[1:], "](")
	if !found || label == "" || strings.Contains(label, "]") {
		return 0
	}
	target, _, closed := strings.Cut(rest, ")")
	if !closed || !webURL(target) {
		return 0
	}
	f.span(entityTextLink, label, tgctl.Entity{URL: target})
	return 1 + len(label) + len("](") + len(target) + 1
}

func webURL(target string) bool {
	return (strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://")) && !strings.ContainsAny(target, " \t\n")
}

func (m formatted) split(limit int) []formatted {
	units := utf16.Encode([]rune(m.text))
	if len(units) <= limit {
		if len(units) == 0 {
			return nil
		}
		return []formatted{m}
	}
	var parts []formatted
	start := 0
	for start < len(units) {
		end := cutPoint(units, start, limit)
		parts = append(parts, formatted{
			text:     string(utf16.Decode(units[start:end])),
			entities: clipEntities(m.entities, start, end),
		})
		start = end
	}
	return parts
}

func cutPoint(units []uint16, start, limit int) int {
	end := start + limit
	if end >= len(units) {
		return len(units)
	}
	for index := end - 1; index > start+limit/2; index-- {
		if units[index] == '\n' {
			return index + 1
		}
	}
	if utf16.IsSurrogate(rune(units[end-1])) && units[end-1] < lowSurrogate {
		return end - 1
	}
	return end
}

func clipEntities(all []tgctl.Entity, start, end int) []tgctl.Entity {
	var clipped []tgctl.Entity
	for _, candidate := range all {
		from := max(candidate.Offset, start)
		until := min(candidate.Offset+candidate.Length, end)
		if until <= from {
			continue
		}
		candidate.Offset, candidate.Length = from-start, until-from
		clipped = append(clipped, candidate)
	}
	return clipped
}

func (m formatted) tail(limit int) formatted {
	parts := m.split(limit)
	if len(parts) == 0 {
		return formatted{}
	}
	return parts[len(parts)-1]
}
