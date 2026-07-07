package oxtask

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/recurrence"
)

// fakeResolver allocates a stable id per distinct named property, mirroring a store's
// GetNamedPropIDs.
type fakeResolver struct {
	ids  map[mapi.PropertyName]uint16
	next uint16
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{ids: map[mapi.PropertyName]uint16{}, next: 0x8000}
}

func (r *fakeResolver) resolve(create bool, names []mapi.PropertyName) ([]uint16, error) {
	out := make([]uint16, len(names))
	for i, n := range names {
		id, ok := r.ids[n]
		if !ok {
			if !create {
				continue // 0 = unresolved
			}
			id = r.next
			r.next++
			r.ids[n] = id
		}
		out[i] = id
	}
	return out, nil
}

// TestTaskRoundTrip proves a task survives the props conversion both ways, so every
// protocol that maps through oxtask reads the same task.
func TestTaskRoundTrip(t *testing.T) {
	r := newFakeResolver()
	in := Task{
		Subject:       "Ship release",
		Body:          "cut the tag",
		Start:         time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC),
		Due:           time.Date(2026, 7, 1, 17, 0, 0, 0, time.UTC),
		Complete:      true,
		DateCompleted: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		ReminderSet:   true,
		ReminderTime:  time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC),
		Importance:    2,
		Sensitivity:   0,
		Categories:    []string{"Work", "Urgent"},
	}
	props, err := ToProps(in, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	out, err := FromProps(props, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	if out.Subject != in.Subject || out.Body != in.Body {
		t.Errorf("subject/body = %q/%q", out.Subject, out.Body)
	}
	if !out.Start.Equal(in.Start) || !out.Due.Equal(in.Due) {
		t.Errorf("start/due = %v / %v", out.Start, out.Due)
	}
	if !out.Complete || !out.DateCompleted.Equal(in.DateCompleted) {
		t.Errorf("complete=%v dateCompleted=%v", out.Complete, out.DateCompleted)
	}
	if !out.ReminderSet || !out.ReminderTime.Equal(in.ReminderTime) {
		t.Errorf("reminderSet=%v reminderTime=%v", out.ReminderSet, out.ReminderTime)
	}
	if out.Importance != 2 || out.Sensitivity != 0 {
		t.Errorf("importance/sensitivity = %d/%d", out.Importance, out.Sensitivity)
	}
	if len(out.Categories) != 2 || out.Categories[0] != "Work" || out.Categories[1] != "Urgent" {
		t.Errorf("categories = %v", out.Categories)
	}
}

// TestTaskMessageClass confirms ToProps stamps the task class.
func TestTaskMessageClass(t *testing.T) {
	r := newFakeResolver()
	props, err := ToProps(Task{Subject: "x", Importance: -1, Sensitivity: -1}, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrMessageClass); v != MessageClass {
		t.Errorf("message class = %v, want %s", v, MessageClass)
	}
}

// TestTaskRecurrenceBlob proves a recurring task emits the MS-OXOCAL
// PidLidTaskRecurrence binary blob (0x8416) Outlook reads, alongside the RRULE text
// the EAS/webmail paths consume, so the recurrence is wire-compatible for a MAPI
// client instead of a webmail-only field.
func TestTaskRecurrenceBlob(t *testing.T) {
	r := newFakeResolver()
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	in := Task{
		Subject:        "Weekly status",
		Start:          start,
		RecurrenceRule: "FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO",
	}
	props, err := ToProps(in, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	rruleTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrenceRule], mapi.PtUnicode)
	blobTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrence], mapi.PtBinary)
	if v, ok := props.Get(rruleTag); !ok || v != in.RecurrenceRule {
		t.Errorf("RRULE text = %v ok=%v, want %q preserved", v, ok, in.RecurrenceRule)
	}
	blob, ok := props.Get(blobTag)
	if !ok {
		t.Fatal("PidLidTaskRecurrence blob not set for a recurring task")
	}
	b, ok := blob.([]byte)
	if !ok || len(b) < 8 {
		t.Fatalf("blob = %T len=%d, want a []byte header", blob, len(b))
	}
	// ReaderVersion 0x3004 at the head proves this is the MS-OXOCAL RecurrencePattern.
	if got := uint16(b[0]) | uint16(b[1])<<8; got != 0x3004 {
		t.Errorf("blob ReaderVersion = %#x, want 0x3004", got)
	}
}

// TestTaskRecurrenceBlobDecode proves a task authored by a MAPI client (which writes
// only the PidLidTaskRecurrence blob, no RRULE text) is read back with the recurrence
// restored, so the EAS/webmail paths see the same series Outlook wrote.
func TestTaskRecurrenceBlobDecode(t *testing.T) {
	r := newFakeResolver()
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	blob, err := recurrence.FromRRule("FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO", start)
	if err != nil {
		t.Fatal(err)
	}
	// A MAPI client writes the blob and no RRULE text. Build the props through
	// ToProps so the named ids are allocated, then drop the RRULE text to simulate
	// a MAPI-only author.
	full, err := ToProps(Task{
		Subject:        "Outlook recurring task",
		Start:          start,
		RecurrenceRule: "FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO",
	}, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	rruleTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrenceRule], mapi.PtUnicode)
	blobTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrence], mapi.PtBinary)
	full.Set(blobTag, blob) // force the blob a MAPI client writes
	full.Set(rruleTag, "")  // MAPI client writes no RRULE text

	out, err := FromProps(full, r.resolve)
	if err != nil {
		t.Fatal(err)
	}
	if out.RecurrenceRule == "" {
		t.Fatal("FromProps did not decode the PidLidTaskRecurrence blob to an RRULE")
	}
	if got := out.RecurrenceRule; !strings.Contains(got, "FREQ=WEEKLY") || !strings.Contains(got, "COUNT=5") || !strings.Contains(got, "BYDAY=MO") {
		t.Errorf("decoded RRULE = %q, want FREQ=WEEKLY;...;COUNT=5;BYDAY=MO", got)
	}
}
