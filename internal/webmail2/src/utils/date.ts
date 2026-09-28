import { getCookie, setCookie, deleteCookie } from "./cookies"

// Centralized, timezone-aware date formatting for the webmail UI.
//
// The user picks a display timezone during onboarding (or in Settings); every
// presentation must render instants in THAT zone instead of the browser's. The
// chosen IANA zone is kept in a module singleton so the pure formatter functions
// (used across many pages) can read it without threading React context through
// every call site. AuthContext sets it from /auth/me on load and onboarding /
// settings update it on change. An empty value means no zone was chosen: times
// then render in UTC, and a full date carries a "UTC" label so it cannot be
// read as local time.

const TZ_COOKIE_KEY = 'hermex-timezone'

// Seed synchronously from the cookie so the very first render already uses the
// chosen zone (no flash of browser-zone times before /auth/me resolves).
let displayTimeZone = getCookie(TZ_COOKIE_KEY) || ''

export function setDisplayTimeZone(tz: string): void {
  displayTimeZone = tz || ''
  if (displayTimeZone) {
    setCookie(TZ_COOKIE_KEY, displayTimeZone)
  } else {
    deleteCookie(TZ_COOKIE_KEY)
  }
}

export function getDisplayTimeZone(): string {
  return displayTimeZone
}

// withTz merges the chosen timezone, or UTC when none was chosen, into Intl
// options, so inline toLocale* calls (calendar, tasks, compose, email-detail)
// render in the user's zone with a one-line change.
export function withTz(opts: Intl.DateTimeFormatOptions = {}): Intl.DateTimeFormatOptions {
  return { ...opts, timeZone: displayTimeZone || 'UTC' }
}

// zoneLabel is appended to a full date: " UTC" when no zone was chosen, so a UTC
// time is never read as local time, and nothing once the user picked a zone.
function zoneLabel(): string {
  return displayTimeZone ? '' : ' UTC'
}

// zoneOffsetMs returns timeZone's UTC offset in milliseconds at instant `at`,
// using Intl (no external dependency). Positive east of UTC.
function zoneOffsetMs(timeZone: string, at: Date): number {
  const dtf = new Intl.DateTimeFormat('en-US', {
    timeZone, hour12: false,
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
  const map: Record<string, number> = {}
  for (const p of dtf.formatToParts(at)) {
    if (p.type !== 'literal') map[p.type] = Number(p.value)
  }
  const asUTC = Date.UTC(map.year, map.month - 1, map.day, map.hour === 24 ? 0 : map.hour, map.minute, map.second)
  return asUTC - at.getTime()
}

// zonedInputToISO converts a <input type="datetime-local"> value (a zoneless
// wall clock "YYYY-MM-DDTHH:mm") into an absolute RFC3339/ISO instant,
// interpreting the wall clock in the user's chosen display timezone, or UTC when
// none is set. The compose scheduler uses it so "14:30" means 14:30 in the zone
// the user actually sees times in. Two passes resolve the zone offset across DST
// boundaries.
export function zonedInputToISO(localValue: string): string {
  if (!localValue) return ''
  const target = new Date(localValue + ':00Z') // wall clock treated as UTC
  if (isNaN(target.getTime())) return ''
  if (!displayTimeZone) return target.toISOString()
  let utcMs = target.getTime() - zoneOffsetMs(displayTimeZone, target)
  utcMs = target.getTime() - zoneOffsetMs(displayTimeZone, new Date(utcMs))
  return new Date(utcMs).toISOString()
}

// zonedInputFromISO is zonedInputToISO the other way round: the "YYYY-MM-DDTHH:mm"
// wall clock an instant shows in the display zone, for a datetime-local input,
// so the input reads the same time the page shows. An unreadable value is "".
export function zonedInputFromISO(value?: string | Date): string {
  if (!value) return ''
  const date = value instanceof Date ? value : new Date(value)
  if (isNaN(date.getTime())) return ''
  const p = zonedParts(date)
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}`
}

// zonedDayKey is the "YYYY-MM-DD" day an instant falls on in the display zone.
export function zonedDayKey(value: string | Date): string {
  return zonedInputFromISO(value).slice(0, 10)
}

// zonedDayStartISO is the instant a "YYYY-MM-DD" day starts in the display zone.
export function zonedDayStartISO(day: string): string {
  return zonedInputToISO(`${day}T00:00`)
}

// addDaysToKey moves a "YYYY-MM-DD" day by n calendar days. A day is a date, not
// an instant, so no zone takes part.
export function addDaysToKey(day: string, n: number): string {
  const [y, m, d] = day.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10)
}

// weekdayOfKey is a "YYYY-MM-DD" day's weekday, 0 for Sunday.
export function weekdayOfKey(day: string): number {
  const [y, m, d] = day.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay()
}

// eventDayKey is the "YYYY-MM-DD" day an event falls on in the display zone. An
// all-day event's date-only start names a day, not an instant, so it is kept.
export function eventDayKey(ev: { start: string; allDay?: boolean }): string {
  if (ev.allDay && ev.start.length === 10) return ev.start
  return zonedDayKey(ev.start)
}

type Translate =(key: string, params?: Record<string, string>) => string

// uiLang is the interface language the page renders in, which useI18n mirrors
// onto <html lang>.
function uiLang(): string {
  return document.documentElement.lang === 'tr' ? 'tr' : 'en'
}

// zonedParts splits an instant into its calendar fields in the display zone.
function zonedParts(date: Date): Record<string, string> {
  const parts = new Intl.DateTimeFormat('en-US', withTz({
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  })).formatToParts(date)
  return Object.fromEntries(parts.map((p) => [p.type, p.value]))
}

// dayText writes a calendar day in the interface language: 28.03.2026 in
// Turkish, 03/28/2026 in English.
function dayText(year: string, month: string, day: string): string {
  return uiLang() === 'tr' ? `${day}.${month}.${year}` : `${month}/${day}/${year}`
}

// clockText writes a wall-clock time in the interface language: 15:33 in
// Turkish, 3:33 PM in English.
function clockText(hour: string, minute: string): string {
  if (uiLang() === 'tr') return `${hour}:${minute}`
  const h = Number(hour)
  return `${h % 12 || 12}:${minute} ${h < 12 ? 'AM' : 'PM'}`
}

// relativeSteps are the units a distance under a day is written in, largest first.
const relativeSteps: Array<[number, string, string]> = [
  [3600000, 'time.hoursAgo', 'time.inHours'],
  [60000, 'time.minutesAgo', 'time.inMinutes'],
  [1000, 'time.secondsAgo', 'time.inSeconds'],
]

// relativeText writes a distance under a day ("5 min ago", "in 4 h"), or "" for
// a day or more, where the date says more.
function relativeText(diffMs: number, t: Translate): string {
  const past = diffMs <= 0
  const abs = Math.abs(diffMs)
  if (abs >= 86400000) return ''
  for (const [size, ago, ahead] of relativeSteps) {
    if (abs >= size) return t(past ? ago : ahead, { n: String(Math.floor(abs / size)) })
  }
  return t(past ? 'time.justNow' : 'time.shortly')
}

// formatWhen writes an instant for every place outside the message lists: under
// a day away as a distance ("10 h ago"), otherwise as the date and time in the
// display zone and the interface language (28.03.2026 15:33), labelled UTC when
// no zone was chosen. An unreadable value is returned as it came.
export function formatWhen(value: string | number | Date, t: Translate, now: number = Date.now()): string {
  const date = value instanceof Date ? value : new Date(value)
  if (isNaN(date.getTime())) return String(value)
  const rel = relativeText(date.getTime() - now, t)
  if (rel) return rel
  const p = zonedParts(date)
  return `${dayText(p.year, p.month, p.day)} ${clockText(p.hour, p.minute)}${zoneLabel()}`
}

// formatDay writes a calendar day in the interface language. A date-only value
// ("2026-03-28", a task's due day) names a day, not an instant, so it is written
// as it is and never moved by a zone; any other value is read in the display zone.
export function formatDay(value: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (m) return dayText(m[1], m[2], m[3])
  const date = new Date(value)
  if (isNaN(date.getTime())) return value
  const p = zonedParts(date)
  return dayText(p.year, p.month, p.day)
}

// formatDate is the compact relative format used in message lists: time within a
// day, weekday within a week, otherwise month + day.
export function formatDate(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diff = now.getTime() - date.getTime()

  if (diff < 86400000) {
    // Less than 24 hours
    return date.toLocaleTimeString([], withTz({ hour: '2-digit', minute: '2-digit' }))
  } else if (diff < 604800000) {
    // Less than 7 days
    return date.toLocaleDateString([], withTz({ weekday: 'short' }))
  } else {
    return date.toLocaleDateString([], withTz({ month: 'short', day: 'numeric' }))
  }
}

// formatFullDate is the long date+time used in detail/header contexts.
export function formatFullDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleString([], withTz({
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })) + zoneLabel()
}

// formatAbsolute renders a full, unambiguous localized date+time for the message
// lists (which previously showed the raw server Date string in the server's
// zone). Invalid input is returned unchanged so a malformed header still shows
// something.
export function formatAbsolute(dateString: string): string {
  const date = new Date(dateString)
  if (isNaN(date.getTime())) return dateString
  return date.toLocaleString([], withTz({
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })) + zoneLabel()
}
