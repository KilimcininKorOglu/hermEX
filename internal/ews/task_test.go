package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// TestFindItemAndGetItemTask confirms a task in the Tasks folder serializes as a
// <t:Task> over EWS (not a generic <t:Message>), reading the same shared properties
// the web backend and ActiveSync use.
func TestFindItemAndGetItemTask(t *testing.T) {
	ts, dir := seededWithMessage(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task := oxtask.Task{
		Subject:     "Ship it",
		Body:        "the notes",
		Due:         time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Importance:  2,
		Sensitivity: -1,
		Categories:  []string{"Work"},
	}
	props, err := oxtask.ToProps(task, st.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	resp, out := soapPost(t, ts, findItemReq("tasks"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("FindItem status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Ship it") {
		t.Errorf("FindItem missing task subject: %s", out)
	}
	if !strings.Contains(out, "DueDate") {
		t.Errorf("FindItem task not serialized as a <t:Task> (no DueDate): %s", out)
	}
	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}

	_, out2 := soapPost(t, ts, getItemReq(itemID[1]), true)
	if !strings.Contains(out2, `ResponseClass="Success"`) {
		t.Errorf("GetItem not success: %s", out2)
	}
	if !strings.Contains(out2, "the notes") {
		t.Errorf("GetItem task missing body: %s", out2)
	}
	if !strings.Contains(out2, "DueDate") {
		t.Errorf("GetItem task missing DueDate: %s", out2)
	}
	if !strings.Contains(out2, "Work") {
		t.Errorf("GetItem task missing category: %s", out2)
	}
}
