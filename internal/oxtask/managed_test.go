package oxtask

import (
	"slices"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// fullTask sets every field ToProps writes.
func fullTask() Task {
	day := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	return Task{
		Subject: "Write the report", Body: "Quarterly", Start: day, Due: day.AddDate(0, 0, 7),
		Complete: true, DateCompleted: day.AddDate(0, 0, 6), Status: 2, PercentComplete: 1,
		ReminderSet: true, ReminderTime: day.AddDate(0, 0, 6), Importance: 2, Sensitivity: 1,
		Categories: []string{"Red"}, RecurrenceRule: "FREQ=WEEKLY", Owner: "alice@hermex.test",
		Assigner: "bob@hermex.test", AcceptanceState: 2, FCreator: true, LastUpdate: day,
	}
}

// TestManagedTagsAreWhatToPropsWrites holds ManagedTags equal to the tags a
// full task produces. A field added to ToProps without a place in the set keeps
// a stale value on every edit that clears it; a tag in the set that ToProps
// never writes deletes what another client stored.
func TestManagedTagsAreWhatToPropsWrites(t *testing.T) {
	r := newFakeResolver()
	props, err := ToProps(fullTask(), r.resolve)
	mustNoErr(t, err, "ToProps")
	written := map[mapi.PropTag]bool{}
	for _, pv := range props {
		written[pv.Tag] = true
	}
	managed, err := ManagedTags(r.resolve)
	mustNoErr(t, err, "ManagedTags")
	for tag := range written {
		if !slices.Contains(managed, tag) {
			t.Errorf("ToProps writes %#x, which ManagedTags leaves out", uint32(tag))
		}
	}
	for _, tag := range managed {
		if !written[tag] {
			t.Errorf("ManagedTags holds %#x, which a full task does not write", uint32(tag))
		}
	}
}

// TestManagedTagsAllocateNothing answers from the names the store already has,
// so asking for the set on a store that never held a task creates no entry.
func TestManagedTagsAllocateNothing(t *testing.T) {
	r := newFakeResolver()
	managed, err := ManagedTags(r.resolve)
	mustNoErr(t, err, "ManagedTags")
	if len(r.ids) != 0 {
		t.Errorf("ManagedTags allocated %d named properties, want 0", len(r.ids))
	}
	if len(managed) != 5 {
		t.Errorf("ManagedTags on an empty store = %d tags, want the 5 fixed ones", len(managed))
	}
}
