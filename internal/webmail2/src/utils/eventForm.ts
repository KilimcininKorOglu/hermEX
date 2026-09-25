import type { CalendarEvent, Room } from "@/utils/api"

// EventForm is the calendar event editor's state. Every field is the text an
// input holds, so an absent value is "" rather than undefined.
export interface EventForm {
  summary: string
  start: string
  end: string
  allDay: boolean
  location: string
  description: string
  attendees: string
  optionalAttendees: string // comma/space-separated optional attendee addresses (OPT-PARTICIPANT)
  recurrence: string // "" | DAILY | WEEKLY | MONTHLY | YEARLY
  calendarId: string // target calendar ("calendar" = default)
  reminder: string // "" = none, else a minute offset ("15", "30", "60", ...)
  busyStatus: string // "" = default (busy), else "0".."4" (free/tentative/busy/oof/working elsewhere)
  sensitivity: string // "" = normal, else "2" (private) or "3" (confidential)
  categories: string[] // selected category names
  sendInvite: boolean // email a METHOD:REQUEST invite to attendees on save
}

// EventPayload is the body the create and update endpoints accept.
export type EventPayload = Omit<CalendarEvent, "uid"> & { sendInvite?: boolean }

export function emptyEventForm(): EventForm {
  return { summary: "", start: "", end: "", allDay: false, location: "", description: "", attendees: "", optionalAttendees: "", recurrence: "", calendarId: "calendar", reminder: "", busyStatus: "", sensitivity: "", categories: [], sendInvite: false }
}

// parseAttendees splits a comma/space/semicolon-separated address string into
// a clean list of addresses.
export function parseAttendees(s: string): string[] {
  return s
    .split(/[\s,;]+/)
    .map((a) => a.trim())
    .filter(Boolean)
}

// rfc3339ToLocalInput converts an RFC3339 instant to the value a
// datetime-local input expects ("YYYY-MM-DDTHH:mm" in local time).
export function rfc3339ToLocalInput(value: string): string {
  const d = new Date(value)
  if (isNaN(d.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// localInputToRFC3339 converts a datetime-local value to an RFC3339 instant.
function localInputToRFC3339(value: string): string {
  const d = new Date(value)
  return isNaN(d.getTime()) ? "" : d.toISOString()
}

// recurrenceToForm maps a stored RRULE value to the form's frequency selector.
export function recurrenceToForm(rrule?: string): string {
  if (!rrule) return ""
  const m = /FREQ=([A-Z]+)/.exec(rrule)
  return m ? m[1] : ""
}

// inputValue renders a stored start or end for the date input (all-day) or the
// datetime-local input (timed).
function inputValue(allDay: boolean, value: string): string {
  return allDay ? value.slice(0, 10) : rfc3339ToLocalInput(value)
}

// numberText renders n for a select, or "" when it is absent or keep rejects it.
function numberText(n: number | undefined, keep: (n: number) => boolean): string {
  return n != null && keep(n) ? String(n) : ""
}

// eventFormOf fills the editor from a stored event.
export function eventFormOf(ev: CalendarEvent): EventForm {
  const allDay = !!ev.allDay
  return {
    summary: ev.summary,
    start: inputValue(allDay, ev.start),
    end: ev.end ? inputValue(allDay, ev.end) : "",
    allDay,
    location: ev.location ?? "",
    description: ev.description ?? "",
    attendees: (ev.attendees ?? []).join(", "),
    optionalAttendees: (ev.optionalAttendees ?? []).join(", "),
    recurrence: recurrenceToForm(ev.recurrence),
    calendarId: ev.calendarId ?? "calendar",
    reminder: numberText(ev.reminderMinutes, (n) => n !== 0),
    busyStatus: numberText(ev.busyStatus, () => true),
    sensitivity: numberText(ev.sensitivity, (n) => n > 0),
    categories: ev.categories ?? [],
    sendInvite: false,
  }
}

// eventFormError returns the i18n key of the first reason the form cannot be
// saved, or null when it can.
export function eventFormError(form: EventForm): string | null {
  if (!form.summary.trim()) return "calendar.titleRequired"
  if (!form.start) return "calendar.startRequired"
  return null
}

// splitRooms separates the attendee string into people and booked rooms, so
// the editor shows rooms as their own chips. The saved value keeps both.
export function splitRooms(attendees: string, rooms: Room[]): { people: string[]; selectedRooms: Room[] } {
  const list = parseAttendees(attendees)
  const roomEmails = new Set(rooms.map((r) => r.email.toLowerCase()))
  return {
    people: list.filter((e) => !roomEmails.has(e.toLowerCase())),
    selectedRooms: rooms.filter((r) => list.some((e) => e.toLowerCase() === r.email.toLowerCase())),
  }
}

// withRoom books room: it adds the room's address to the attendees once and
// uses the room's name as the location when none is set.
export function withRoom(form: EventForm, room: Room): EventForm {
  const list = parseAttendees(form.attendees)
  if (!list.includes(room.email)) list.push(room.email)
  return { ...form, attendees: list.join(", "), location: form.location || room.name }
}

// withoutRoom removes room's address from the attendees.
export function withoutRoom(form: EventForm, room: Room): EventForm {
  const attendees = parseAttendees(form.attendees).filter((e) => e.toLowerCase() !== room.email.toLowerCase())
  return { ...form, attendees: attendees.join(", ") }
}

// pickerWindow is the time window the attendee picker shows free/busy for: the
// event's own span, or an hour from the start when no end is set. An all-day
// or unstarted event has none.
export function pickerWindow(form: EventForm): { start: string; end: string } | undefined {
  if (!form.start || form.allDay) return undefined
  const end = form.end ? localInputToRFC3339(form.end) : new Date(new Date(form.start).getTime() + 60 * 60 * 1000).toISOString()
  return { start: localInputToRFC3339(form.start), end }
}

function instant(allDay: boolean, value: string): string {
  return allDay ? value : localInputToRFC3339(value)
}

function listOrUndefined(list: string[]): string[] | undefined {
  return list.length > 0 ? list : undefined
}

function numberOrUndefined(s: string): number | undefined {
  return s ? Number(s) : undefined
}

// eventPayload builds the create/update body from the form. timezone anchors a
// timed event to the user's zone so a recurrence keeps its wall time across
// DST; an all-day event stays a floating date.
export function eventPayload(form: EventForm, timezone: string): EventPayload {
  return {
    summary: form.summary.trim(),
    start: instant(form.allDay, form.start),
    end: form.end ? instant(form.allDay, form.end) : undefined,
    allDay: form.allDay || undefined,
    location: form.location || undefined,
    description: form.description || undefined,
    attendees: listOrUndefined(parseAttendees(form.attendees)),
    optionalAttendees: listOrUndefined(parseAttendees(form.optionalAttendees)),
    recurrence: form.recurrence ? `FREQ=${form.recurrence}` : undefined,
    calendarId: form.calendarId || "calendar",
    reminderMinutes: numberOrUndefined(form.reminder),
    busyStatus: numberOrUndefined(form.busyStatus),
    sensitivity: numberOrUndefined(form.sensitivity),
    categories: listOrUndefined(form.categories),
    sendInvite: form.sendInvite || undefined,
    timezone: form.allDay ? undefined : timezone,
  }
}
