import type { AppearanceSettings, CalendarSettings } from "@/utils/api"
import { defaultMailColumns } from "@/utils/mailListColumns"
import { DEFAULT_PAGE_SIZE } from "@/utils/inboxNavigation"

// AppearanceRecord is the whole appearance record the settings page edits and
// saves. The server's read-only preview cap is not part of it.
export type AppearanceRecord = Required<Omit<AppearanceSettings, "previewMaxBytes">>

// defaultAppearance is the record the page shows until the stored one is read.
export function defaultAppearance(): AppearanceRecord {
  return {
    theme: "system",
    language: "system",
    dateFormat: "iso",
    timeFormat: "24",
    nameDisplay: "firstlast",
    showUnreadCounter: false,
    unreadBorder: false,
    hideWidgetPanel: false,
    startupFolder: "",
    autoCc: "",
    shortcutMode: "extended",
    iconSet: "breeze",
    showItemData: false,
    filePreview: true,
    pdfZoom: "page-width",
    inboxNavMode: "pagination",
    inboxPageSize: DEFAULT_PAGE_SIZE,
    mailListColumns: defaultMailColumns(),
  }
}

// appearanceFromServer fills every field the server left out with its default,
// so a save always writes a whole record.
export function appearanceFromServer(a: Partial<AppearanceSettings>): AppearanceRecord {
  const base = defaultAppearance()
  const merged = { ...base }
  for (const key of Object.keys(base) as (keyof AppearanceRecord)[]) {
    const value = a[key]
    if (value !== undefined && value !== null) Object.assign(merged, { [key]: value })
  }
  return merged
}

// DEFAULT_CALENDAR is the calendar record the page shows until the stored one is
// read: Monday first, 30-minute slots, 09:00 to 18:00 on weekdays.
export const DEFAULT_CALENDAR: CalendarSettings = {
  firstDayOfWeek: 1,
  resolution: 30,
  workDayStart: 9,
  workDayEnd: 18,
  showNonWorkingHours: true,
  workDays: [1, 2, 3, 4, 5],
  defaultDuration: 30,
  defaultReminder: 15,
}

// calendarFromServer fills every field the server left out with its default.
export function calendarFromServer(c: Partial<CalendarSettings>): CalendarSettings {
  const merged = { ...DEFAULT_CALENDAR }
  for (const key of Object.keys(DEFAULT_CALENDAR) as (keyof CalendarSettings)[]) {
    const value = c[key]
    if (value !== undefined && value !== null) Object.assign(merged, { [key]: value })
  }
  return merged
}

// BROWSERS lists the user-agent markers in match order: Edge and Opera carry
// "Chrome" in their user agent, so they are matched before Chrome, and Chrome
// before Safari, which every Chromium user agent also lists.
const BROWSERS: { name: string; marker: RegExp; version: RegExp }[] = [
  { name: "Edge", marker: /Edg\//, version: /Edg\/(\d+)/ },
  { name: "Opera", marker: /OPR\//, version: /OPR\/(\d+)/ },
  { name: "Firefox", marker: /Firefox\//, version: /Firefox\/(\d+)/ },
  { name: "Chrome", marker: /Chrome\//, version: /Chrome\/(\d+)/ },
  { name: "Safari", marker: /Version\/.*Safari/, version: /Version\/(\d+)/ },
]

// detectBrowser derives a "Name Version" string from a user agent for the About
// section.
export function detectBrowser(ua: string): string {
  const hit = BROWSERS.find((b) => b.marker.test(ua))
  if (!hit) return "Unknown"
  const ver = ua.match(hit.version)?.[1]
  return ver ? `${hit.name} ${ver}` : hit.name
}

// rfc3339ToDate extracts the YYYY-MM-DD part from an RFC3339 string for <input type="date">.
export function rfc3339ToDate(value?: string): string {
  return value ? value.slice(0, 10) : ""
}

// dateToRFC3339 turns a YYYY-MM-DD value into an RFC3339 UTC timestamp, or undefined when empty.
export function dateToRFC3339(value: string): string | undefined {
  return value ? `${value}T00:00:00Z` : undefined
}

// formatStorageBytes renders a byte count with a binary-prefix unit for the
// storage gauge, falling back to a raw byte count below 1 KiB.
export function formatStorageBytes(n: number): string {
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GB`
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${n} B`
}

// usagePercent is the share of the quota in use, capped at 100.
export function usagePercent(used: number, limit: number): number {
  return Math.min(100, Math.round((used / limit) * 100))
}
