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
	mustNoErr(t, "open the store", err)
	props, err := oxtask.ToProps(oxtask.Task{
		Subject:     "Ship it",
		Body:        "the notes",
		Due:         time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Importance:  2,
		Sensitivity: -1,
		Categories:  []string{"Work"},
	}, st.GetNamedPropIDs)
	mustNoErr(t, "render the task properties", err)
	_, err = st.CreateMessage(int64(mapi.PrivateFIDTasks), &oxcmail.Message{Props: props})
	mustNoErr(t, "store the task", err)
	st.Close()

	resp, out := soapPost(t, ts, findItemReq("tasks"), true)
	wantEq(t, "the FindItem status", resp.StatusCode, 200)
	wantContains(t, "the FindItem task subject", out, "Ship it")
	// A DueDate element is what proves the task was serialized as a <t:Task>.
	wantContains(t, "the FindItem task due date", out, "DueDate")

	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}

	_, out2 := soapPost(t, ts, getItemReq(itemID[1]), true)
	wantContains(t, "the GetItem response class", out2, `ResponseClass="Success"`)
	wantContains(t, "the GetItem task body", out2, "the notes")
	wantContains(t, "the GetItem task due date", out2, "DueDate")
	wantContains(t, "the GetItem task category", out2, "Work")
}

// TestFindItemAndGetItemNote confirms a sticky note serializes as a base <t:Item>
// (EWS has no Note type) carrying ItemClass="IPM.StickyNote", subject, and body.
func TestFindItemAndGetItemNote(t *testing.T) {
	ts, dir := seededWithMessage(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.StickyNote"},
		{Tag: mapi.PrSubject, Value: "Grocery"},
		{Tag: mapi.PrBody, Value: "milk and eggs"},
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDNotes), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	resp, out := soapPost(t, ts, findItemReq("notes"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("FindItem status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Grocery") {
		t.Errorf("FindItem missing note subject: %s", out)
	}
	if !strings.Contains(out, "IPM.StickyNote") {
		t.Errorf("FindItem note not a base Item with StickyNote class: %s", out)
	}
	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}

	_, out2 := soapPost(t, ts, getItemReq(itemID[1]), true)
	if !strings.Contains(out2, `ResponseClass="Success"`) {
		t.Errorf("GetItem not success: %s", out2)
	}
	if !strings.Contains(out2, "milk and eggs") {
		t.Errorf("GetItem note missing body: %s", out2)
	}
	if !strings.Contains(out2, "IPM.StickyNote") {
		t.Errorf("GetItem note missing ItemClass: %s", out2)
	}
}
