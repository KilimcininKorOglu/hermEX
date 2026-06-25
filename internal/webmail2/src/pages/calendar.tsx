import { useState, useEffect, useCallback } from "react"
import { CalendarDays, Plus, MapPin, Clock, Edit, Trash2, MoreHorizontal, Users, Repeat, List, LayoutGrid, ChevronLeft, ChevronRight, Settings2, Share2, X } from "lucide-react"
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
import { withTz, getDisplayTimeZone } from "@/utils/date"
import { detectTimeZone } from "@/utils/timezone"
import api, { type Calendar, type CalendarEvent, type UserFreeBusy, type Room } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import { ShareFolderDialog } from "@/components/share-folder-dialog"

type TFunc = (key: string, params?: Record<string, string>) => string

// parseAttendees splits the stored comma/space-separated attendee string into a
// clean list of addresses.
function parseAttendees(s: string): string[] {
  return s
    .split(/[\s,;]+/)
    .map((a) => a.trim())
    .filter(Boolean)
}

// rfc3339ToLocalInput converts an RFC3339 instant to the value a
// datetime-local input expects ("YYYY-MM-DDTHH:mm" in local time).
function rfc3339ToLocalInput(value: string): string {
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

interface EventForm {
  summary: string
  start: string
  end: string
  allDay: boolean
  location: string
  description: string
  attendees: string
  recurrence: string // "" | DAILY | WEEKLY | MONTHLY | YEARLY
  calendarId: string // target calendar ("calendar" = default)
}

const emptyForm: EventForm = { summary: "", start: "", end: "", allDay: false, location: "", description: "", attendees: "", recurrence: "", calendarId: "calendar" }

// recurrenceToForm maps a stored RRULE value to the form's frequency selector.
function recurrenceToForm(rrule?: string): string {
  if (!rrule) return ""
  const m = /FREQ=([A-Z]+)/.exec(rrule)
  return m ? m[1] : ""
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

// monthMatrix returns the 42 days (6 weeks, Monday-first) that fill the grid for
// the month containing cursor, including trailing days from adjacent months.
function monthMatrix(cursor: Date): Date[] {
  const year = cursor.getFullYear()
  const month = cursor.getMonth()
  const first = new Date(year, month, 1)
  const offset = (first.getDay() + 6) % 7 // Monday = 0
  const days: Date[] = []
  for (let i = 0; i < 42; i++) {
    days.push(new Date(year, month, 1 - offset + i))
  }
  return days
}

export function CalendarPage() {
  const { t } = useI18n()
  const weekdayLabels = [
    t("calendar.weekdays.mon"),
    t("calendar.weekdays.tue"),
    t("calendar.weekdays.wed"),
    t("calendar.weekdays.thu"),
    t("calendar.weekdays.fri"),
    t("calendar.weekdays.sat"),
    t("calendar.weekdays.sun"),
  ]
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingUID, setEditingUID] = useState<string | null>(null)
  const [form, setForm] = useState<EventForm>(emptyForm)
  const [busy, setBusy] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<CalendarEvent | null>(null)
  const [rooms, setRooms] = useState<Room[]>([])

  // View toggle: agenda list vs. month grid. cursor is the displayed month.
  const [view, setView] = useState<"list" | "month">("list")
  const [cursor, setCursor] = useState(() => new Date())

  // Availability (free/busy) lookup.
  const [fbOpen, setFbOpen] = useState(false)
  const [fbEmails, setFbEmails] = useState("")
  const [fbDate, setFbDate] = useState(() => {
    const d = new Date()
    const pad = (n: number) => String(n).padStart(2, "0")
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  })
  const [fbLoading, setFbLoading] = useState(false)
  const [fbResults, setFbResults] = useState<UserFreeBusy[] | null>(null)

  // Multi-calendar state.
  const { user } = useAuth()
  const [calendars, setCalendars] = useState<Calendar[]>([])
  const [visibleCalendarIds, setVisibleCalendarIds] = useState<Set<string>>(new Set(["default"]))
  const [calDialogOpen, setCalDialogOpen] = useState(false)
  const [calDialogMode, setCalDialogMode] = useState<"create" | "edit">("create")
  const [calDialogCal, setCalDialogCal] = useState<Calendar | null>(null)
  const [calForm, setCalForm] = useState({ name: "", description: "", color: "#3b82f6" })
  const [calBusy, setCalBusy] = useState(false)
  const [deleteCalTarget, setDeleteCalTarget] = useState<Calendar | null>(null)
  const [shareDialogOpen, setShareDialogOpen] = useState(false)
  const [shareDialogCal, setShareDialogCal] = useState<Calendar | null>(null)

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

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getCalendarEvents()
      const list = (res.events ?? []).slice().sort((a, b) => a.start.localeCompare(b.start))
      setEvents(list)
    } catch {
      setEvents([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
    loadCalendars()
  }, [load, loadCalendars])

  // Load the organization's bookable rooms for the room picker.
  useEffect(() => {
    api.getRooms()
      .then((res) => setRooms(res.rooms ?? []))
      .catch(() => setRooms([]))
  }, [])

  const openCreate = () => {
    setEditingUID(null)
    setForm(emptyForm)
    setDialogOpen(true)
  }

  // openCreateOn opens the new-event dialog with the start prefilled to 09:00
  // on the clicked grid day.
  const openCreateOn = (day: Date) => {
    const start = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 9, 0)
    setEditingUID(null)
    setForm({ ...emptyForm, start: rfc3339ToLocalInput(start.toISOString()) })
    setDialogOpen(true)
  }

  const openEdit = (ev: CalendarEvent) => {
    setEditingUID(ev.uid)
    setForm({
      summary: ev.summary,
      start: ev.allDay ? ev.start.slice(0, 10) : rfc3339ToLocalInput(ev.start),
      end: ev.end ? (ev.allDay ? ev.end.slice(0, 10) : rfc3339ToLocalInput(ev.end)) : "",
      allDay: !!ev.allDay,
      location: ev.location ?? "",
      description: ev.description ?? "",
      attendees: (ev.attendees ?? []).join(", "),
      recurrence: recurrenceToForm(ev.recurrence),
      calendarId: ev.calendarId ?? "calendar",
    })
    setDialogOpen(true)
  }

  const submit = async () => {
    if (!form.summary.trim()) {
      toast.error(t("calendar.titleRequired"))
      return
    }
    if (!form.start) {
      toast.error(t("calendar.startRequired"))
      return
    }
    const attendees = form.attendees
      .split(/[\s,;]+/)
      .map((a) => a.trim())
      .filter(Boolean)
    const payload = {
      summary: form.summary.trim(),
      start: form.allDay ? form.start : localInputToRFC3339(form.start),
      end: form.end ? (form.allDay ? form.end : localInputToRFC3339(form.end)) : undefined,
      allDay: form.allDay || undefined,
      location: form.location || undefined,
      description: form.description || undefined,
      attendees: attendees.length > 0 ? attendees : undefined,
      recurrence: form.recurrence ? `FREQ=${form.recurrence}` : undefined,
      calendarId: form.calendarId || "calendar",
      // Anchor timed events to the user's zone so recurrences keep their wall
      // time across DST (stored as DTSTART;TZID + VTIMEZONE). All-day events stay
      // floating dates.
      timezone: form.allDay ? undefined : (getDisplayTimeZone() || detectTimeZone()),
    }
    setBusy(true)
    try {
      if (editingUID) {
        await api.updateCalendarEvent(editingUID, payload)
        toast.success(t("calendar.eventUpdated"))
      } else {
        const created = await api.createCalendarEvent(payload)
        const unbooked = (created as { unbookedRooms?: string[] }).unbookedRooms
        if (unbooked && unbooked.length > 0) {
          toast.warning(t("calendar.eventCreatedRoomsBusy", { rooms: unbooked.join(", ") }))
        } else {
          toast.success(t("calendar.eventCreated"))
        }
      }
      setDialogOpen(false)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("calendar.saveFailed"))
    } finally {
      setBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    setBusy(true)
    try {
      await api.deleteCalendarEvent(deleteTarget.uid)
      toast.success(t("calendar.eventDeleted"))
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("calendar.deleteFailed"))
    } finally {
      setBusy(false)
    }
  }

  const submitCalDialog = async () => {
    if (!calForm.name.trim()) {
      toast.error(t("calendar.calendarNameRequired"))
      return
    }
    setCalBusy(true)
    try {
      if (calDialogMode === "create") {
        await api.createCalendar({ name: calForm.name, description: calForm.description || undefined, color: calForm.color || "#3b82f6" })
        toast.success(t("calendar.calendarCreated"))
      } else if (calDialogCal) {
        await api.updateCalendar(calDialogCal.id, { name: calForm.name, description: calForm.description || undefined, color: calForm.color || "#3b82f6" })
        toast.success(t("calendar.calendarUpdated"))
      }
      setCalDialogOpen(false)
      await loadCalendars()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("calendar.calendarSaveFailed"))
    } finally {
      setCalBusy(false)
    }
  }

  const confirmDeleteCalendar = async () => {
    if (!deleteCalTarget || calBusy) return
    setCalBusy(true)
    try {
      await api.deleteCalendar(deleteCalTarget.id)
      toast.success(t("calendar.calendarDeleted"))
      setDeleteCalTarget(null)
      await loadCalendars()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("calendar.calendarDeleteFailed"))
    } finally {
      setCalBusy(false)
    }
  }

  const checkAvailability = async () => {
    const emails = fbEmails
      .split(/[\s,;]+/)
      .map((e) => e.trim())
      .filter(Boolean)
    if (emails.length === 0) {
      toast.error(t("calendar.enterEmail"))
      return
    }
    if (!fbDate) {
      toast.error(t("calendar.pickDate"))
      return
    }
    // Query the whole local day.
    const dayStart = new Date(`${fbDate}T00:00:00`)
    const dayEnd = new Date(dayStart.getTime() + 24 * 60 * 60 * 1000)
    setFbLoading(true)
    setFbResults(null)
    try {
      const res = await api.getFreeBusy(emails, dayStart.toISOString(), dayEnd.toISOString())
      setFbResults(res.freeBusy ?? [])
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("calendar.availabilityFailed"))
    } finally {
      setFbLoading(false)
    }
  }

  // Only show events from calendars the user has toggled visible (all by
  // default); when calendar metadata is unavailable, show everything.
  const shownEvents =
    calendars.length === 0
      ? events
      : events.filter((ev) => visibleCalendarIds.has(ev.calendarId ?? "calendar"))

  // Per-calendar color for an event, to tint it in the views; only when more than
  // one calendar exists (a single calendar needs no color distinction).
  const calColorById = new Map(calendars.map((c) => [c.id, c.color]))
  const eventColor = (ev: CalendarEvent): string | undefined =>
    calendars.length > 1 ? (calColorById.get(ev.calendarId ?? "calendar") ?? "#3b82f6") : undefined

  // Group sorted events by day for the agenda view.
  const groups: { day: string; items: CalendarEvent[] }[] = []
  for (const ev of shownEvents) {
    const key = dayKey(ev.start)
    const last = groups[groups.length - 1]
    if (last && last.day === key) last.items.push(ev)
    else groups.push({ day: key, items: [ev] })
  }

  // Bucket events by local day key for the month grid.
  const eventsByDay = new Map<string, CalendarEvent[]>()
  for (const ev of shownEvents) {
    const key = eventDayKey(ev)
    if (!key) continue
    const bucket = eventsByDay.get(key)
    if (bucket) bucket.push(ev)
    else eventsByDay.set(key, [ev])
  }

  const monthDays = monthMatrix(cursor)
  const todayKey = dateKey(new Date())
  const monthLabel = cursor.toLocaleDateString(undefined, { month: "long", year: "numeric" })

  // The attendee string holds both people and booked rooms; split them so the
  // event form shows rooms as their own chips instead of mixing resource
  // mailboxes into the people picker. The saved value keeps including rooms.
  const roomEmailSet = new Set(rooms.map((r) => r.email.toLowerCase()))
  const formAttendeeList = parseAttendees(form.attendees)
  const peopleAttendees = formAttendeeList.filter((e) => !roomEmailSet.has(e.toLowerCase()))
  const selectedRooms = rooms.filter((r) =>
    formAttendeeList.some((e) => e.toLowerCase() === r.email.toLowerCase()),
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <CalendarDays className="h-6 w-6 text-primary" />
          <h1 className="text-2xl font-bold">{t("nav.calendar")}</h1>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex rounded-md border">
            <Button
              variant={view === "list" ? "secondary" : "ghost"}
              size="sm"
              className="rounded-r-none"
              onClick={() => setView("list")}
            >
              <List className="mr-2 h-4 w-4" />
              {t("calendar.list")}
            </Button>
            <Button
              variant={view === "month" ? "secondary" : "ghost"}
              size="sm"
              className="rounded-l-none"
              onClick={() => setView("month")}
            >
              <LayoutGrid className="mr-2 h-4 w-4" />
              {t("calendar.month")}
            </Button>
          </div>
          <Button variant="outline" onClick={() => { setFbResults(null); setFbOpen(true) }}>
            <Users className="mr-2 h-4 w-4" />
            {t("calendar.availability")}
          </Button>
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            {t("calendar.newEvent")}
          </Button>
        </div>
      </div>

      {/* Calendar overlay sidebar: list of calendars with visibility toggles */}
      {calendars.length > 0 && (
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm text-muted-foreground">{t("calendar.calendars")}:</span>
          {calendars.map((cal) => {
            const visible = visibleCalendarIds.has(cal.id)
            return (
              <div
                key={cal.id}
                className={`flex items-center gap-1.5 rounded-full px-3 py-1 text-sm border transition-colors cursor-pointer ${
                  visible ? "opacity-100" : "opacity-50"
                }`}
                style={{ borderColor: cal.color ?? "#3b82f6", color: cal.color ?? "#3b82f6", backgroundColor: visible ? `${cal.color ?? "#3b82f6"}15` : "transparent" }}
                onClick={() => {
                  setVisibleCalendarIds((prev) => {
                    const next = new Set(prev)
                    if (next.has(cal.id)) next.delete(cal.id)
                    else next.add(cal.id)
                    return next
                  })
                }}
                title={cal.description}
              >
                <span
                  className="h-2 w-2 rounded-full shrink-0"
                  style={{ backgroundColor: cal.color ?? "#3b82f6" }}
                />
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
                    <DropdownMenuItem
                      onClick={() => {
                        setCalDialogMode("edit")
                        setCalDialogCal(cal)
                        setCalForm({ name: cal.name, description: cal.description ?? "", color: cal.color ?? "#3b82f6" })
                        setCalDialogOpen(true)
                      }}
                    >
                      <Edit className="mr-2 h-4 w-4" />
                      {t("common.edit")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onClick={() => {
                        setShareDialogCal(cal)
                        setShareDialogOpen(true)
                      }}
                    >
                      <Share2 className="mr-2 h-4 w-4" />
                      {t("share.dialogTitle")}
                    </DropdownMenuItem>
                    {!cal.isDefault && (
                      <DropdownMenuItem
                        className="text-destructive"
                        onClick={() => setDeleteCalTarget(cal)}
                      >
                        <Trash2 className="mr-2 h-4 w-4" />
                        {t("common.delete")}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            )
          })}
          <Button
            variant="ghost"
            size="sm"
            className="h-7 text-xs"
            onClick={() => {
              setCalDialogMode("create")
              setCalDialogCal(null)
              setCalForm({ name: "", description: "", color: "#3b82f6" })
              setCalDialogOpen(true)
            }}
          >
            <Plus className="h-3 w-3 mr-1" />
            {t("calendar.addCalendar")}
          </Button>
        </div>
      )}

      {loading ? (
        <p className="text-sm text-muted-foreground py-8 text-center">{t("common.loading")}</p>
      ) : view === "month" ? (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-lg font-semibold">{monthLabel}</h2>
            <div className="flex items-center gap-1">
              <Button variant="outline" size="sm" onClick={() => setCursor(new Date())}>
                {t("common.today")}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label={t("calendar.previousMonth")}
                onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() - 1, 1))}
              >
                <ChevronLeft className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label={t("calendar.nextMonth")}
                onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() + 1, 1))}
              >
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <div className="overflow-hidden rounded-lg border bg-card">
            <div className="grid grid-cols-7 border-b bg-muted/30 text-center text-xs font-medium text-muted-foreground">
              {weekdayLabels.map((label) => (
                <div key={label} className="py-2">{label}</div>
              ))}
            </div>
            <div className="grid grid-cols-7">
              {monthDays.map((day) => {
                const key = dateKey(day)
                const inMonth = day.getMonth() === cursor.getMonth()
                const isToday = key === todayKey
                const dayEvents = eventsByDay.get(key) ?? []
                return (
                  <div
                    key={key}
                    className={`min-h-24 cursor-pointer border-b border-r p-1 transition-colors last:border-r-0 hover:bg-accent/50 ${inMonth ? "" : "bg-muted/20 text-muted-foreground"}`}
                    onClick={() => openCreateOn(day)}
                  >
                    <div className="flex justify-end">
                      <span
                        className={`flex h-6 w-6 items-center justify-center rounded-full text-xs ${isToday ? "bg-primary font-semibold text-primary-foreground" : ""}`}
                      >
                        {day.getDate()}
                      </span>
                    </div>
                    <div className="mt-0.5 space-y-0.5">
                      {dayEvents.slice(0, 3).map((ev) => (
                        <button
                          key={ev.uid}
                          className="block w-full truncate rounded bg-primary/10 px-1 py-0.5 text-left text-xs text-foreground hover:bg-primary/20"
                          style={eventColor(ev) ? { borderLeft: `3px solid ${eventColor(ev)}` } : undefined}
                          onClick={(e) => { e.stopPropagation(); openEdit(ev) }}
                          title={ev.summary}
                        >
                          {!ev.allDay && (
                            <span className="mr-1 text-muted-foreground">
                              {new Date(ev.start).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))}
                            </span>
                          )}
                          {ev.summary}
                        </button>
                      ))}
                      {dayEvents.length > 3 && (
                        <p className="px-1 text-xs text-muted-foreground">{t("calendar.moreEvents", { count: String(dayEvents.length - 3) })}</p>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        </div>
      ) : events.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <CalendarDays className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-medium">{t("calendar.noEvents")}</h3>
          <p className="text-muted-foreground mt-1">{t("calendar.noEventsHint")}</p>
          <Button className="mt-4" onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            {t("calendar.newEvent")}
          </Button>
        </div>
      ) : (
        <div className="space-y-6">
          {groups.map((group) => (
            <div key={group.day}>
              <h2 className="mb-2 text-sm font-semibold text-muted-foreground">{group.day}</h2>
              <div className="rounded-lg border bg-card divide-y">
                {group.items.map((ev) => (
                  <div key={ev.uid} className="flex items-start gap-4 p-4 hover:bg-accent/50 transition-colors">
                    {eventColor(ev) && (
                      <div className="w-1 self-stretch rounded-full" style={{ backgroundColor: eventColor(ev) }} aria-hidden />
                    )}
                    <div className="flex w-24 shrink-0 items-center gap-1 text-sm text-muted-foreground">
                      <Clock className="h-3.5 w-3.5" />
                      {timeLabel(t, ev)}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="font-medium truncate">{ev.summary}</p>
                      {ev.location && (
                        <p className="flex items-center gap-1 text-sm text-muted-foreground">
                          <MapPin className="h-3.5 w-3.5" />
                          {ev.location}
                        </p>
                      )}
                      {ev.recurrence && (
                        <p className="flex items-center gap-1 text-sm text-muted-foreground">
                          <Repeat className="h-3.5 w-3.5" />
                          {recurrenceLabel(t, recurrenceToForm(ev.recurrence))}
                        </p>
                      )}
                      {ev.description && (
                        <p className="text-sm text-muted-foreground truncate">{ev.description}</p>
                      )}
                    </div>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon" className="h-8 w-8">
                          <MoreHorizontal className="h-4 w-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => openEdit(ev)}>
                          <Edit className="mr-2 h-4 w-4" />
                          {t("common.edit")}
                        </DropdownMenuItem>
                        <DropdownMenuItem className="text-destructive" onClick={() => setDeleteTarget(ev)}>
                          <Trash2 className="mr-2 h-4 w-4" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create / edit dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editingUID ? t("calendar.editEvent") : t("calendar.newEvent")}</DialogTitle>
            <DialogDescription>{t("calendar.dialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="ev-summary">{t("calendar.title")}</Label>
              <Input
                id="ev-summary"
                value={form.summary}
                onChange={(e) => setForm({ ...form, summary: e.target.value })}
                placeholder={t("calendar.titlePlaceholder")}
              />
            </div>
            {calendars.length > 1 && (
              <div className="space-y-2">
                <Label htmlFor="ev-calendar">{t("calendar.calendar")}</Label>
                <Select
                  value={form.calendarId}
                  onValueChange={(value) => setForm({ ...form, calendarId: value })}
                >
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
            )}
            <div className="flex items-center justify-between">
              <Label htmlFor="ev-allday">{t("calendar.allDay")}</Label>
              <Switch
                id="ev-allday"
                checked={form.allDay}
                onCheckedChange={(checked) => setForm({ ...form, allDay: checked })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-recurrence">{t("calendar.repeat")}</Label>
              <Select
                value={form.recurrence}
                onValueChange={(value) => setForm({ ...form, recurrence: value === "none" ? "" : value })}
              >
                <SelectTrigger id="ev-recurrence">
                  <SelectValue placeholder={t("calendar.recurrence.none")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{recurrenceLabel(t, "")}</SelectItem>
                  <SelectItem value="DAILY">{recurrenceLabel(t, "DAILY")}</SelectItem>
                  <SelectItem value="WEEKLY">{recurrenceLabel(t, "WEEKLY")}</SelectItem>
                  <SelectItem value="MONTHLY">{recurrenceLabel(t, "MONTHLY")}</SelectItem>
                  <SelectItem value="YEARLY">{recurrenceLabel(t, "YEARLY")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="ev-start">{t("calendar.start")}</Label>
                <Input
                  id="ev-start"
                  type={form.allDay ? "date" : "datetime-local"}
                  value={form.start}
                  onChange={(e) => setForm({ ...form, start: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="ev-end">{t("calendar.end")}</Label>
                <Input
                  id="ev-end"
                  type={form.allDay ? "date" : "datetime-local"}
                  value={form.end}
                  onChange={(e) => setForm({ ...form, end: e.target.value })}
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-location">{t("calendar.location")}</Label>
              <Input
                id="ev-location"
                value={form.location}
                onChange={(e) => setForm({ ...form, location: e.target.value })}
                placeholder={t("common.optional")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-desc">{t("calendar.description")}</Label>
              <Textarea
                id="ev-desc"
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
                rows={3}
                placeholder={t("common.optional")}
              />
            </div>
            <div className="space-y-2">
              <Label>{t("calendar.attendees")}</Label>
              <AttendeePicker
                value={peopleAttendees}
                onChange={(emails) =>
                  setForm({ ...form, attendees: [...emails, ...selectedRooms.map((r) => r.email)].join(", ") })
                }
                window={
                  form.start && !form.allDay
                    ? {
                        start: localInputToRFC3339(form.start),
                        end: form.end
                          ? localInputToRFC3339(form.end)
                          : new Date(new Date(form.start).getTime() + 60 * 60 * 1000).toISOString(),
                      }
                    : undefined
                }
              />
              <p className="text-xs text-muted-foreground">
                {t("calendar.attendeesHint")}
              </p>
            </div>
            {rooms.length > 0 && (
              <div className="space-y-2">
                <Label htmlFor="ev-room">{t("calendar.room")}</Label>
                <Select
                  value=""
                  onValueChange={(email) => {
                    const room = rooms.find((r) => r.email === email)
                    if (!room) return
                    setForm((prev) => {
                      const list = prev.attendees
                        .split(/[\s,;]+/)
                        .map((a) => a.trim())
                        .filter(Boolean)
                      if (!list.includes(room.email)) list.push(room.email)
                      return { ...prev, attendees: list.join(", "), location: prev.location || room.name }
                    })
                  }}
                >
                  <SelectTrigger id="ev-room">
                    <SelectValue placeholder={t("calendar.addRoom")} />
                  </SelectTrigger>
                  <SelectContent>
                    {rooms.map((room) => (
                      <SelectItem key={room.email} value={room.email}>
                        {room.name}
                        {room.capacity ? ` ${t("calendar.roomSeats", { count: String(room.capacity) })}` : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {selectedRooms.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {selectedRooms.map((room) => (
                      <span
                        key={room.email}
                        className="inline-flex items-center gap-1 rounded-md bg-secondary px-2 py-0.5 text-xs"
                      >
                        {room.name}
                        {room.capacity ? ` ${t("calendar.roomSeats", { count: String(room.capacity) })}` : ""}
                        <button
                          type="button"
                          className="text-muted-foreground hover:text-foreground"
                          aria-label={`${t("common.remove")} ${room.name}`}
                          onClick={() =>
                            setForm((prev) => ({
                              ...prev,
                              attendees: parseAttendees(prev.attendees)
                                .filter((e) => e.toLowerCase() !== room.email.toLowerCase())
                                .join(", "),
                            }))
                          }
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
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submit} disabled={busy}>
              {editingUID ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Availability (free/busy) lookup */}
      <Dialog open={fbOpen} onOpenChange={setFbOpen}>
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
                value={parseAttendees(fbEmails)}
                onChange={(emails) => setFbEmails(emails.join(", "))}
                placeholder={t("calendar.searchNameEmail")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fb-date">{t("common.date")}</Label>
              <Input
                id="fb-date"
                type="date"
                value={fbDate}
                onChange={(e) => setFbDate(e.target.value)}
              />
            </div>
            {fbResults && (
              <div className="space-y-3 rounded-lg border bg-muted/30 p-3">
                {fbResults.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("common.noResults")}</p>
                ) : (
                  fbResults.map((r) => (
                    <div key={r.user}>
                      <p className="text-sm font-medium">{r.user}</p>
                      {r.busy.length === 0 ? (
                        <p className="text-sm text-muted-foreground">{t("calendar.freeAllDay")}</p>
                      ) : (
                        <ul className="mt-1 space-y-0.5">
                          {r.busy.map((b, i) => (
                            <li key={i} className="flex items-center gap-1.5 text-sm text-muted-foreground">
                              <Clock className="h-3.5 w-3.5" />
                              {new Date(b.start).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))}
                              {" – "}
                              {new Date(b.end).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  ))
                )}
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFbOpen(false)} disabled={fbLoading}>
              {t("common.close")}
            </Button>
            <Button onClick={checkAvailability} disabled={fbLoading}>
              {fbLoading ? t("calendar.checking") : t("calendar.check")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("calendar.deleteEvent")}</DialogTitle>
            <DialogDescription>{t("calendar.deleteConfirm", { name: deleteTarget?.summary ?? "" })}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={confirmDelete} disabled={busy}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Calendar create/edit dialog */}
      <Dialog open={calDialogOpen} onOpenChange={setCalDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {calDialogMode === "create" ? t("calendar.newCalendar") : t("calendar.editCalendar")}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="cal-name">{t("calendar.calendarName")}</Label>
              <Input
                id="cal-name"
                value={calForm.name}
                onChange={(e) => setCalForm({ ...calForm, name: e.target.value })}
                placeholder={t("calendar.calendarNamePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="cal-desc">{t("calendar.description")}</Label>
              <Input
                id="cal-desc"
                value={calForm.description}
                onChange={(e) => setCalForm({ ...calForm, description: e.target.value })}
                placeholder={t("common.optional")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="cal-color">{t("calendar.color")}</Label>
              <div className="flex items-center gap-3">
                <input
                  id="cal-color"
                  type="color"
                  value={calForm.color}
                  onChange={(e) => setCalForm({ ...calForm, color: e.target.value })}
                  className="h-9 w-14 rounded border cursor-pointer p-0.5"
                />
                <Input
                  value={calForm.color}
                  onChange={(e) => setCalForm({ ...calForm, color: e.target.value })}
                  placeholder="#3b82f6"
                  className="font-mono"
                />
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCalDialogOpen(false)} disabled={calBusy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submitCalDialog} disabled={calBusy}>
              {calDialogMode === "create" ? t("common.create") : t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Calendar delete confirmation */}
      <Dialog open={deleteCalTarget !== null} onOpenChange={(open) => { if (!open) setDeleteCalTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("calendar.deleteCalendar")}</DialogTitle>
            <DialogDescription>
              {t("calendar.deleteCalendarConfirm", { name: deleteCalTarget?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteCalTarget(null)} disabled={calBusy}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={confirmDeleteCalendar} disabled={calBusy}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Calendar sharing dialog */}
      {shareDialogCal && (
        <ShareFolderDialog
          open={shareDialogOpen}
          onOpenChange={(open) => {
            setShareDialogOpen(open)
            if (!open) setShareDialogCal(null)
          }}
          folderName={shareDialogCal.id}
          folderLabel={shareDialogCal.name}
          owner={user?.email ?? ""}
          isOwner={true}
        />
      )}
    </div>
  )
}
