import { getCookie, setCookie, deleteCookie } from "./cookies"

// Centralized, timezone-aware date formatting for the webmail UI.
//
// The user picks a display timezone during onboarding (or in Settings); every
// presentation must render instants in THAT zone instead of the browser's. The
// chosen IANA zone is kept in a module singleton so the pure formatter functions
// (used across many pages) can read it without threading React context through
// every call site. AuthContext sets it from /auth/me on load and onboarding /
// settings update it on change. An empty value means "follow the device" —
// formatters then omit the timeZone option and fall back to the browser zone.

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

// withTz merges the chosen timezone into Intl options. Callers pass their format
// options and get them back with { timeZone } added when a zone is set, so
// inline toLocale* calls (calendar, tasks, compose, email-detail) localize to
// the user's zone with a one-line change.
export function withTz(opts: Intl.DateTimeFormatOptions = {}): Intl.DateTimeFormatOptions {
  return displayTimeZone ? { ...opts, timeZone: displayTimeZone } : opts
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
// interpreting the wall clock in the user's chosen display timezone (falling
// back to the browser zone when none is set). The compose scheduler uses it so
// "14:30" means 14:30 in the zone the user actually sees times in. Two passes
// resolve the zone offset across DST boundaries.
export function zonedInputToISO(localValue: string): string {
  if (!localValue) return ''
  if (!displayTimeZone) {
    const d = new Date(localValue)
    return isNaN(d.getTime()) ? '' : d.toISOString()
  }
  const target = new Date(localValue + ':00Z') // wall clock treated as UTC
  if (isNaN(target.getTime())) return ''
  let utcMs = target.getTime() - zoneOffsetMs(displayTimeZone, target)
  utcMs = target.getTime() - zoneOffsetMs(displayTimeZone, new Date(utcMs))
  return new Date(utcMs).toISOString()
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
  }))
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
  }))
}
