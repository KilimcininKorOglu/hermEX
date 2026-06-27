package oxcical

import (
	"strings"
	"time"
)

// ExpandRecurrence expands a recurring VEVENT into its individual instances within
// [rangeStart, rangeEnd), for the CalDAV <C:expand> report (RFC 4791 9.6.5): one
// VEVENT per occurrence with RECURRENCE-ID set, DTSTART/DTEND shifted to the instance,
// and the recurrence rules (RRULE/RDATE/EXDATE) stripped. EXDATE instances are
// skipped, and a RECURRENCE-ID override component present in the source replaces the
// generated instance for that occurrence. ok is false when the object carries no
// master RRULE, so the caller serves it unchanged.
func ExpandRecurrence(ical []byte, rangeStart, rangeEnd time.Time) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	var master *icomp
	overrides := map[string]*icomp{}
	for _, c := range cal.comps {
		if c.name != "VEVENT" {
			continue
		}
		if rid := c.prop("RECURRENCE-ID"); rid != nil {
			if t, _, ok := parseICalTime(rid); ok {
				overrides[instantKey(t)] = c
			}
			continue
		}
		if c.prop("RRULE") != nil {
			master = c
		}
	}
	if master == nil {
		return nil, false
	}
	start, allDay, sok := parseICalTime(master.prop("DTSTART"))
	if !sok {
		return nil, false
	}
	end, eok := eventEnd(master, start, allDay)
	if !eok {
		end = start
	}
	dur := end.Sub(start)
	rec, rok := parseRRule(master.prop("RRULE").value)
	if !rok {
		return nil, false
	}

	skip := map[string]bool{}
	for _, l := range master.propLines("EXDATE") {
		for v := range strings.SplitSeq(l.value, ",") {
			ex := iline{name: "EXDATE", params: l.params, value: strings.TrimSpace(v)}
			if t, _, ok := parseICalTime(&ex); ok {
				skip[instantKey(t)] = true
			}
		}
	}

	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	b.add("VERSION:2.0")
	b.add("PRODID:-//hermEX//CalDAV//EN")
	for _, t := range rec.Occurrences(start.UTC(), rangeStart, rangeEnd, 4096) {
		key := instantKey(t)
		if skip[key] {
			continue
		}
		if ov, ok := overrides[key]; ok {
			writeComponent(b, ov)
			continue
		}
		writeInstance(b, master, t, t.Add(dur), allDay)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// instantKey normalizes an instant to a UTC comparison key, so an EXDATE or a
// RECURRENCE-ID override value matches the generated occurrence at the same time.
func instantKey(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// writeInstance emits one expanded VEVENT: the master's UID and descriptive
// properties, with RECURRENCE-ID and the shifted DTSTART/DTEND, and no recurrence
// rules.
func writeInstance(b *builder, master *icomp, start, end time.Time, allDay bool) {
	b.add("BEGIN:VEVENT")
	b.line("UID", master.propText("UID"))
	b.add("DTSTAMP:" + formatICalUTC(start))
	b.add(dtLine("RECURRENCE-ID", start, allDay))
	b.add(dtLine("DTSTART", start, allDay))
	b.add(dtLine("DTEND", end, allDay))
	for _, name := range []string{"SUMMARY", "DESCRIPTION", "LOCATION", "STATUS", "TRANSP", "CLASS", "PRIORITY", "SEQUENCE", "ORGANIZER"} {
		if l := master.prop(name); l != nil {
			b.add(renderIline(*l))
		}
	}
	for _, l := range master.propLines("ATTENDEE") {
		b.add(renderIline(l))
	}
	b.add("END:VEVENT")
}

// writeComponent emits a parsed component verbatim (its content lines and nested
// components), used to pass a RECURRENCE-ID override through unchanged.
func writeComponent(b *builder, c *icomp) {
	b.add("BEGIN:" + c.name)
	for _, l := range c.props {
		b.add(renderIline(l))
	}
	for _, sub := range c.comps {
		writeComponent(b, sub)
	}
	b.add("END:" + c.name)
}

// renderIline serializes a parsed content line back to "NAME;PARAM=v:value", carrying
// its parameters and the (already-escaped) value.
func renderIline(l iline) string {
	var sb strings.Builder
	sb.WriteString(l.name)
	for k, vals := range l.params {
		for _, v := range vals {
			sb.WriteString(";")
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(v)
		}
	}
	sb.WriteString(":")
	sb.WriteString(l.value)
	return sb.String()
}
