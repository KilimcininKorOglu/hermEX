import { describe, expect, it } from "vitest"
import { emptyEventForm, eventFormError, eventFormOf, eventPayload, movedEventPayload, parseAttendees, pickerWindow, recurrenceToForm, splitRooms, withoutRoom, withRoom } from "./eventForm"

const localISO = (h: number, m: number) => new Date(2026, 8, 25, h, m).toISOString()

describe("movedEventPayload", () => {
  it("moves the event to the new window and keeps its zone and other fields", () => {
    const ev = {
      uid: "7", summary: "Standup", start: localISO(9, 0), end: localISO(9, 30), recurrence: "FREQ=DAILY",
      timezone: "Europe/Istanbul", attendees: ["a@hermex.test"], categories: ["Red"], reminderMinutes: 10,
      organizer: "alice@hermex.test", tracking: [{ email: "a@hermex.test", response: 3 }],
    }
    const start = new Date(2026, 8, 25, 10, 0)
    const end = new Date(2026, 8, 25, 10, 30)
    expect(movedEventPayload(ev, start, end)).toEqual({
      summary: "Standup", start: start.toISOString(), end: end.toISOString(), allDay: undefined, calendarId: "calendar",
      location: undefined, description: undefined, attendees: ["a@hermex.test"], optionalAttendees: undefined,
      recurrence: "FREQ=DAILY", reminderMinutes: 10, busyStatus: undefined, sensitivity: undefined, categories: ["Red"],
      timezone: "Europe/Istanbul",
    })
  })
})

describe("eventFormOf", () => {
  it("fills a timed event's inputs in local time and keeps every field", () => {
    const form = eventFormOf({
      uid: "1", summary: "Review", start: localISO(10, 30), end: localISO(11, 0), location: "Room 1", description: "Notes",
      attendees: ["a@hermex.test", "b@hermex.test"], optionalAttendees: ["c@hermex.test"], recurrence: "FREQ=WEEKLY;BYDAY=MO",
      calendarId: "work", reminderMinutes: 15, busyStatus: 0, sensitivity: 2, categories: ["Red"],
    })
    expect(form).toEqual({
      summary: "Review", start: "2026-09-25T10:30", end: "2026-09-25T11:00", allDay: false, location: "Room 1", description: "Notes",
      attendees: "a@hermex.test, b@hermex.test", optionalAttendees: "c@hermex.test", recurrence: "WEEKLY", calendarId: "work",
      reminder: "15", busyStatus: "0", sensitivity: "2", categories: ["Red"], sendInvite: false,
    })
  })

  it("reads an all-day event as dates and absent or neutral values as empty", () => {
    const form = eventFormOf({ uid: "2", summary: "Holiday", start: "2026-09-25", allDay: true, reminderMinutes: 0, sensitivity: 0 })
    expect(form).toEqual({ ...emptyEventForm(), summary: "Holiday", start: "2026-09-25", allDay: true })
  })
})

describe("eventFormError", () => {
  it("requires a title, then a start", () => {
    expect(eventFormError(emptyEventForm())).toBe("calendar.titleRequired")
    expect(eventFormError({ ...emptyEventForm(), summary: "  " })).toBe("calendar.titleRequired")
    expect(eventFormError({ ...emptyEventForm(), summary: "A" })).toBe("calendar.startRequired")
    expect(eventFormError({ ...emptyEventForm(), summary: "A", start: "2026-09-25T10:00" })).toBeNull()
  })
})

describe("eventPayload", () => {
  it("sends a timed event as instants anchored to the zone", () => {
    const payload = eventPayload({
      ...emptyEventForm(), summary: " Review ", start: "2026-09-25T10:30", end: "2026-09-25T11:00", attendees: "a@x; b@x",
      optionalAttendees: "c@x", recurrence: "DAILY", reminder: "30", busyStatus: "1", sensitivity: "3", categories: ["Red"], sendInvite: true,
    }, "Europe/Istanbul")
    expect(payload).toEqual({
      summary: "Review", start: localISO(10, 30), end: localISO(11, 0), allDay: undefined, location: undefined, description: undefined,
      attendees: ["a@x", "b@x"], optionalAttendees: ["c@x"], recurrence: "FREQ=DAILY", calendarId: "calendar", reminderMinutes: 30,
      busyStatus: 1, sensitivity: 3, categories: ["Red"], sendInvite: true, timezone: "Europe/Istanbul",
    })
  })

  it("sends an all-day event as floating dates without a zone and drops empty values", () => {
    const payload = eventPayload({ ...emptyEventForm(), summary: "Holiday", start: "2026-09-25", end: "2026-09-26", allDay: true, calendarId: "" }, "UTC")
    expect(payload).toEqual({
      summary: "Holiday", start: "2026-09-25", end: "2026-09-26", allDay: true, location: undefined, description: undefined,
      attendees: undefined, optionalAttendees: undefined, recurrence: undefined, calendarId: "calendar", reminderMinutes: undefined,
      busyStatus: undefined, sensitivity: undefined, categories: undefined, sendInvite: undefined, timezone: undefined,
    })
  })

  it("round-trips an edited event without losing a field", () => {
    const ev = { uid: "3", summary: "Sync", start: localISO(9, 0), end: localISO(9, 30), attendees: ["a@x"], optionalAttendees: ["b@x"], recurrence: "FREQ=MONTHLY", calendarId: "work", reminderMinutes: 10, busyStatus: 2, sensitivity: 2, categories: ["Blue"] }
    const { uid: _uid, ...stored } = ev
    expect(eventPayload(eventFormOf(ev), "UTC")).toEqual({ ...stored, allDay: undefined, location: undefined, description: undefined, sendInvite: undefined, timezone: "UTC" })
  })
})

describe("rooms", () => {
  const room = { email: "Room1@hermex.test", name: "Room 1", capacity: 8 }
  const other = { email: "room2@hermex.test", name: "Room 2" }

  it("splits people from booked rooms case-insensitively", () => {
    expect(splitRooms("a@x, room1@hermex.test", [room, other])).toEqual({ people: ["a@x"], selectedRooms: [room] })
  })

  it("books a room once and keeps an existing location", () => {
    const booked = withRoom({ ...emptyEventForm(), attendees: "a@x" }, room)
    expect(booked.attendees).toBe("a@x, Room1@hermex.test")
    expect(booked.location).toBe("Room 1")
    expect(withRoom(booked, room).attendees).toBe("a@x, Room1@hermex.test")
    expect(withRoom({ ...emptyEventForm(), location: "HQ" }, room).location).toBe("HQ")
  })

  it("removes a room whatever the case of its address", () => {
    expect(withoutRoom({ ...emptyEventForm(), attendees: "a@x, room1@HERMEX.test" }, room).attendees).toBe("a@x")
  })
})

describe("pickerWindow", () => {
  it("spans the event, defaults to an hour, and is absent for an all-day or unstarted event", () => {
    expect(pickerWindow({ ...emptyEventForm(), start: "2026-09-25T10:30", end: "2026-09-25T12:00" })).toEqual({ start: localISO(10, 30), end: localISO(12, 0) })
    expect(pickerWindow({ ...emptyEventForm(), start: "2026-09-25T10:30" })).toEqual({ start: localISO(10, 30), end: localISO(11, 30) })
    expect(pickerWindow({ ...emptyEventForm(), start: "2026-09-25", allDay: true })).toBeUndefined()
    expect(pickerWindow(emptyEventForm())).toBeUndefined()
  })
})

describe("parseAttendees and recurrenceToForm", () => {
  it("split addresses and read the frequency", () => {
    expect(parseAttendees(" a@x,, b@x ;c@x ")).toEqual(["a@x", "b@x", "c@x"])
    expect(recurrenceToForm("FREQ=YEARLY;INTERVAL=2")).toBe("YEARLY")
    expect(recurrenceToForm(undefined)).toBe("")
  })
})
