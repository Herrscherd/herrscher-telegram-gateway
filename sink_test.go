package telegram

import "testing"

func TestARefusedNoteNeverClearsABindingItDidNotSet(t *testing.T) {
	target := &sink{turn: &turnView{asked: notedPrompt{author: "ana", text: "go"}}, awaitingAck: make(map[uint64]ackOutcome)}
	target.noteUser(t.Context(), notedPrompt{id: 10, author: "ana", text: "go", note: 1})
	target.forgetUser(notedPrompt{id: 10, author: "ana", text: "go", note: 2})
	if target.turn.prompt != 10 || target.turn.promptNote != 1 {
		t.Fatalf("a refusal of another note cleared the binding: prompt %d, note %d", target.turn.prompt, target.turn.promptNote)
	}
}
