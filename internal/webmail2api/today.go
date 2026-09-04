package webmail2api

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxtask"
)

// todayJSON is the aggregate the Bugun/Dashboard panel renders: unread mail count,
// today's appointments, and the tasks due today (or overdue). The SPA's Today page
// lays these out as configurable widgets the way the reference today module does.
type todayJSON struct {
	Unread       int         `json:"unread"`
	UnreadRecent []todayMail `json:"unreadRecent"`
	Appointments []todayItem `json:"appointments"`
	Tasks        []todayTask `json:"tasks"`
	Notes        int         `json:"notes"`
	Contacts     int         `json:"contacts"`
}

type todayMail struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Date    string `json:"date"`
}

type todayItem struct {
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Start    string `json:"start"`
	End      string `json:"end,omitempty"`
	AllDay   bool   `json:"allDay,omitempty"`
	Calendar string `json:"calendar,omitempty"`
}

type todayTask struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Due     string `json:"due,omitempty"`
}

// handleGetToday aggregates the dashboard data in one round trip: unread inbox mail,
// today's calendar appointments, and tasks due today (or overdue and not complete),
// plus simple counts for the notes and contacts widgets.
func (s *Server) handleGetToday(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	out := todayJSON{}
	out.Unread, out.UnreadRecent = inboxUnread(st, 5)
	out.Appointments = appointmentsOn(st, dayStart, dayEnd)
	out.Tasks = tasksDueBy(st, dayEnd)
	if objs, err := st.ListFolderObjects(mapi.PrivateFIDNotes); err == nil {
		out.Notes = len(objs)
	}
	if objs, err := st.ListFolderObjects(mapi.PrivateFIDContacts); err == nil {
		out.Contacts = len(objs)
	}
	writeJSON(w, http.StatusOK, out)
}

// inboxUnread returns the unread count and the n most recent unread inbox subjects.
func inboxUnread(st *objectstore.Store, n int) (int, []todayMail) {
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		return 0, nil
	}
	count := 0
	var recent []todayMail
	for _, m := range msgs {
		if m.Flags&objectstore.FlagSeen != 0 {
			continue
		}
		count++
		if len(recent) < n {
			if raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), m.UID); err == nil {
				subject := headerField(raw, "Subject")
				from := headerField(raw, "From")
				date := headerField(raw, "Date")
				recent = append(recent, todayMail{
					ID:      strconv.FormatUint(uint64(m.UID), 10),
					Subject: subject,
					From:    from,
					Date:    date,
				})
			}
		}
	}
	return count, recent
}

// appointmentsOn returns the appointments whose start falls within [dayStart, dayEnd).
// It reads PidLidAppointmentStartWhole/EndWhole/SubType directly (no per-item iCal
// export), so the dashboard is cheap even for a large calendar.
func appointmentsOn(st *objectstore.Store, dayStart, dayEnd time.Time) []todayItem {
	ids, _ := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole, mapi.NameAppointmentSubType})
	var startTag, endTag, subTag mapi.PropTag
	if len(ids) == 3 {
		startTag = mapi.MakeTag(ids[0], mapi.PtSysTime)
		endTag = mapi.MakeTag(ids[1], mapi.PtSysTime)
		subTag = mapi.MakeTag(ids[2], mapi.PtBoolean)
	}
	// Let the store apply the day window. Its predicate is wider than the one
	// below (it keeps a span that merely overlaps, and an object with no start),
	// so the exact test still runs, but it runs over the day rather than over the
	// whole calendar.
	var objs []objectstore.FolderObject
	if startTag != 0 && endTag != 0 {
		objs, _ = st.ListFolderObjectsInWindow(mapi.PrivateFIDCalendar, startTag, endTag, dayStart, dayEnd)
	} else {
		objs, _ = st.ListFolderObjects(mapi.PrivateFIDCalendar)
	}
	out := make([]todayItem, 0, len(objs))
	for _, o := range objs {
		props, err := st.GetMessageProperties(o.ID, mapi.PrSubject, startTag, endTag, subTag)
		if err != nil {
			continue
		}
		start := propTime(props, startTag)
		if start.IsZero() {
			continue
		}
		if start.Before(dayStart) || !start.Before(dayEnd) {
			continue
		}
		item := todayItem{
			ID:      strconv.FormatInt(o.ID, 10),
			Subject: propString2(props, mapi.PrSubject),
			Start:   start.UTC().Format(time.RFC3339),
		}
		if end := propTime(props, endTag); !end.IsZero() {
			item.End = end.UTC().Format(time.RFC3339)
		}
		if v, ok := props.Get(subTag); ok {
			if b, ok := v.(bool); ok {
				item.AllDay = b
			}
		}
		out = append(out, item)
	}
	return out
}

// tasksDueBy returns the incomplete tasks due on or before the cutoff (today's end,
// so overdue tasks surface too), the to-do view the dashboard's TasksWidget shows.
func tasksDueBy(st *objectstore.Store, cutoff time.Time) []todayTask {
	objs, _ := st.ListFolderObjects(mapi.PrivateFIDTasks)
	out := make([]todayTask, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		t, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		if err != nil {
			continue
		}
		if t.Complete {
			continue
		}
		if t.Due.IsZero() || t.Due.After(cutoff) {
			continue
		}
		item := todayTask{ID: strconv.FormatInt(o.ID, 10), Subject: t.Subject}
		if !t.Due.IsZero() {
			item.Due = t.Due.UTC().Format("2006-01-02")
		}
		out = append(out, item)
	}
	return out
}

// propTime reads a PtSysTime from a property bag.
func propTime(p mapi.PropertyValues, tag mapi.PropTag) time.Time {
	if tag == 0 {
		return time.Time{}
	}
	v, ok := p.Get(tag)
	if !ok {
		return time.Time{}
	}
	nt, ok := v.(uint64)
	if !ok {
		return time.Time{}
	}
	return mapi.NTTimeToUnix(nt).UTC()
}

// propString2 reads a string property (string or []byte).
func propString2(p mapi.PropertyValues, tag mapi.PropTag) string {
	v, ok := p.Get(tag)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}

// headerField extracts a header value from a raw RFC822 message (best-effort, no full
// parse) for the dashboard's recent-mail widget.
func headerField(raw []byte, name string) string {
	prefix := []byte(name + ":")
	for _, line := range splitLines(raw) {
		if len(line) >= len(prefix) && equalFold(line[:len(prefix)], prefix) {
			return trimSpaces(string(line[len(prefix):]))
		}
		if len(line) == 0 {
			break
		}
	}
	return ""
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			line := b[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func equalFold(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func trimSpaces(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
