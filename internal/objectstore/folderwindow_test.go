package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// seedAppointment stores one object in the Calendar folder carrying the two
// appointment time properties, or neither when start is zero.
func seedAppointment(t *testing.T, s *Store, startTag, endTag mapi.PropTag, start, end time.Time) int64 {
	t.Helper()
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Appointment")
	if !start.IsZero() {
		props.Set(startTag, mapi.UnixToNTTime(start))
		props.Set(endTag, mapi.UnixToNTTime(end))
	}
	id, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestListFolderObjectsInWindow pins the store-side calendar range predicate. The
// window has to be applied by the query, not by reading every object's properties
// back, and it has to stay WIDER than any caller's own filter so it can never
// change a result: an object with no start time is kept, and a span that merely
// overlaps the window is kept.
func TestListFolderObjectsInWindow(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole,
	})
	if err != nil {
		t.Fatal(err)
	}
	startTag := mapi.MakeTag(ids[0], mapi.PtSysTime)
	endTag := mapi.MakeTag(ids[1], mapi.PtSysTime)

	day := func(d int, h int) time.Time { return time.Date(2026, 6, d, h, 0, 0, 0, time.UTC) }
	inside := seedAppointment(t, st, startTag, endTag, day(10, 9), day(10, 10))
	straddlesStart := seedAppointment(t, st, startTag, endTag, day(9, 23), day(10, 1))
	straddlesEnd := seedAppointment(t, st, startTag, endTag, day(10, 23), day(11, 1))
	before := seedAppointment(t, st, startTag, endTag, day(1, 9), day(1, 10))
	after := seedAppointment(t, st, startTag, endTag, day(20, 9), day(20, 10))
	undated := seedAppointment(t, st, startTag, endTag, time.Time{}, time.Time{})

	got, err := st.ListFolderObjectsInWindow(int64(mapi.PrivateFIDCalendar), startTag, endTag, day(10, 0), day(11, 0))
	if err != nil {
		t.Fatal(err)
	}
	in := map[int64]bool{}
	for _, o := range got {
		in[o.ID] = true
		if o.ChangeNumber == 0 {
			t.Errorf("object %d came back with no change number", o.ID)
		}
	}
	for _, c := range []struct {
		name string
		id   int64
		want bool
	}{
		{"wholly inside", inside, true},
		{"straddles the start", straddlesStart, true},
		{"straddles the end", straddlesEnd, true},
		{"undated", undated, true},
		{"entirely before", before, false},
		{"entirely after", after, false},
	} {
		if in[c.id] != c.want {
			t.Errorf("%s: present = %v, want %v", c.name, in[c.id], c.want)
		}
	}
}

// TestListFolderObjectsInWindowSkipsDeleted keeps the window on the same footing as
// the unwindowed listing: a soft-deleted object is not a member of the folder.
func TestListFolderObjectsInWindowSkipsDeleted(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	startTag, endTag, ok := func() (mapi.PropTag, mapi.PropTag, bool) {
		ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
			mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole,
		})
		if err != nil {
			return 0, 0, false
		}
		return mapi.MakeTag(ids[0], mapi.PtSysTime), mapi.MakeTag(ids[1], mapi.PtSysTime), true
	}()
	if !ok {
		t.Fatal("could not allocate the appointment time tags")
	}

	start := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	id := seedAppointment(t, st, startTag, endTag, start, start.Add(time.Hour))
	if err := st.DeleteObject(id); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListFolderObjectsInWindow(int64(mapi.PrivateFIDCalendar), startTag, endTag,
		start.Add(-time.Hour), start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a deleted object is still a window member: %+v", got)
	}
}

// TestAppointmentTimeTagsNeverAllocates keeps a read off the write path: a mailbox
// that holds no appointment must report the tags as absent rather than minting
// named-property ids for them.
func TestAppointmentTimeTagsNeverAllocates(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, _, ok := st.AppointmentTimeTags(); ok {
		t.Fatal("a fresh store reported the appointment time tags as present")
	}
	if _, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := st.AppointmentTimeTags(); !ok {
		t.Fatal("the tags were allocated but still report as absent")
	}
}
