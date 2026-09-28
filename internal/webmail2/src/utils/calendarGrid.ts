import { addDaysToKey, eventDayKey, zonedDayKey, zonedDayStartISO, zonedInputToISO } from "@/utils/date"

// The calendar grid lays out days of the display zone, the zone the page shows
// every time in. A grid day is a Date at UTC midnight whose UTC fields name that
// day, so day arithmetic and labels never go through the browser's zone: read
// it with the getUTC* methods and label it with dayLabel.

type DayView = "day" | "week" | "workweek" | "month"

const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS

// gridDay is the grid day of a "YYYY-MM-DD" key.
export function gridDay(key: string): Date {
  return new Date(`${key}T00:00:00Z`)
}

// dayKeyOf is the "YYYY-MM-DD" key of a grid day.
export function dayKeyOf(day: Date): string {
  return day.toISOString().slice(0, 10)
}

// todayGridDay is the grid day it is now in the display zone.
export function todayGridDay(now: Date = new Date()): Date {
  return gridDay(zonedDayKey(now))
}

// utcDay builds a grid day from a year, a 0-based month and a day that may
// overflow into the next or previous month.
function utcDay(year: number, month: number, day: number): Date {
  return new Date(Date.UTC(year, month, day))
}

// dayLabel renders a grid day with Intl options. A grid day is not an instant
// in the display zone, so it is rendered in UTC, where its fields are the day.
export function dayLabel(day: Date, opts: Intl.DateTimeFormatOptions): string {
  return day.toLocaleDateString(undefined, { ...opts, timeZone: "UTC" })
}

// monthMatrix returns the 42 days (6 weeks) that fill the grid for the month
// containing cursor, including trailing days from adjacent months. The week
// starts on firstDayOfWeek (0=Sun..6=Sat).
export function monthMatrix(cursor: Date, firstDayOfWeek: number): Date[] {
  const year = cursor.getUTCFullYear()
  const month = cursor.getUTCMonth()
  const offset = (utcDay(year, month, 1).getUTCDay() - firstDayOfWeek + 7) % 7
  return Array.from({ length: 42 }, (_, i) => utcDay(year, month, 1 - offset + i))
}

// weekDays returns count consecutive days from the firstDayOfWeek of the week
// containing cursor.
export function weekDays(cursor: Date, count: number, firstDayOfWeek: number): Date[] {
  const offset = (cursor.getUTCDay() - firstDayOfWeek + 7) % 7
  const y = cursor.getUTCFullYear()
  const m = cursor.getUTCMonth()
  const d = cursor.getUTCDate() - offset
  return Array.from({ length: count }, (_, i) => utcDay(y, m, d + i))
}

// moveCursor advances the cursor by one step for the view: a day, a week, or
// to the first of the next or previous month.
export function moveCursor(cursor: Date, view: DayView, sign: number): Date {
  const y = cursor.getUTCFullYear()
  const m = cursor.getUTCMonth()
  if (view === "month") return utcDay(y, m + sign, 1)
  return utcDay(y, m, cursor.getUTCDate() + sign * (view === "day" ? 1 : 7))
}

// dayBounds is the span of a grid day in the display zone as epoch milliseconds.
// A day that crosses a DST switch is 23 or 25 hours long.
function dayBounds(day: Date): { start: number; end: number } {
  const key = dayKeyOf(day)
  return { start: Date.parse(zonedDayStartISO(key)), end: Date.parse(zonedDayStartISO(addDaysToKey(key, 1))) }
}

// eventTopMinutes returns the minutes from the start of day at which the
// event's box starts, clamped to >= 0 (an event that began the day before
// starts at the top of the column).
export function eventTopMinutes(ev: { start: string }, day: Date): number {
  return Math.max(0, Math.round((Date.parse(ev.start) - dayBounds(day).start) / MINUTE_MS))
}

// eventHeightMinutes returns the event's height in minutes within day, never
// past the day's end and never shorter than 15 minutes. The end defaults to an
// hour after the start when absent.
export function eventHeightMinutes(ev: { start: string; end?: string }, day: Date): number {
  const start = Date.parse(ev.start)
  const end = ev.end ? Date.parse(ev.end) : start + HOUR_MS
  const bounds = dayBounds(day)
  return Math.max(15, Math.round((Math.min(end, bounds.end) - Math.max(start, bounds.start)) / MINUTE_MS))
}

// eventsOnDay returns the events that fall on day, timed or all-day.
export function eventsOnDay<T extends { start: string; allDay?: boolean }>(events: T[], day: Date, allDay: boolean): T[] {
  const key = dayKeyOf(day)
  return events.filter((ev) => !!ev.allDay === allDay && eventDayKey(ev) === key)
}

// wallInput is the datetime-local value for minutes after the start of day, a
// wall clock in the display zone. 24:00 is the start of the next day.
export function wallInput(day: Date, minutes: number): string {
  const key = addDaysToKey(dayKeyOf(day), Math.floor(minutes / 1440))
  const rest = minutes % 1440
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${key}T${pad(Math.floor(rest / 60))}:${pad(rest % 60)}`
}

// wallTime is the instant of wallInput(day, minutes) in the display zone.
export function wallTime(day: Date, minutes: number): Date {
  return new Date(zonedInputToISO(wallInput(day, minutes)))
}
