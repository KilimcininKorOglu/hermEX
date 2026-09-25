package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// storeTaskBody files a task whose body is the given text, the way the early web
// client stored one.
func storeTaskBody(t *testing.T, s *Store, subject, body string) int64 {
	t.Helper()
	id, err := s.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: oxtask.MessageClass},
		{Tag: mapi.PrSubject, Value: subject},
		{Tag: mapi.PrBody, Value: body},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// readTask reads a stored task through the task model.
func readTask(t *testing.T, s *Store, id int64) oxtask.Task {
	t.Helper()
	msg, err := s.OpenMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	task, err := oxtask.FromProps(msg.Props, s.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// TestOpenUpgradesAJSONBodyTask reopens a mailbox holding a task in the early
// JSON-body shape: the task keeps its id and reads with its own description,
// due date and completion, while a task with any other body is left alone.
func TestOpenUpgradesAJSONBodyTask(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := storeTaskBody(t, s, "Write the report",
		`{"uid":"","summary":"Write the report","description":"Q2","due":"2026-06-30","completed":true}`)
	other := storeTaskBody(t, s, "Notes", `{"summary":"Notes","colour":"red"}`)
	s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := readTask(t, s, legacy)
	if got.Subject != "Write the report" || got.Body != "Q2" || !got.Complete {
		t.Errorf("upgraded task = %+v, want the JSON's summary, description and completion", got)
	}
	if !got.Due.Equal(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("upgraded due = %v, want 2026-06-30", got.Due)
	}
	if body := readTask(t, s, other).Body; body != `{"summary":"Notes","colour":"red"}` {
		t.Errorf("a body outside the early shape was rewritten to %q", body)
	}
}
