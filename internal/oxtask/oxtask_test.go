package oxtask

import (
	"testing"
	"time"

	"hermex/internal/mapi"
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
