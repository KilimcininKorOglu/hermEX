import { useState, useEffect, useCallback, useRef, type CSSProperties, type Dispatch, type ReactNode, type SetStateAction } from "react"
import { CalendarDays, Plus, MapPin, Clock, Edit, Trash2, MoreHorizontal, Users, Repeat, Bell, Printer, ChevronLeft, ChevronRight, Settings2, Share2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { toast } from "sonner"
import { AttendeePicker } from "@/components/attendee-picker"
import { CategoryChips, toggledCategories, type CategoryOption } from "@/components/category-chips"
import { withTz, getDisplayTimeZone } from "@/utils/date"
import { detectTimeZone } from "@/utils/timezone"
import api, { type Calendar, type CalendarEvent, type UserFreeBusy, type Room, type CalendarSettings } from "@/utils/api"
import {
  emptyEventForm,
  eventFormError,
  eventFormOf,
  eventPayload,
  parseAttendees,
  pickerWindow,
  recurrenceToForm,
  rfc3339ToLocalInput,
  splitRooms,
  withoutRoom,
  withRoom,
  type EventForm,
  type EventPayload,
} from "@/utils/eventForm"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import { ShareFolderDialog } from "@/components/share-folder-dialog"
import { useBusyGate } from "@/hooks/useBusyGate"

type TFunc = (key: string, params?: Record<string, string>) => string

type CalendarView = "list" | "day" | "week" | "workweek" | "month"
type TimeGridView = "day" | "week" | "workweek"

const DEFAULT_COLOR = "#3b82f6"
const CALENDAR_PALETTE = ["#ef4444", "#f97316", "#eab308", "#22c55e", "#3b82f6", "#8b5cf6", "#ec4899", "#64748b"]
const REMINDER_MINUTES = [5, 10, 15, 30, 60, 120, 1440]
const RESOLUTION_MINUTES = [5, 10, 15, 30, 60]
const RECURRENCE_FREQUENCIES = ["DAILY", "WEEKLY", "MONTHLY", "YEARLY"]
// GRID_DAYS is the number of day columns each time-grid view shows.
const GRID_DAYS: Record<TimeGridView, number> = { day: 1, workweek: 5, week: 7 }
// RESPONSE_KEYS maps an attendee's PidLidResponseStatus to its label.
const RESPONSE_KEYS: Record<number, string> = { 2: "calendar.respTentative", 3: "calendar.respAccepted", 4: "calendar.respDeclined" }

function isTimeGrid(view: CalendarView): view is TimeGridView {
  return view === "day" || view === "week" || view === "workweek"
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}

// recurrenceLabel maps a frequency value to its localized label.
function recurrenceLabel(t: TFunc, freq: string): string {
  switch (freq) {
    case "DAILY": return t("calendar.recurrence.daily")
    case "WEEKLY": return t("calendar.recurrence.weekly")
    case "MONTHLY": return t("calendar.recurrence.monthly")
    case "YEARLY": return t("calendar.recurrence.yearly")
    case "": return t("calendar.recurrence.none")
    default: return t("calendar.recurrence.repeats")
  }
}

function dayKey(value: string): string {
  const d = new Date(value)
  return isNaN(d.getTime()) ? value : d.toLocaleDateString(undefined, withTz({ weekday: "long", year: "numeric", month: "long", day: "numeric" }))
}

function timeLabel(t: TFunc, ev: CalendarEvent): string {
  if (ev.allDay) return t("calendar.allDay")
  const start = new Date(ev.start)
  const opts = withTz({ hour: "2-digit", minute: "2-digit" })
  const s = isNaN(start.getTime()) ? "" : start.toLocaleTimeString(undefined, opts)
  if (!ev.end) return s
  const end = new Date(ev.end)
  return isNaN(end.getTime()) ? s : `${s} – ${end.toLocaleTimeString(undefined, opts)}`
}

// clockTime renders an instant as the local hour and minute.
function clockTime(value: string): string {
  return new Date(value).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))
}

// dateKey returns a local YYYY-MM-DD key for a Date, used to bucket events
// into the calendar grid's day cells.
function dateKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// eventDayKey returns the local day key an event belongs to. All-day events
// carry a date-only start ("YYYY-MM-DD"); timed events carry an RFC3339 instant.
function eventDayKey(ev: CalendarEvent): string {
  const raw = ev.allDay && ev.start.length === 10 ? `${ev.start}T00:00:00` : ev.start
  const d = new Date(raw)
  return isNaN(d.getTime()) ? "" : dateKey(d)
}

// monthMatrix returns the 42 days (6 weeks) that fill the grid for the month
// containing cursor, including trailing days from adjacent months. The week
// starts on firstDayOfWeek (0=Sun..6=Sat), matching the user's locale setting.
function monthMatrix(cursor: Date, firstDayOfWeek: number): Date[] {
  const year = cursor.getFullYear()
  const month = cursor.getMonth()
  const first = new Date(year, month, 1)
  const offset = (first.getDay() - firstDayOfWeek + 7) % 7
  const days: Date[] = []
  for (let i = 0; i < 42; i++) {
    days.push(new Date(year, month, 1 - offset + i))
  }
  return days
}

// weekDays returns the consecutive days that fill a time-grid view anchored to
// the firstDayOfWeek of the week containing cursor. count is the number of day
// columns (1 for day, 5 for work week, 7 for full week), the standard
// "days in columns" calendar layout with time on the vertical axis.
function weekDays(cursor: Date, count: number, firstDayOfWeek: number): Date[] {
  const d = new Date(cursor.getFullYear(), cursor.getMonth(), cursor.getDate())
  const offset = (d.getDay() - firstDayOfWeek + 7) % 7
  const start = new Date(d.getFullYear(), d.getMonth(), d.getDate() - offset)
  const days: Date[] = []
  for (let i = 0; i < count; i++) {
    days.push(new Date(start.getFullYear(), start.getMonth(), start.getDate() + i))
  }
  return days
}

// PX_PER_MINUTE positions timed events in the time grid (60 px per hour). The
// grid renders one row per hour over 24 hours, so an event's vertical offset is
// its minutes-since-local-midnight times this factor.
const PX_PER_MINUTE = 1

// timedEventForDay returns the timed events that fall on the given local day,
// positioned within the day column. An event spanning midnight is clamped to the
// day boundary (it renders in the start day only), so the height is the overlap
// of the event with the day, never negative. All-day events are excluded; they
// render in the all-day header row.
function timedEventsForDay(events: CalendarEvent[], day: Date): CalendarEvent[] {
  const key = dateKey(day)
  return events.filter((ev) => !ev.allDay && eventDayKey(ev) === key)
}

// allDayEventsForDay returns the all-day events that fall on the given local day.
function allDayEventsForDay(events: CalendarEvent[], day: Date): CalendarEvent[] {
  const key = dateKey(day)
  return events.filter((ev) => ev.allDay && eventDayKey(ev) === key)
}

// eventTopMinutes returns the minutes offset from the day's local midnight at
// which the event's box should start, clamped to >= 0 (a past-midnight start
// renders at the top of the column).
function eventTopMinutes(ev: CalendarEvent, day: Date): number {
  const start = new Date(ev.start)
  const dayStart = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 0, 0)
  return Math.max(0, Math.round((start.getTime() - dayStart.getTime()) / 60000))
}

// eventHeightMinutes returns the event's height in minutes, clamped so the box
// never overflows the day and is never shorter than 15 minutes (a readable sliver
// for short meetings). The end defaults to one hour after start when absent.
function eventHeightMinutes(ev: CalendarEvent, day: Date): number {
  const start = new Date(ev.start)
  const end = ev.end ? new Date(ev.end) : new Date(start.getTime() + 60 * 60 * 1000)
  const dayEnd = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 23, 59, 59, 999)
  const s = Math.max(start.getTime(), new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime())
  const e = Math.min(end.getTime(), dayEnd.getTime())
  return Math.max(15, Math.round((e - s) / 60000))
}

// rangeLabel renders the human label for a day/week/work-week range so the
// header shows the spanned days, not just the anchor month.
function rangeLabel(days: Date[]): string {
  const first = days[0]
  const last = days[days.length - 1]
  if (days.length === 1) {
    return first.toLocaleDateString(undefined, withTz({ weekday: "long", year: "numeric", month: "long", day: "numeric" }))
  }
  const sameMonth = first.getMonth() === last.getMonth() && first.getFullYear() === last.getFullYear()
  const dayNum = (d: Date) => d.getDate()
  if (sameMonth) {
    return `${first.toLocaleDateString(undefined, withTz({ month: "long" }))} ${dayNum(first)} – ${dayNum(last)}, ${first.getFullYear()}`
  }
  return `${first.toLocaleDateString(undefined, withTz({ month: "short", day: "numeric" }))} – ${last.toLocaleDateString(undefined, withTz({ month: "short", day: "numeric" }))}, ${last.getFullYear()}`
}

// moveCursor advances the cursor by one step for the active view: a day for the
// day view, a week for the work-week/week views, a month for the month view.
function moveCursor(cursor: Date, view: CalendarView, sign: number): Date {
  if (view === "month") return new Date(cursor.getFullYear(), cursor.getMonth() + sign, 1)
  const days = view === "day" ? 1 : 7
  return new Date(cursor.getFullYear(), cursor.getMonth(), cursor.getDate() + sign * days)
}

// weekdayNames returns the localized short weekday names, Sunday first.
function weekdayNames(t: TFunc): string[] {
  return [
    t("calendar.weekdays.sun"),
    t("calendar.weekdays.mon"),
    t("calendar.weekdays.tue"),
    t("calendar.weekdays.wed"),
    t("calendar.weekdays.thu"),
    t("calendar.weekdays.fri"),
    t("calendar.weekdays.sat"),
  ]
}

// groupByDay groups sorted events by their day label for the agenda view.
function groupByDay(events: CalendarEvent[]): { day: string; items: CalendarEvent[] }[] {
  const groups: { day: string; items: CalendarEvent[] }[] = []
  for (const ev of events) {
    const key = dayKey(ev.start)
    const last = groups[groups.length - 1]
    if (last && last.day === key) last.items.push(ev)
    else groups.push({ day: key, items: [ev] })
  }
  return groups
}

// bucketByDay buckets events by their local day key for the month grid.
function bucketByDay(events: CalendarEvent[]): Map<string, CalendarEvent[]> {
  const byDay = new Map<string, CalendarEvent[]>()
  for (const ev of events) {
    const key = eventDayKey(ev)
    if (!key) continue
    const bucket = byDay.get(key)
    if (bucket) bucket.push(ev)
    else byDay.set(key, [ev])
  }
  return byDay
}

function colorBorder(color: string | undefined): CSSProperties | undefined {
  return color ? { borderLeft: `3px solid ${color}` } : undefined
}

// shownEventsOf keeps the events of the calendars the user has toggled
// visible (all by default); when calendar metadata is unavailable, it keeps
// everything.
function shownEventsOf(events: CalendarEvent[], calendars: Calendar[], visibleIds: Set<string>): CalendarEvent[] {
  if (calendars.length === 0) return events
  return events.filter((ev) => visibleIds.has(ev.calendarId ?? "calendar"))
}

// eventColorFor returns the per-calendar color that tints an event in the
// views; only when more than one calendar exists (a single calendar needs no
// color distinction).
function eventColorFor(calendars: Calendar[]): (ev: CalendarEvent) => string | undefined {
  const colorById = new Map(calendars.map((c) => [c.id, c.color]))
  return (ev) => (calendars.length > 1 ? (colorById.get(ev.calendarId ?? "calendar") ?? DEFAULT_COLOR) : undefined)
}

// GridSettings is the DB-backed calendar display settings (week-start day,
// time-grid resolution, working-hours window, non-working-hours visibility),
// persisted in the shared webmail settings blob so they survive a reload and
// apply cross-device.
interface GridSettings {
  firstDayOfWeek: number
  resolution: number
  workDayStart: number
  workDayEnd: number
  showNonWorkingHours: boolean
}

// DEFAULT_GRID holds the Exchange values the grid uses until the settings load.
const DEFAULT_GRID: GridSettings = { firstDayOfWeek: 1, resolution: 30, workDayStart: 9, workDayEnd: 18, showNonWorkingHours: true }

function gridSettingsOf(res: CalendarSettings): GridSettings {
  return {
    firstDayOfWeek: res.firstDayOfWeek ?? DEFAULT_GRID.firstDayOfWeek,
    resolution: res.resolution ?? DEFAULT_GRID.resolution,
    workDayStart: res.workDayStart ?? DEFAULT_GRID.workDayStart,
    workDayEnd: res.workDayEnd ?? DEFAULT_GRID.workDayEnd,
    showNonWorkingHours: res.showNonWorkingHours ?? DEFAULT_GRID.showNonWorkingHours,
  }
}

function useGridSettings() {
  const [grid, setGrid] = useState<GridSettings>(DEFAULT_GRID)
  // stored is the whole settings object the server returned. The PUT replaces
  // the whole object, so a grid change is written on top of it; null means the
  // read failed and nothing is persisted, because writing the grid defaults
  // would overwrite the working days and event defaults set in settings.
  const stored = useRef<CalendarSettings | null>(null)
  useEffect(() => {
    api.getCalendarSettings()
      .then((res) => {
        stored.current = res
        setGrid(gridSettingsOf(res))
      })
      .catch(() => {
        /* keep the defaults when settings are unavailable */
      })
  }, [])
  // saveGrid applies the change in-session at once, then persists it.
  const saveGrid = (next: GridSettings) => {
    setGrid(next)
    if (!stored.current) return
    const merged = { ...stored.current, ...next }
    stored.current = merged
    api.setCalendarSettings(merged).catch(() => {
      /* best-effort: the grid keeps the chosen values in-session */
    })
  }
  return { grid, saveGrid }
}

// useCalendarData loads the events around the cursor, the calendars with their
// visibility, and the rooms and categories the event editor offers.
function useCalendarData(cursor: Date) {
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [calendars, setCalendars] = useState<Calendar[]>([])
  const [visibleCalendarIds, setVisibleCalendarIds] = useState<Set<string>>(new Set(["default"]))
  const [rooms, setRooms] = useState<Room[]>([])
  // allCategories is the user's master category list (name + color), loaded once
  // so the event form can offer the same palette the mail/settings pages use.
  const [allCategories, setAllCategories] = useState<CategoryOption[]>([])

  const loadCalendars = useCallback(async () => {
    try {
      const res = await api.getCalendars()
      const cals = res.calendars ?? []
      setCalendars(cals)
      // Show all calendars by default.
      setVisibleCalendarIds(new Set(cals.map((c) => c.id)))
    } catch {
      // Fallback: single default calendar.
      setCalendars([])
    }
  }, [])

  // Fetch a bounded window around the cursor's month (previous month through the
  // month after next) rather than the whole calendar, so the backend export scales
  // with the visible range, not the age of the account. The window is keyed on
  // year/month, so day/week navigation within a month reuses the same fetch and
  // only a month change triggers a reload.
  const cursorYear = cursor.getFullYear()
  const cursorMonth = cursor.getMonth()
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const start = new Date(cursorYear, cursorMonth - 1, 1)
      const end = new Date(cursorYear, cursorMonth + 2, 1)
      const res = await api.getCalendarEvents({ start: start.toISOString(), end: end.toISOString() })
      const list = (res.events ?? []).slice().sort((a, b) => a.start.localeCompare(b.start))
      setEvents(list)
    } catch {
      setEvents([])
    } finally {
      setLoading(false)
    }
  }, [cursorYear, cursorMonth])

  useEffect(() => {
    load()
    loadCalendars()
  }, [load, loadCalendars])

  // Load the organization's bookable rooms for the room picker.
  useEffect(() => {
    api.getRooms()
      .then((res) => setRooms(res.rooms ?? []))
      .catch(() => setRooms([]))
    api.getCategories()
      .then((res) => setAllCategories(res.categories ?? []))
      .catch(() => setAllCategories([]))
  }, [])

  const toggleVisible = (id: string) => {
    setVisibleCalendarIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return { events, loading, load, calendars, loadCalendars, visibleCalendarIds, toggleVisible, rooms, allCategories }
}

// announceCreated reports a new event, warning when a booked room declined.
function announceCreated(t: TFunc, created: CalendarEvent) {
  const unbooked = (created as { unbookedRooms?: string[] }).unbookedRooms ?? []
  if (unbooked.length > 0) {
    toast.warning(t("calendar.eventCreatedRoomsBusy", { rooms: unbooked.join(", ") }))
  } else {
    toast.success(t("calendar.eventCreated"))
  }
}

// useEventEditor holds the event dialog, the delete confirmation and the
// mutations behind them. load refreshes the events after a change.
function useEventEditor(load: () => Promise<void>) {
  const { t } = useI18n()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingUID, setEditingUID] = useState<string | null>(null)
  // editingTracking holds the per-attendee response status of the event being
  // edited, shown read-only as the organizer's TrackingTab. Null when not editing.
  const [editingTracking, setEditingTracking] = useState<CalendarEvent["tracking"] | null>(null)
  const [form, setForm] = useState<EventForm>(emptyEventForm)
  // One mutation at a time: a button's disabled attribute is not the guard,
  // because a second click can arrive before React re-renders with the new state.
  const { busy, begin: beginMutation, end: endMutation } = useBusyGate()
  const [deleteTarget, setDeleteTarget] = useState<CalendarEvent | null>(null)

  const openWith = (uid: string | null, tracking: CalendarEvent["tracking"] | null, next: EventForm) => {
    setEditingUID(uid)
    setEditingTracking(tracking)
    setForm(next)
    setDialogOpen(true)
  }

  const openCreate = () => openWith(null, null, emptyEventForm())

  // openCreateOn opens the new-event dialog prefilled for the clicked grid day.
  // When start/end are supplied (a drag-select time range), the dialog opens with
  // that exact window; otherwise it defaults to 09:00 for an hour.
  const openCreateOn = (day: Date, start?: Date, end?: Date) => {
    const s = start ?? new Date(day.getFullYear(), day.getMonth(), day.getDate(), 9, 0)
    const e = end ?? new Date(s.getFullYear(), s.getMonth(), s.getDate(), s.getHours() + 1, s.getMinutes())
    openWith(null, null, { ...emptyEventForm(), start: rfc3339ToLocalInput(s.toISOString()), end: rfc3339ToLocalInput(e.toISOString()) })
  }

  const openEdit = (ev: CalendarEvent) => openWith(ev.uid, ev.tracking ?? null, eventFormOf(ev))

  const save = async (payload: EventPayload) => {
    if (editingUID) {
      await api.updateCalendarEvent(editingUID, payload)
      toast.success(t("calendar.eventUpdated"))
      return
    }
    announceCreated(t, await api.createCalendarEvent(payload))
  }

  const submit = async () => {
    const problem = eventFormError(form)
    if (problem) {
      toast.error(t(problem))
      return
    }
    const payload = eventPayload(form, getDisplayTimeZone() || detectTimeZone())
    if (!beginMutation()) return
    try {
      await save(payload)
      setDialogOpen(false)
      await load()
    } catch (err) {
      toast.error(errorText(err, t("calendar.saveFailed")))
    } finally {
      endMutation()
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    if (!beginMutation()) return
    try {
      await api.deleteCalendarEvent(deleteTarget.uid)
      toast.success(t("calendar.eventDeleted"))
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(errorText(err, t("calendar.deleteFailed")))
    } finally {
      endMutation()
    }
  }

  // moveEvent commits a drag-move/resize: PUT the event with the new start/end
  // (preserving its other fields) and reload. Best-effort: a failure toasts.
  const moveEvent = async (ev: CalendarEvent, start: Date, end: Date) => {
    try {
      await api.updateCalendarEvent(ev.uid, {
        summary: ev.summary,
        start: start.toISOString(),
        end: end.toISOString(),
        allDay: ev.allDay,
        calendarId: ev.calendarId ?? "calendar",
        location: ev.location,
        description: ev.description,
        attendees: ev.attendees,
        optionalAttendees: ev.optionalAttendees,
        recurrence: ev.recurrence,
        reminderMinutes: ev.reminderMinutes,
        busyStatus: ev.busyStatus,
        sensitivity: ev.sensitivity,
        categories: ev.categories,
      })
      await load()
    } catch (err) {
      toast.error(errorText(err, t("calendar.saveFailed")))
      await load()
    }
  }

  return {
    dialogOpen, setDialogOpen, editingUID, editingTracking, form, setForm, busy, submit,
    openCreate, openCreateOn, openEdit, deleteTarget, setDeleteTarget, confirmDelete, moveEvent,
  }
}

type EventEditor = ReturnType<typeof useEventEditor>

// freeBusyError returns the i18n key of the first reason a free/busy lookup
// cannot run, or null when it can.
function freeBusyError(emails: string[], date: string): string | null {
  if (emails.length === 0) return "calendar.enterEmail"
  if (!date) return "calendar.pickDate"
  return null
}

// useFreeBusy holds the availability lookup dialog.
function useFreeBusy() {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [emails, setEmails] = useState("")
  const [date, setDate] = useState(() => dateKey(new Date()))
  const [loading, setLoading] = useState(false)
  const [results, setResults] = useState<UserFreeBusy[] | null>(null)

  const show = () => {
    setResults(null)
    setOpen(true)
  }

  const check = async () => {
    const list = parseAttendees(emails)
    const problem = freeBusyError(list, date)
    if (problem) {
      toast.error(t(problem))
      return
    }
    // Query the whole local day.
    const dayStart = new Date(`${date}T00:00:00`)
    const dayEnd = new Date(dayStart.getTime() + 24 * 60 * 60 * 1000)
    setLoading(true)
    setResults(null)
    try {
      const res = await api.getFreeBusy(list, dayStart.toISOString(), dayEnd.toISOString())
      setResults(res.freeBusy ?? [])
    } catch (err) {
      toast.error(errorText(err, t("calendar.availabilityFailed")))
    } finally {
      setLoading(false)
    }
  }

  return { open, setOpen, show, emails, setEmails, date, setDate, loading, results, check }
}

type FreeBusy = ReturnType<typeof useFreeBusy>

interface CalendarFormState {
  name: string
  description: string
  color: string
}

const EMPTY_CALENDAR_FORM: CalendarFormState = { name: "", description: "", color: DEFAULT_COLOR }

// useCalendarEditor holds the calendar create/edit dialog and the calendar
// delete confirmation. loadCalendars refreshes the list after a change.
function useCalendarEditor(loadCalendars: () => Promise<void>) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<"create" | "edit">("create")
  const [target, setTarget] = useState<Calendar | null>(null)
  const [form, setForm] = useState<CalendarFormState>(EMPTY_CALENDAR_FORM)
  // One mutation at a time: a second click can arrive before React re-renders
  // with the disabled button, and a second create is a duplicate calendar.
  const { busy, begin: beginMutation, end: endMutation } = useBusyGate()
  const [deleteTarget, setDeleteTarget] = useState<Calendar | null>(null)

  const openNew = () => {
    setMode("create")
    setTarget(null)
    setForm(EMPTY_CALENDAR_FORM)
    setOpen(true)
  }

  const openEdit = (cal: Calendar) => {
    setMode("edit")
    setTarget(cal)
    setForm({ name: cal.name, description: cal.description ?? "", color: cal.color ?? DEFAULT_COLOR })
    setOpen(true)
  }

  const save = async () => {
    // The description is always sent: an empty one clears the stored one.
    const input = { name: form.name, description: form.description, color: form.color || DEFAULT_COLOR }
    if (mode === "create") {
      await api.createCalendar(input)
      toast.success(t("calendar.calendarCreated"))
    } else if (target) {
      await api.updateCalendar(target.id, input)
      toast.success(t("calendar.calendarUpdated"))
    }
  }

  const submit = async () => {
    if (!form.name.trim()) {
      toast.error(t("calendar.calendarNameRequired"))
      return
    }
    if (!beginMutation()) return
    try {
      await save()
      setOpen(false)
      await loadCalendars()
    } catch (err) {
      toast.error(errorText(err, t("calendar.calendarSaveFailed")))
    } finally {
      endMutation()
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    if (!beginMutation()) return
    try {
      await api.deleteCalendar(deleteTarget.id)
      toast.success(t("calendar.calendarDeleted"))
      setDeleteTarget(null)
      await loadCalendars()
    } catch (err) {
      toast.error(errorText(err, t("calendar.calendarDeleteFailed")))
    } finally {
      endMutation()
    }
  }

  return { open, setOpen, mode, form, setForm, busy, submit, openNew, openEdit, deleteTarget, setDeleteTarget, confirmDelete }
}

type CalendarEditor = ReturnType<typeof useCalendarEditor>

export function CalendarPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const { grid, saveGrid } = useGridSettings()
  // View toggle: agenda list, day/week/work-week time grid, or month grid. cursor
  // is the displayed month (month view) or the anchor day (day/week/work-week).
  const [view, setView] = useState<CalendarView>("list")
  const [cursor, setCursor] = useState(() => new Date())
  // sideBySide renders each visible calendar in its own day/week grid (columns)
  // instead of overlaying them in one shared grid; only meaningful with 2+ visible
  // calendars, so the toggle is hidden otherwise.
  const [sideBySide, setSideBySide] = useState(false)
  const [shareCal, setShareCal] = useState<Calendar | null>(null)
  const data = useCalendarData(cursor)
  const editor = useEventEditor(data.load)
  const freeBusy = useFreeBusy()
  const calEditor = useCalendarEditor(data.loadCalendars)

  const dayNames = weekdayNames(t)
  // weekdayLabels rotated to start on firstDayOfWeek, so the month-grid header
  // and the time-grid day header columns align with monthMatrix/weekDays.
  const weekdayLabels = [...dayNames.slice(grid.firstDayOfWeek), ...dayNames.slice(0, grid.firstDayOfWeek)]
  const visibleCalendars = data.calendars.filter((c) => data.visibleCalendarIds.has(c.id))
  const shownEvents = shownEventsOf(data.events, data.calendars, data.visibleCalendarIds)
  const eventColor = eventColorFor(data.calendars)

  return (
    <div className="space-y-4">
      <CalendarToolbar
        view={view}
        onView={setView}
        grid={grid}
        onGrid={saveGrid}
        dayNames={dayNames}
        cursor={cursor}
        onCursor={setCursor}
        onAvailability={freeBusy.show}
        showSideBySide={visibleCalendars.length >= 2 && isTimeGrid(view)}
        sideBySide={sideBySide}
        onToggleSideBySide={() => setSideBySide((s) => !s)}
        onCreate={editor.openCreate}
      />
      {data.calendars.length > 0 && (
        <CalendarChips
          calendars={data.calendars}
          visibleIds={data.visibleCalendarIds}
          onToggle={data.toggleVisible}
          onEdit={calEditor.openEdit}
          onShare={setShareCal}
          onDelete={calEditor.setDeleteTarget}
          onAdd={calEditor.openNew}
        />
      )}
      <CalendarBody
        loading={data.loading}
        view={view}
        hasEvents={data.events.length > 0}
        cursor={cursor}
        setCursor={setCursor}
        grid={grid}
        weekdayLabels={weekdayLabels}
        events={shownEvents}
        eventColor={eventColor}
        editor={editor}
        sideBySide={sideBySide}
        visibleCalendars={visibleCalendars}
      />
      <EventDialog editor={editor} calendars={data.calendars} rooms={data.rooms} categories={data.allCategories} />
      <FreeBusyDialog freeBusy={freeBusy} />
      <ConfirmDeleteDialog
        open={editor.deleteTarget !== null}
        title={t("calendar.deleteEvent")}
        description={t("calendar.deleteConfirm", { name: editor.deleteTarget?.summary ?? "" })}
        busy={editor.busy}
        onCancel={() => editor.setDeleteTarget(null)}
        onConfirm={editor.confirmDelete}
      />
      <CalendarDialog calEditor={calEditor} />
      <ConfirmDeleteDialog
        open={calEditor.deleteTarget !== null}
        title={t("calendar.deleteCalendar")}
        description={t("calendar.deleteCalendarConfirm", { name: calEditor.deleteTarget?.name ?? "" })}
        busy={calEditor.busy}
        onCancel={() => calEditor.setDeleteTarget(null)}
        onConfirm={calEditor.confirmDelete}
      />
      {shareCal && (
        <ShareFolderDialog
          open
          onOpenChange={(open) => {
            if (!open) setShareCal(null)
          }}
          folderName={shareCal.id}
          folderLabel={shareCal.name}
          owner={user?.email ?? ""}
          isOwner={true}
        />
      )}
    </div>
  )
}

function CalendarToolbar(props: {
  view: CalendarView
  onView: (view: CalendarView) => void
  grid: GridSettings
  onGrid: (next: GridSettings) => void
  dayNames: string[]
  cursor: Date
  onCursor: (d: Date) => void
  onAvailability: () => void
  showSideBySide: boolean
  sideBySide: boolean
  onToggleSideBySide: () => void
  onCreate: () => void
}) {
  const { t } = useI18n()
  const { grid, onGrid } = props
  return (
    <div className="flex items-center justify-between" data-print="hide">
      <div className="flex items-center gap-2">
        <CalendarDays className="h-6 w-6 text-primary" />
        <h1 className="text-2xl font-bold">{t("nav.calendar")}</h1>
      </div>
      <div className="flex items-center gap-2">
        <Select value={props.view} onValueChange={(v) => props.onView(v as CalendarView)}>
          <SelectTrigger className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="day">{t("calendar.day")}</SelectItem>
            <SelectItem value="workweek">{t("calendar.workweek")}</SelectItem>
            <SelectItem value="week">{t("calendar.week")}</SelectItem>
            <SelectItem value="month">{t("calendar.month")}</SelectItem>
            <SelectItem value="list">{t("calendar.list")}</SelectItem>
          </SelectContent>
        </Select>
        <Select value={String(grid.firstDayOfWeek)} onValueChange={(v) => onGrid({ ...grid, firstDayOfWeek: Number(v) })}>
          <SelectTrigger className="w-28" aria-label={t("calendar.firstDayOfWeek")} title={t("calendar.firstDayOfWeek")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {props.dayNames.map((label, idx) => (
              <SelectItem key={idx} value={String(idx)}>{label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={String(grid.resolution)} onValueChange={(v) => onGrid({ ...grid, resolution: Number(v) })}>
          <SelectTrigger className="w-24" aria-label={t("calendar.resolution")} title={t("calendar.resolution")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RESOLUTION_MINUTES.map((m) => (
              <SelectItem key={m} value={String(m)}>{t("calendar.resolutionMinutes", { n: String(m) })}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button variant="outline" onClick={props.onAvailability}>
          <Users className="mr-2 h-4 w-4" />
          {t("calendar.availability")}
        </Button>
        {props.showSideBySide && (
          <Button
            variant={props.sideBySide ? "secondary" : "outline"}
            onClick={props.onToggleSideBySide}
            title={t("calendar.sideBySide")}
            aria-pressed={props.sideBySide}
          >
            {t("calendar.sideBySide")}
          </Button>
        )}
        <input
          type="date"
          aria-label={t("calendar.jumpToDate")}
          title={t("calendar.jumpToDate")}
          value={dateKey(props.cursor)}
          onChange={(e) => {
            const d = new Date(e.target.value)
            if (!isNaN(d.getTime())) props.onCursor(d)
          }}
          className="rounded-md border bg-background px-2 py-1.5 text-sm outline-none focus:ring-2 focus:ring-primary/20"
        />
        <Button variant="outline" onClick={() => window.print()} title={t("calendar.print")}>
          <Printer className="h-4 w-4" />
        </Button>
        <Button onClick={props.onCreate}>
          <Plus className="mr-2 h-4 w-4" />
          {t("calendar.newEvent")}
        </Button>
      </div>
    </div>
  )
}

// CalendarChips is the calendar overlay bar: every calendar as a chip that
// toggles its visibility, with a menu to edit, share or delete it.
function CalendarChips(props: {
  calendars: Calendar[]
  visibleIds: Set<string>
  onToggle: (id: string) => void
  onEdit: (cal: Calendar) => void
  onShare: (cal: Calendar) => void
  onDelete: (cal: Calendar) => void
  onAdd: () => void
}) {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-2 flex-wrap" data-print="hide">
      <span className="text-sm text-muted-foreground">{t("calendar.calendars")}:</span>
      {props.calendars.map((cal) => (
        <CalendarChip
          key={cal.id}
          cal={cal}
          visible={props.visibleIds.has(cal.id)}
          onToggle={() => props.onToggle(cal.id)}
          onEdit={() => props.onEdit(cal)}
          onShare={() => props.onShare(cal)}
          onDelete={() => props.onDelete(cal)}
        />
      ))}
      <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={props.onAdd}>
        <Plus className="h-3 w-3 mr-1" />
        {t("calendar.addCalendar")}
      </Button>
    </div>
  )
}

function CalendarChip({ cal, visible, onToggle, onEdit, onShare, onDelete }: {
  cal: Calendar
  visible: boolean
  onToggle: () => void
  onEdit: () => void
  onShare: () => void
  onDelete: () => void
}) {
  const { t } = useI18n()
  const color = cal.color ?? DEFAULT_COLOR
  return (
    <div
      className={`flex items-center gap-1.5 rounded-full px-3 py-1 text-sm border transition-colors cursor-pointer ${
        visible ? "opacity-100" : "opacity-50"
      }`}
      style={{ borderColor: color, color, backgroundColor: visible ? `${color}15` : "transparent" }}
      onClick={onToggle}
      title={cal.description}
    >
      <span className="h-2 w-2 rounded-full shrink-0" style={{ backgroundColor: color }} />
      <span className="truncate max-w-24">{cal.name}</span>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="h-5 w-5 ml-1 p-0 opacity-50 hover:opacity-100"
            onClick={(e) => e.stopPropagation()}
          >
            <Settings2 className="h-3 w-3" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={onEdit}>
            <Edit className="mr-2 h-4 w-4" />
            {t("common.edit")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={onShare}>
            <Share2 className="mr-2 h-4 w-4" />
            {t("share.dialogTitle")}
          </DropdownMenuItem>
          {!cal.isDefault && (
            <DropdownMenuItem className="text-destructive" onClick={onDelete}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

// ViewProps is what every calendar view reads: the cursor, the display
// settings, the visible events and the editor that opens or moves them.
interface ViewProps {
  cursor: Date
  setCursor: Dispatch<SetStateAction<Date>>
  grid: GridSettings
  weekdayLabels: string[]
  events: CalendarEvent[]
  eventColor: (ev: CalendarEvent) => string | undefined
  editor: EventEditor
}

function CalendarBody(props: ViewProps & {
  loading: boolean
  view: CalendarView
  hasEvents: boolean
  sideBySide: boolean
  visibleCalendars: Calendar[]
}) {
  const { t } = useI18n()
  const { loading, view, hasEvents, sideBySide, visibleCalendars, ...viewProps } = props
  if (loading) return <p className="text-sm text-muted-foreground py-8 text-center">{t("common.loading")}</p>
  if (view === "month") return <MonthView {...viewProps} />
  if (isTimeGrid(view)) return <TimeGridBody {...viewProps} view={view} sideBySide={sideBySide} visibleCalendars={visibleCalendars} />
  if (!hasEvents) return <NoEvents onCreate={props.editor.openCreate} />
  return <AgendaView events={props.events} eventColor={props.eventColor} editor={props.editor} />
}

// PeriodNav is the heading of a month or time-grid view with its today,
// previous and next buttons.
function PeriodNav({ label, prevLabel, nextLabel, onPrev, onNext, onToday }: {
  label: string
  prevLabel: string
  nextLabel: string
  onPrev: () => void
  onNext: () => void
  onToday: () => void
}) {
  const { t } = useI18n()
  return (
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">{label}</h2>
      <div className="flex items-center gap-1" data-print="hide">
        <Button variant="outline" size="sm" onClick={onToday}>
          {t("common.today")}
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={prevLabel} onClick={onPrev}>
          <ChevronLeft className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={nextLabel} onClick={onNext}>
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

function MonthView({ cursor, setCursor, grid, weekdayLabels, events, eventColor, editor }: ViewProps) {
  const { t } = useI18n()
  const eventsByDay = bucketByDay(events)
  const todayKey = dateKey(new Date())
  return (
    <div className="space-y-3">
      <PeriodNav
        label={cursor.toLocaleDateString(undefined, { month: "long", year: "numeric" })}
        prevLabel={t("calendar.previousMonth")}
        nextLabel={t("calendar.nextMonth")}
        onPrev={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() - 1, 1))}
        onNext={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() + 1, 1))}
        onToday={() => setCursor(new Date())}
      />
      <div className="overflow-hidden rounded-lg border bg-card">
        <div className="grid grid-cols-7 border-b bg-muted/30 text-center text-xs font-medium text-muted-foreground">
          {weekdayLabels.map((label) => (
            <div key={label} className="py-2">{label}</div>
          ))}
        </div>
        <div className="grid grid-cols-7">
          {monthMatrix(cursor, grid.firstDayOfWeek).map((day) => {
            const key = dateKey(day)
            return (
              <MonthDayCell
                key={key}
                day={day}
                inMonth={day.getMonth() === cursor.getMonth()}
                isToday={key === todayKey}
                events={eventsByDay.get(key) ?? []}
                eventColor={eventColor}
                editor={editor}
              />
            )
          })}
        </div>
      </div>
    </div>
  )
}

function dayNumberClass(isToday: boolean, hasEvents: boolean): string {
  if (isToday) return "bg-primary font-semibold text-primary-foreground"
  return hasEvents ? "font-semibold" : ""
}

// MonthDayCell is one day of the month grid: the day number and up to three
// events. A click on the empty cell opens the new-event dialog on that day.
function MonthDayCell({ day, inMonth, isToday, events, eventColor, editor }: {
  day: Date
  inMonth: boolean
  isToday: boolean
  events: CalendarEvent[]
  eventColor: (ev: CalendarEvent) => string | undefined
  editor: EventEditor
}) {
  const { t } = useI18n()
  return (
    <div
      className={`min-h-24 cursor-pointer border-b border-r p-1 transition-colors last:border-r-0 hover:bg-accent/50 ${inMonth ? "" : "bg-muted/20 text-muted-foreground"}`}
      onClick={() => editor.openCreateOn(day)}
    >
      <div className="flex justify-end">
        <span className={`flex h-6 w-6 items-center justify-center rounded-full text-xs ${dayNumberClass(isToday, events.length > 0)}`}>
          {day.getDate()}
        </span>
      </div>
      <div className="mt-0.5 space-y-0.5">
        {events.slice(0, 3).map((ev) => (
          <button
            key={ev.uid}
            className="block w-full truncate rounded bg-primary/10 px-1 py-0.5 text-left text-xs text-foreground hover:bg-primary/20"
            style={colorBorder(eventColor(ev))}
            onClick={(e) => { e.stopPropagation(); editor.openEdit(ev) }}
            title={ev.summary}
          >
            {!ev.allDay && <span className="mr-1 text-muted-foreground">{clockTime(ev.start)}</span>}
            {ev.summary}
          </button>
        ))}
        {events.length > 3 && (
          <p className="px-1 text-xs text-muted-foreground">{t("calendar.moreEvents", { count: String(events.length - 3) })}</p>
        )}
      </div>
    </div>
  )
}

// TimeGridBody renders the day/work-week/week time grid. In side-by-side mode
// (2+ visible calendars) it renders one grid per visible calendar, each
// filtered to its own events; otherwise it overlays all visible calendars in a
// single shared grid.
function TimeGridBody({ view, cursor, setCursor, grid, weekdayLabels, events, eventColor, editor, sideBySide, visibleCalendars }: ViewProps & {
  view: TimeGridView
  sideBySide: boolean
  visibleCalendars: Calendar[]
}) {
  const { t } = useI18n()
  const days = weekDays(cursor, GRID_DAYS[view], grid.firstDayOfWeek)
  const todayKey = dateKey(new Date())
  const renderGrid = (evs: CalendarEvent[], label: string) => (
    <DayTimeGrid
      days={days}
      label={label}
      prevLabel={t(view === "day" ? "calendar.previousDay" : "calendar.previousWeek")}
      nextLabel={t(view === "day" ? "calendar.nextDay" : "calendar.nextWeek")}
      todayLabel={t("common.today")}
      onPrev={() => setCursor((c) => moveCursor(c, view, -1))}
      onNext={() => setCursor((c) => moveCursor(c, view, +1))}
      onToday={() => setCursor(new Date())}
      weekdayLabels={weekdayLabels}
      firstDayOfWeek={grid.firstDayOfWeek}
      resolution={grid.resolution}
      workDayStart={grid.workDayStart}
      workDayEnd={grid.workDayEnd}
      showNonWorkingHours={grid.showNonWorkingHours}
      events={evs}
      eventColor={eventColor}
      todayKey={todayKey}
      onOpenEvent={editor.openEdit}
      onCreateOn={editor.openCreateOn}
      onMoveEvent={editor.moveEvent}
    />
  )
  if (sideBySide && visibleCalendars.length >= 2) {
    return (
      <div className="flex gap-4 overflow-x-auto pb-2">
        {visibleCalendars.map((cal) => (
          <div key={cal.id} className="min-w-[20rem] flex-1">
            {renderGrid(
              events.filter((ev) => (ev.calendarId ?? "calendar") === cal.id),
              `${rangeLabel(days)} - ${cal.name}`,
            )}
          </div>
        ))}
      </div>
    )
  }
  return renderGrid(events, rangeLabel(days))
}

function NoEvents({ onCreate }: { onCreate: () => void }) {
  const { t } = useI18n()
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="rounded-full bg-muted p-4">
        <CalendarDays className="h-8 w-8 text-muted-foreground" />
      </div>
      <h3 className="mt-4 text-lg font-medium">{t("calendar.noEvents")}</h3>
      <p className="text-muted-foreground mt-1">{t("calendar.noEventsHint")}</p>
      <Button className="mt-4" onClick={onCreate}>
        <Plus className="mr-2 h-4 w-4" />
        {t("calendar.newEvent")}
      </Button>
    </div>
  )
}

function AgendaView({ events, eventColor, editor }: Pick<ViewProps, "events" | "eventColor" | "editor">) {
  return (
    <div className="space-y-6">
      {groupByDay(events).map((group) => (
        <div key={group.day}>
          <h2 className="mb-2 text-sm font-semibold text-muted-foreground">{group.day}</h2>
          <div className="rounded-lg border bg-card divide-y">
            {group.items.map((ev) => (
              <AgendaRow key={ev.uid} ev={ev} color={eventColor(ev)} editor={editor} />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function AgendaDetail({ icon: Icon, children }: { icon: typeof MapPin; children: ReactNode }) {
  return (
    <p className="flex items-center gap-1 text-sm text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
      {children}
    </p>
  )
}

function AgendaRow({ ev, color, editor }: { ev: CalendarEvent; color: string | undefined; editor: EventEditor }) {
  const { t } = useI18n()
  const reminder = ev.reminderMinutes ?? 0
  return (
    <div className="flex items-start gap-4 p-4 hover:bg-accent/50 transition-colors">
      {color && <div className="w-1 self-stretch rounded-full" style={{ backgroundColor: color }} aria-hidden />}
      <div className="flex w-24 shrink-0 items-center gap-1 text-sm text-muted-foreground">
        <Clock className="h-3.5 w-3.5" />
        {timeLabel(t, ev)}
      </div>
      <div className="flex-1 min-w-0">
        <p className="font-medium truncate">{ev.summary}</p>
        {ev.location && <AgendaDetail icon={MapPin}>{ev.location}</AgendaDetail>}
        {ev.recurrence && <AgendaDetail icon={Repeat}>{recurrenceLabel(t, recurrenceToForm(ev.recurrence))}</AgendaDetail>}
        {reminder > 0 && <AgendaDetail icon={Bell}>{t("calendar.reminderMinutes", { n: String(reminder) })}</AgendaDetail>}
        {ev.description && <p className="text-sm text-muted-foreground truncate">{ev.description}</p>}
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="h-8 w-8">
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={() => editor.openEdit(ev)}>
            <Edit className="mr-2 h-4 w-4" />
            {t("common.edit")}
          </DropdownMenuItem>
          <DropdownMenuItem className="text-destructive" onClick={() => editor.setDeleteTarget(ev)}>
            <Trash2 className="mr-2 h-4 w-4" />
            {t("common.delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

// FieldProps is what an event dialog section reads and writes.
interface FieldProps {
  form: EventForm
  update: (patch: Partial<EventForm>) => void
}

function EventDialog({ editor, calendars, rooms, categories }: {
  editor: EventEditor
  calendars: Calendar[]
  rooms: Room[]
  categories: CategoryOption[]
}) {
  const { t } = useI18n()
  const { form, setForm } = editor
  const update = (patch: Partial<EventForm>) => setForm((prev) => ({ ...prev, ...patch }))
  const tracking = editor.editingUID ? editor.editingTracking ?? [] : []
  return (
    <Dialog open={editor.dialogOpen} onOpenChange={editor.setDialogOpen}>
      {/* The event form is taller than a laptop viewport; scroll it so its
          buttons stay reachable. */}
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{editor.editingUID ? t("calendar.editEvent") : t("calendar.newEvent")}</DialogTitle>
          <DialogDescription>{t("calendar.dialogDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="ev-summary">{t("calendar.title")}</Label>
            <Input
              id="ev-summary"
              value={form.summary}
              onChange={(e) => update({ summary: e.target.value })}
              placeholder={t("calendar.titlePlaceholder")}
            />
          </div>
          {calendars.length > 1 && <CalendarField form={form} update={update} calendars={calendars} />}
          <div className="flex items-center justify-between">
            <Label htmlFor="ev-allday">{t("calendar.allDay")}</Label>
            <Switch id="ev-allday" checked={form.allDay} onCheckedChange={(checked) => update({ allDay: checked })} />
          </div>
          <EventOptionFields form={form} update={update} />
          {categories.length > 0 && (
            <div className="space-y-2">
              <Label>{t("calendar.categories")}</Label>
              <CategoryChips
                categories={categories}
                selected={form.categories}
                onToggle={(name, on) => setForm((prev) => ({ ...prev, categories: toggledCategories(prev.categories, name, on) }))}
              />
            </div>
          )}
          {tracking.length > 0 && <TrackingList tracking={tracking} />}
          <EventTimeFields form={form} update={update} />
          <AttendeeFields form={form} update={update} rooms={rooms} />
          {rooms.length > 0 && <RoomPicker form={form} setForm={setForm} rooms={rooms} />}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => editor.setDialogOpen(false)} disabled={editor.busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={editor.submit} disabled={editor.busy}>
            {editor.editingUID ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CalendarField({ form, update, calendars }: FieldProps & { calendars: Calendar[] }) {
  const { t } = useI18n()
  return (
    <div className="space-y-2">
      <Label htmlFor="ev-calendar">{t("calendar.calendar")}</Label>
      <Select value={form.calendarId} onValueChange={(value) => update({ calendarId: value })}>
        <SelectTrigger id="ev-calendar">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {calendars.map((cal) => (
            <SelectItem key={cal.id} value={cal.id}>
              {cal.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

// noneToEmpty maps a select's "none" sentinel back to the form's empty value.
function noneToEmpty(value: string): string {
  return value === "none" ? "" : value
}

// EventOptionFields holds the repeat, reminder, show-as and sensitivity
// selects.
function EventOptionFields({ form, update }: FieldProps) {
  const { t } = useI18n()
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="ev-recurrence">{t("calendar.repeat")}</Label>
        <Select value={form.recurrence} onValueChange={(value) => update({ recurrence: noneToEmpty(value) })}>
          <SelectTrigger id="ev-recurrence">
            <SelectValue placeholder={t("calendar.recurrence.none")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">{recurrenceLabel(t, "")}</SelectItem>
            {RECURRENCE_FREQUENCIES.map((freq) => (
              <SelectItem key={freq} value={freq}>{recurrenceLabel(t, freq)}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="ev-reminder">{t("calendar.reminder")}</Label>
        <Select value={form.reminder} onValueChange={(value) => update({ reminder: noneToEmpty(value) })}>
          <SelectTrigger id="ev-reminder">
            <SelectValue placeholder={t("calendar.reminderNone")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">{t("calendar.reminderNone")}</SelectItem>
            {REMINDER_MINUTES.map((m) => (
              <SelectItem key={m} value={String(m)}>{t("calendar.reminderMinutes", { n: String(m) })}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="ev-busy">{t("calendar.busyStatus")}</Label>
        <Select value={form.busyStatus || "2"} onValueChange={(value) => update({ busyStatus: value })}>
          <SelectTrigger id="ev-busy">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="0">{t("calendar.busyFree")}</SelectItem>
            <SelectItem value="1">{t("calendar.busyTentative")}</SelectItem>
            <SelectItem value="2">{t("calendar.busyBusy")}</SelectItem>
            <SelectItem value="3">{t("calendar.busyOof")}</SelectItem>
            <SelectItem value="4">{t("calendar.busyWorkingElsewhere")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="ev-sens">{t("calendar.sensitivity")}</Label>
        <Select value={form.sensitivity || "0"} onValueChange={(value) => update({ sensitivity: value === "0" ? "" : value })}>
          <SelectTrigger id="ev-sens">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="0">{t("calendar.sensitivityNormal")}</SelectItem>
            <SelectItem value="2">{t("calendar.sensitivityPrivate")}</SelectItem>
            <SelectItem value="3">{t("calendar.sensitivityConfidential")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </>
  )
}

// TrackingList shows the organizer each attendee's response to the meeting.
function TrackingList({ tracking }: { tracking: NonNullable<CalendarEvent["tracking"]> }) {
  const { t } = useI18n()
  return (
    <div className="space-y-2">
      <Label>{t("calendar.tracking")}</Label>
      <ul className="rounded-md border divide-y text-sm">
        {tracking.map((tr) => (
          <li key={tr.email} className="flex items-center justify-between px-3 py-1.5">
            <span className="truncate">{tr.email}</span>
            <span className="shrink-0 text-muted-foreground">{t(RESPONSE_KEYS[tr.response] ?? "calendar.respNone")}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

// EventTimeFields holds the start and end inputs plus the location and
// description.
function EventTimeFields({ form, update }: FieldProps) {
  const { t } = useI18n()
  const inputType = form.allDay ? "date" : "datetime-local"
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="ev-start">{t("calendar.start")}</Label>
          <Input id="ev-start" type={inputType} value={form.start} onChange={(e) => update({ start: e.target.value })} />
        </div>
        <div className="space-y-2">
          <Label htmlFor="ev-end">{t("calendar.end")}</Label>
          <Input id="ev-end" type={inputType} value={form.end} onChange={(e) => update({ end: e.target.value })} />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="ev-location">{t("calendar.location")}</Label>
        <Input
          id="ev-location"
          value={form.location}
          onChange={(e) => update({ location: e.target.value })}
          placeholder={t("common.optional")}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="ev-desc">{t("calendar.description")}</Label>
        <Textarea
          id="ev-desc"
          value={form.description}
          onChange={(e) => update({ description: e.target.value })}
          rows={3}
          placeholder={t("common.optional")}
        />
      </div>
    </>
  )
}

// AttendeeFields holds the required and optional attendee pickers and the
// invite switch. Booked rooms stay out of the people picker.
function AttendeeFields({ form, update, rooms }: FieldProps & { rooms: Room[] }) {
  const { t } = useI18n()
  const { people, selectedRooms } = splitRooms(form.attendees, rooms)
  const busyWindow = pickerWindow(form)
  return (
    <div className="space-y-2">
      <Label>{t("calendar.attendees")}</Label>
      <AttendeePicker
        value={people}
        onChange={(emails) => update({ attendees: [...emails, ...selectedRooms.map((r) => r.email)].join(", ") })}
        window={busyWindow}
      />
      <p className="text-xs text-muted-foreground">
        {t("calendar.attendeesHint")}
      </p>
      {/* Optional attendees (OPT-PARTICIPANT): a separate picker so the
          organizer can distinguish required from optional invitees. */}
      <Label className="pt-1 text-xs text-muted-foreground">{t("calendar.optionalAttendees")}</Label>
      <AttendeePicker
        value={parseAttendees(form.optionalAttendees)}
        onChange={(emails) => update({ optionalAttendees: emails.join(", ") })}
        window={busyWindow}
      />
      {/* Send a METHOD:REQUEST meeting invite to the attendees on save. */}
      <div className="flex items-center justify-between pt-1">
        <Label htmlFor="ev-invite" className="text-sm font-normal cursor-pointer">
          {t("calendar.sendInvite")}
        </Label>
        <Switch id="ev-invite" checked={form.sendInvite} onCheckedChange={(checked) => update({ sendInvite: checked })} />
      </div>
      {form.sendInvite && <p className="text-xs text-muted-foreground">{t("calendar.sendInviteHint")}</p>}
    </div>
  )
}

function roomLabel(t: TFunc, room: Room): string {
  return room.capacity ? `${room.name} ${t("calendar.roomSeats", { count: String(room.capacity) })}` : room.name
}

// RoomPicker books a room as an attendee and lists the booked rooms as chips.
function RoomPicker({ form, setForm, rooms }: {
  form: EventForm
  setForm: Dispatch<SetStateAction<EventForm>>
  rooms: Room[]
}) {
  const { t } = useI18n()
  const { selectedRooms } = splitRooms(form.attendees, rooms)
  const book = (email: string) => {
    const room = rooms.find((r) => r.email === email)
    if (room) setForm((prev) => withRoom(prev, room))
  }
  return (
    <div className="space-y-2">
      <Label htmlFor="ev-room">{t("calendar.room")}</Label>
      <Select value="" onValueChange={book}>
        <SelectTrigger id="ev-room">
          <SelectValue placeholder={t("calendar.addRoom")} />
        </SelectTrigger>
        <SelectContent>
          {rooms.map((room) => (
            <SelectItem key={room.email} value={room.email}>
              {roomLabel(t, room)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {selectedRooms.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {selectedRooms.map((room) => (
            <span key={room.email} className="inline-flex items-center gap-1 rounded-md bg-secondary px-2 py-0.5 text-xs">
              {roomLabel(t, room)}
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                aria-label={`${t("common.remove")} ${room.name}`}
                onClick={() => setForm((prev) => withoutRoom(prev, room))}
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        {t("calendar.roomHint")}
      </p>
    </div>
  )
}

// FreeBusyDialog looks up when the named people are busy on one day.
function FreeBusyDialog({ freeBusy }: { freeBusy: FreeBusy }) {
  const { t } = useI18n()
  return (
    <Dialog open={freeBusy.open} onOpenChange={freeBusy.setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("calendar.checkAvailability")}</DialogTitle>
          <DialogDescription>
            {t("calendar.availabilityDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label>{t("calendar.people")}</Label>
            <AttendeePicker
              value={parseAttendees(freeBusy.emails)}
              onChange={(emails) => freeBusy.setEmails(emails.join(", "))}
              placeholder={t("calendar.searchNameEmail")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="fb-date">{t("common.date")}</Label>
            <Input id="fb-date" type="date" value={freeBusy.date} onChange={(e) => freeBusy.setDate(e.target.value)} />
          </div>
          {freeBusy.results && <FreeBusyResults results={freeBusy.results} />}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => freeBusy.setOpen(false)} disabled={freeBusy.loading}>
            {t("common.close")}
          </Button>
          <Button onClick={freeBusy.check} disabled={freeBusy.loading}>
            {freeBusy.loading ? t("calendar.checking") : t("calendar.check")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function FreeBusyResults({ results }: { results: UserFreeBusy[] }) {
  const { t } = useI18n()
  return (
    <div className="space-y-3 rounded-lg border bg-muted/30 p-3">
      {results.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("common.noResults")}</p>
      ) : (
        results.map((r) => (
          <div key={r.user}>
            <p className="text-sm font-medium">{r.user}</p>
            {r.busy.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("calendar.freeAllDay")}</p>
            ) : (
              <ul className="mt-1 space-y-0.5">
                {r.busy.map((b, i) => (
                  <li key={i} className="flex items-center gap-1.5 text-sm text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    {clockTime(b.start)}
                    {" – "}
                    {clockTime(b.end)}
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))
      )}
    </div>
  )
}

function ConfirmDeleteDialog({ open, title, description, busy, onCancel, onConfirm }: {
  open: boolean
  title: string
  description: string
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useI18n()
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={busy}>
            <Trash2 className="mr-2 h-4 w-4" />
            {t("common.delete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// CalendarDialog creates a calendar or edits one's name, description and color.
function CalendarDialog({ calEditor }: { calEditor: CalendarEditor }) {
  const { t } = useI18n()
  const { form, setForm } = calEditor
  const creating = calEditor.mode === "create"
  return (
    <Dialog open={calEditor.open} onOpenChange={calEditor.setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {creating ? t("calendar.newCalendar") : t("calendar.editCalendar")}
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="cal-name">{t("calendar.calendarName")}</Label>
            <Input
              id="cal-name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder={t("calendar.calendarNamePlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cal-desc">{t("calendar.description")}</Label>
            <Input
              id="cal-desc"
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              placeholder={t("common.optional")}
            />
          </div>
          <ColorField value={form.color} onChange={(color) => setForm({ ...form, color })} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => calEditor.setOpen(false)} disabled={calEditor.busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={calEditor.submit} disabled={calEditor.busy}>
            {creating ? t("common.create") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ColorField({ value, onChange }: { value: string; onChange: (color: string) => void }) {
  const { t } = useI18n()
  return (
    <div className="space-y-2">
      <Label htmlFor="cal-color">{t("calendar.color")}</Label>
      <div className="flex items-center gap-3">
        <input
          id="cal-color"
          type="color"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="h-9 w-14 rounded border cursor-pointer p-0.5"
        />
        <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder={DEFAULT_COLOR} className="font-mono" />
      </div>
      {/* Color scheme palette: quick-pick swatches for the calendar color. */}
      <div className="flex flex-wrap gap-1.5 pt-1">
        {CALENDAR_PALETTE.map((c) => (
          <button
            key={c}
            type="button"
            aria-label={c}
            onClick={() => onChange(c)}
            className={`h-6 w-6 rounded-full border-2 transition-transform hover:scale-110 ${value.toLowerCase() === c ? "border-foreground" : "border-transparent"}`}
            style={{ backgroundColor: c }}
          />
        ))}
      </div>
    </div>
  )
}

// DayTimeGrid renders the day/work-week/week time grid: one column per day with
// an hour gutter, an all-day header row, and timed events positioned by their
// start/end minutes. The layout is days-in-columns with time on the vertical
// axis. The body scrolls over 24 hours; clicking empty space in a day column
// opens the create dialog on that day.
function DayTimeGrid(props: {
  days: Date[]
  label: string
  prevLabel: string
  nextLabel: string
  todayLabel: string
  onPrev: () => void
  onNext: () => void
  onToday: () => void
  weekdayLabels: string[]
  firstDayOfWeek: number
  resolution: number
  workDayStart: number
  workDayEnd: number
  showNonWorkingHours: boolean
  events: CalendarEvent[]
  eventColor: (ev: CalendarEvent) => string | undefined
  todayKey: string
  onOpenEvent: (ev: CalendarEvent) => void
  onCreateOn: (day: Date, start?: Date, end?: Date) => void
  onMoveEvent: (ev: CalendarEvent, start: Date, end: Date) => void
}) {
  const { t } = useI18n()
  // drag is the in-progress click-drag time selection (a click-drag in a day
  // column selects a time range and opens the create dialog on that window).
  // startMin/endMin are absolute minutes since local midnight; dayIdx the column.
  const [drag, setDrag] = useState<{ dayIdx: number; startMin: number; endMin: number } | null>(null)
  // eventDrag moves or resizes an existing event: mode "move" shifts start+end
  // together (duration preserved); mode "resize" drags the end. startMin/endMin
  // are the tentative absolute minutes since midnight on the event's day.
  const [eventDrag, setEventDrag] = useState<{ dayIdx: number; ev: CalendarEvent; mode: "move" | "resize"; startMin: number; endMin: number; durationMin: number } | null>(null)
  // columnRefs maps each day column to its DOM node so a mousemove can compute the
  // time from the cursor's Y offset within the active column.
  const columnRefs = useRef<(HTMLDivElement | null)[]>([])
  // snapMin clamps a minute offset to the configured time-grid resolution.
  const snapMin = (min: number) => {
    const snapped = Math.round(min / props.resolution) * props.resolution
    return Math.max(0, Math.min(24 * 60, snapped))
  }
  // minFromY returns the absolute minutes since local midnight for a client Y in
  // the day column at dayIdx, accounting for the non-working-hours offset.
  const minFromY = (dayIdx: number, clientY: number) => {
    const el = columnRefs.current[dayIdx]
    if (!el) return 0
    const rect = el.getBoundingClientRect()
    const y = clientY - rect.top
    const off = props.showNonWorkingHours ? 0 : props.workDayStart * 60
    return snapMin((y / PX_PER_MINUTE) + off)
  }
  // A drag ends on mouseup anywhere: a no-move click opens the create dialog at
  // the default 09:00; a drag selects the window and opens it prefilled.
  useEffect(() => {
    if (!drag) return
    const onMove = (e: MouseEvent) => {
      setDrag((d) => (d ? { ...d, endMin: minFromY(d.dayIdx, e.clientY) } : d))
    }
    const onUp = () => {
      setDrag((d) => {
        if (d) {
          const lo = Math.min(d.startMin, d.endMin)
          const hi = Math.max(d.startMin, d.endMin)
          const day = props.days[d.dayIdx]
          const start = new Date(day.getFullYear(), day.getMonth(), day.getDate(), Math.floor(lo / 60), lo % 60)
          const end = new Date(day.getFullYear(), day.getMonth(), day.getDate(), Math.floor(hi / 60), hi % 60)
          // A click (lo == hi) opens the default 09:00 dialog; a drag opens the
          // selected window, but only when it spans at least one slot.
          if (hi - lo >= props.resolution) {
            props.onCreateOn(day, start, end)
          } else {
            props.onCreateOn(day)
          }
        }
        return null
      })
    }
    window.addEventListener("mousemove", onMove)
    window.addEventListener("mouseup", onUp)
    return () => {
      window.removeEventListener("mousemove", onMove)
      window.removeEventListener("mouseup", onUp)
    }
  }, [drag, props.days, props.resolution])
  // eventDrag: a global mousemove updates the tentative start/end (move preserves
  // the duration, resize drags only the end); mouseup commits via onMoveEvent.
  useEffect(() => {
    if (!eventDrag) return
    const onMove = (e: MouseEvent) => {
      setEventDrag((d) => {
        if (!d) return d
        const m = snapMin(minFromY(d.dayIdx, e.clientY))
        if (d.mode === "move") {
          return { ...d, startMin: m, endMin: m + d.durationMin }
        }
        return { ...d, endMin: Math.max(m, d.startMin + props.resolution) }
      })
    }
    const onUp = () => {
      setEventDrag((d) => {
        if (d) {
          const day = props.days[d.dayIdx]
          const start = new Date(day.getFullYear(), day.getMonth(), day.getDate(), Math.floor(d.startMin / 60), d.startMin % 60)
          const end = new Date(day.getFullYear(), day.getMonth(), day.getDate(), Math.floor(d.endMin / 60), d.endMin % 60)
          props.onMoveEvent(d.ev, start, end)
        }
        return null
      })
    }
    window.addEventListener("mousemove", onMove)
    window.addEventListener("mouseup", onUp)
    return () => {
      window.removeEventListener("mousemove", onMove)
      window.removeEventListener("mouseup", onUp)
    }
  }, [eventDrag, props.days, props.resolution])
  // hoursToRender is the set of hour rows the grid shows. When non-working hours
  // are hidden, only the working window (workDayStart..workDayEnd) renders, so the
  // grid focuses on the user's business hours; events outside it still render via
  // their absolute offsets but are clipped to the visible window.
  const hours = Array.from({ length: 24 }, (_, i) => i).filter(
    (h) => props.showNonWorkingHours || (h >= props.workDayStart && h <= props.workDayEnd),
  )
  const gridHeight = props.showNonWorkingHours ? 24 * 60 : (props.workDayEnd - props.workDayStart + 1) * 60
  // offset is the vertical shift applied when non-working hours are hidden: the
  // grid's top becomes workDayStart:00, so every absolute minute offset is reduced
  // by workDayStart*60 to land in the visible window.
  const offset = props.showNonWorkingHours ? 0 : props.workDayStart * 60
  // nonWorkingRanges are the hour spans shaded as non-working when the full day is
  // shown: before workDayStart and after workDayEnd.
  const nonWorkingRanges = [
    { start: 0, end: props.workDayStart },
    { start: props.workDayEnd + 1, end: 24 },
  ].filter((r) => r.end > r.start)
  const colTemplate = `56px repeat(${props.days.length}, minmax(0, 1fr))`
  // slotLines are the minute offsets within each hour at which a finer grid line
  // is drawn (e.g. 30 for a 30-min resolution, 15/30/45 for 15-min). The top-of-hour
  // line is drawn separately as the hour boundary.
  const slotLines = props.resolution < 60 ? Array.from({ length: 60 / props.resolution - 1 }, (_, i) => (i + 1) * props.resolution) : []
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{props.label}</h2>
        <div className="flex items-center gap-1" data-print="hide">
          <Button variant="outline" size="sm" onClick={props.onToday}>{props.todayLabel}</Button>
          <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={props.prevLabel} onClick={props.onPrev}>
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={props.nextLabel} onClick={props.onNext}>
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      </div>
      <div className="overflow-hidden rounded-lg border bg-card">
        {/* Day header row: a corner gutter plus one header per day. */}
        <div className="grid border-b bg-muted/30" style={{ gridTemplateColumns: colTemplate }}>
          <div className="py-2" />
          {props.days.map((day) => {
            const key = dateKey(day)
            const isToday = key === props.todayKey
            return (
              <div key={key} className={`border-l py-2 text-center ${isToday ? "text-primary font-semibold" : ""}`}>
                <div className="text-xs text-muted-foreground">{props.weekdayLabels[(day.getDay() - props.firstDayOfWeek + 7) % 7]}</div>
                <div className="text-sm">{day.getDate()}</div>
              </div>
            )
          })}
        </div>
        {/* All-day events row. */}
        <div className="grid border-b" style={{ gridTemplateColumns: colTemplate }}>
          <div className="py-1 text-center text-[10px] text-muted-foreground">{t("calendar.allDay")}</div>
          {props.days.map((day) => {
            const key = dateKey(day)
            const allDay = allDayEventsForDay(props.events, day)
            return (
              <div key={key} className="min-h-7 border-l px-1 py-0.5">
                {allDay.map((ev) => (
                  <button
                    key={ev.uid}
                    onClick={() => props.onOpenEvent(ev)}
                    className="block w-full truncate rounded bg-primary/15 px-1 py-0.5 text-left text-xs"
                    style={props.eventColor(ev) ? { borderLeft: `3px solid ${props.eventColor(ev)}` } : undefined}
                    title={ev.summary}
                  >
                    {ev.summary}
                  </button>
                ))}
              </div>
            )
          })}
        </div>
        {/* Time grid body: hour gutter + day columns, scrollable over 24 hours.
            data-print="content" lets the grid expand in print instead of clipping
            at the 70vh scroll cap. */}
        <div className="max-h-[70vh] overflow-y-auto" data-print="content">
          <div className="grid" style={{ gridTemplateColumns: colTemplate }}>
            <div className="relative" style={{ height: gridHeight }}>
              {hours.map((h) => (
                <div key={h} className="absolute right-1 text-[10px] text-muted-foreground" style={{ top: h * 60 - offset - 6 }}>
                  {String(h).padStart(2, "0")}:00
                </div>
              ))}
            </div>
            {props.days.map((day, dayIdx) => {
              const key = dateKey(day)
              const isToday = key === props.todayKey
              const timed = timedEventsForDay(props.events, day)
              return (
                <div
                  key={key}
                  ref={(el) => { columnRefs.current[dayIdx] = el }}
                  className={`relative overflow-hidden border-l ${isToday ? "bg-primary/5" : ""}`}
                  style={{ height: gridHeight }}
                  onMouseDown={(e) => {
                    // Start a drag-select at the snapped time under the cursor.
                    e.preventDefault()
                    const m = minFromY(dayIdx, e.clientY)
                    setDrag({ dayIdx, startMin: m, endMin: m })
                  }}
                >
                  {/* Shade non-working hours when the grid shows the full day. */}
                  {props.showNonWorkingHours && nonWorkingRanges.map((r, i) => (
                    <div
                      key={`nw-${i}`}
                      className="absolute left-0 right-0 bg-muted/30"
                      style={{ top: r.start * 60 - offset, height: (r.end - r.start) * 60 }}
                    />
                  ))}
                  {hours.map((h) => (
                    <div key={h} className="absolute left-0 right-0 border-t border-border/40" style={{ top: h * 60 - offset }} />
                  ))}
                  {/* Resolution slot lines (finer than the hour boundary). */}
                  {hours.flatMap((h) =>
                    slotLines.map((m) => (
                      <div
                        key={`${h}-${m}`}
                        className="absolute left-0 right-0 border-t border-dashed border-border/20"
                        style={{ top: h * 60 + m - offset }}
                      />
                    )),
                  )}
                  {/* Drag-select time range: a highlighted band from start to end. */}
                  {drag && drag.dayIdx === dayIdx && (
                    <div
                      className="absolute left-0.5 right-0.5 rounded bg-primary/30 border border-primary/50 pointer-events-none"
                      style={{
                        top: Math.min(drag.startMin, drag.endMin) * PX_PER_MINUTE - offset,
                        height: Math.abs(drag.endMin - drag.startMin) * PX_PER_MINUTE,
                      }}
                    />
                  )}
                  {timed.map((ev) => {
                    // When this event is being dragged, render at the tentative
                    // position; otherwise at its stored start/end.
                    const active = eventDrag && eventDrag.ev.uid === ev.uid
                    const startMin = active ? eventDrag!.startMin : eventTopMinutes(ev, day)
                    const endMin = active ? eventDrag!.endMin : startMin + eventHeightMinutes(ev, day)
                    const top = startMin * PX_PER_MINUTE - offset
                    const height = Math.max(15, (endMin - startMin) * PX_PER_MINUTE)
                    if (top + height <= 0 || top >= gridHeight) return null // event in a hidden hour
                    return (
                      <div
                        key={ev.uid}
                        role="button"
                        tabIndex={0}
                        className={`absolute left-0.5 right-0.5 cursor-move overflow-hidden rounded bg-primary/20 px-1 py-0.5 text-left text-[11px] hover:bg-primary/30 ${active ? "ring-2 ring-primary" : ""}`}
                        style={{ top: Math.max(0, top), height, borderLeft: `3px solid ${props.eventColor(ev) ?? "hsl(var(--primary))"}` }}
                        // A click (mousedown+up without a move) opens the editor;
                        // a drag moves the event. stopPropagation keeps the day
                        // column's select-drag from firing under the event box.
                        onMouseDown={(e) => {
                          e.stopPropagation()
                          const origStart = eventTopMinutes(ev, day)
                          const origEnd = origStart + eventHeightMinutes(ev, day)
                          setEventDrag({ dayIdx, ev, mode: "move", startMin: origStart, endMin: origEnd, durationMin: origEnd - origStart })
                          // Record the down position so the click handler can tell
                          // a no-move click from a drag.
                          ;(e.currentTarget as HTMLElement).dataset.downX = String(e.clientX)
                          ;(e.currentTarget as HTMLElement).dataset.downY = String(e.clientY)
                        }}
                        onClick={(e) => {
                          const el = e.currentTarget
                          const dx = Math.abs(e.clientX - Number(el.dataset.downX ?? e.clientX))
                          const dy = Math.abs(e.clientY - Number(el.dataset.downY ?? e.clientY))
                          if (dx < 4 && dy < 4) props.onOpenEvent(ev) // a click, not a drag
                        }}
                        title={`${ev.summary} ${timeLabel(t, ev)}`}
                      >
                        <div className="truncate font-medium">{ev.summary}</div>
                        <div className="truncate text-muted-foreground">
                          {new Date(ev.start).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))}
                        </div>
                        {/* Resize handle: drag the bottom edge to change the end. */}
                        <div
                          className="absolute bottom-0 left-0 right-0 h-1.5 cursor-ns-resize bg-primary/40"
                          onMouseDown={(e) => {
                            e.stopPropagation()
                            const origStart = eventTopMinutes(ev, day)
                            const origEnd = origStart + eventHeightMinutes(ev, day)
                            setEventDrag({ dayIdx, ev, mode: "resize", startMin: origStart, endMin: origEnd, durationMin: origEnd - origStart })
                          }}
                        />
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </div>
  )
}
