package oxtask

import (
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
	mustNoErr(t, err, "ToProps")
	out, err := FromProps(props, r.resolve)
	mustNoErr(t, err, "FromProps")

	wantEq(t, out.Subject, in.Subject, "the subject")
	wantEq(t, out.Body, in.Body, "the body")
	wantTime(t, out.Start, in.Start, "the start")
	wantTime(t, out.Due, in.Due, "the due date")
	wantTrue(t, out.Complete, "the completion flag")
	wantTime(t, out.DateCompleted, in.DateCompleted, "the completion date")
	wantTrue(t, out.ReminderSet, "the reminder flag")
	wantTime(t, out.ReminderTime, in.ReminderTime, "the reminder time")
	wantEq(t, out.Importance, in.Importance, "the importance")
	wantEq(t, out.Sensitivity, in.Sensitivity, "the sensitivity")
	wantStrings(t, out.Categories, in.Categories, "the categories")
}

// TestTaskMessageClass confirms ToProps stamps the task class.
func TestTaskMessageClass(t *testing.T) {
	r := newFakeResolver()
	props, err := ToProps(Task{Subject: "x", Importance: -1, Sensitivity: -1}, r.resolve)
	mustNoErr(t, err, "ToProps")
	v, _ := props.Get(mapi.PrMessageClass)
	wantEq(t, v, any(MessageClass), "the message class")
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
	mustNoErr(t, err, "ToProps")
	rruleTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrenceRule], mapi.PtUnicode)
	blobTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrence], mapi.PtBinary)
	v, _ := props.Get(rruleTag)
	wantEq(t, v, any(in.RecurrenceRule), "the preserved RRULE text")

	blob, ok := props.Get(blobTag)
	wantTrue(t, ok, "the PidLidTaskRecurrence blob of a recurring task")
	b, ok := blob.([]byte)
	if !ok || len(b) < 8 {
		t.Fatalf("blob = %T len=%d, want a []byte header", blob, len(b))
	}
	// ReaderVersion 0x3004 at the head proves this is the MS-OXOCAL RecurrencePattern.
	wantEq(t, uint16(b[0])|uint16(b[1])<<8, uint16(0x3004), "the blob ReaderVersion")
}

// TestTaskRecurrenceBlobDecode proves a task authored by a MAPI client (which writes
// only the PidLidTaskRecurrence blob, no RRULE text) is read back with the recurrence
// restored, so the EAS/webmail paths see the same series Outlook wrote.
func TestTaskRecurrenceBlobDecode(t *testing.T) {
	r := newFakeResolver()
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	blob, err := recurrence.FromRRule("FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO", start)
	mustNoErr(t, err, "FromRRule")
	// A MAPI client writes the blob and no RRULE text. Build the props through
	// ToProps so the named ids are allocated, then drop the RRULE text to simulate
	// a MAPI-only author.
	full, err := ToProps(Task{
		Subject:        "Outlook recurring task",
		Start:          start,
		RecurrenceRule: "FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO",
	}, r.resolve)
	mustNoErr(t, err, "ToProps")
	rruleTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrenceRule], mapi.PtUnicode)
	blobTag := mapi.MakeTag(r.ids[mapi.NameTaskRecurrence], mapi.PtBinary)
	full.Set(blobTag, blob) // force the blob a MAPI client writes
	full.Set(rruleTag, "")  // MAPI client writes no RRULE text

	out, err := FromProps(full, r.resolve)
	mustNoErr(t, err, "FromProps")
	if out.RecurrenceRule == "" {
		t.Fatal("FromProps did not decode the PidLidTaskRecurrence blob to an RRULE")
	}
	wantContains(t, out.RecurrenceRule, "FREQ=WEEKLY", "the decoded RRULE")
	wantContains(t, out.RecurrenceRule, "COUNT=5", "the decoded RRULE")
	wantContains(t, out.RecurrenceRule, "BYDAY=MO", "the decoded RRULE")
}
