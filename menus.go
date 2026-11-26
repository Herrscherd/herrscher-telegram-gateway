package telegram

import (
	"crypto/rand"
	"strconv"
	"strings"
	"sync"
)

const (
	menuCapacity    = 512
	choicePrefix    = "c"
	bindPrefix      = "b"
	callbackDivider = ":"
	callbackParts   = 3
)

type choice struct {
	value string
	label string
}

type menu struct {
	conv   string
	values []string
	labels []string
}

type menuTable struct {
	mu    sync.Mutex
	menus map[string]menu
	order []string
}

func newMenuTable() *menuTable {
	return &menuTable{menus: make(map[string]menu)}
}

func (t *menuTable) put(conv string, values, labels []string) string {
	token := newMenuToken()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.menus[token] = menu{conv: conv, values: values, labels: labels}
	t.order = append(t.order, token)
	for len(t.order) > menuCapacity {
		delete(t.menus, t.order[0])
		t.order = t.order[1:]
	}
	return token
}

func (t *menuTable) take(token, conv string, index int) (choice, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	found, ok := t.menus[token]
	if !ok || found.conv != conv || index < 0 || index >= len(found.values) {
		return choice{}, false
	}
	delete(t.menus, token)
	return choice{value: found.values[index], label: found.labels[index]}, true
}

func newMenuToken() string { return rand.Text() }

type callback struct {
	kind  string
	token string
	index int
}

func callbackData(kind, token string, index int) string {
	return strings.Join([]string{kind, token, strconv.Itoa(index)}, callbackDivider)
}

func parseCallback(data string) (callback, bool) {
	parts := strings.Split(data, callbackDivider)
	if len(parts) != callbackParts {
		return callback{}, false
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		return callback{}, false
	}
	if parts[0] != choicePrefix && parts[0] != bindPrefix {
		return callback{}, false
	}
	return callback{kind: parts[0], token: parts[1], index: index}, true
}

func keyboard(kind, token string, labels []string) *inlineKeyboard {
	rows := make([][]inlineButton, 0, len(labels))
	for index, label := range labels {
		rows = append(rows, []inlineButton{{Text: label, CallbackData: callbackData(kind, token, index)}})
	}
	return &inlineKeyboard{Rows: rows}
}
