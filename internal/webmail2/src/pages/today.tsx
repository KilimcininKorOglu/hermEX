import { useEffect, useState } from "react"
import { useNavigate } from "react-router-dom"
import { CalendarDays, Mail, ListTodo, Plus, Clock, StickyNote, Users } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import api, { type CalendarEvent, type Task, type Contact } from "@/utils/api"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"
import { withTz } from "@/utils/date"

// dateKey returns a local YYYY-MM-DD key for a Date.
function dateKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// eventDayKey returns the local day key an event belongs to (all-day uses the
// date-only start; timed uses the RFC3339 instant).
function eventDayKey(ev: CalendarEvent): string {
  const raw = ev.allDay && ev.start.length === 10 ? `${ev.start}T00:00:00` : ev.start
  const d = new Date(raw)
  return isNaN(d.getTime()) ? "" : dateKey(d)
}

// TodayPage is the dashboard: a configurable set of widgets summarizing the
// user's day - today's appointments (and a quick-add), the inbox unread count,
// and outstanding tasks. It is the home the calendar's AppointmentsWidget and
// QuickAppointmentWidget live in (plan Q.1/Q.5).
export function TodayPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const { inboxUnread } = useMailbox()
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [quickSummary, setQuickSummary] = useState("")
  const [quickTime, setQuickTime] = useState("09:00")
  const [quickBusy, setQuickBusy] = useState(false)
  // QuickNoteWidget + QuickContactWidget quick-create state.
  const [quickNote, setQuickNote] = useState("")
  const [quickContactName, setQuickContactName] = useState("")
  const [quickContactEmail, setQuickContactEmail] = useState("")
  const [recentContacts, setRecentContacts] = useState<Contact[]>([])
  // hideWidgets mirrors the DB-backed "Hide widget panel" display setting
  // (reference zarafa/v1/widgets/sidebar/hide_widgetpanel): when on, the
  // interactive quick-create widgets are hidden and only the read-only day
  // summaries remain.
  const [hideWidgets, setHideWidgets] = useState(false)

  useEffect(() => {
    api.getCalendarEvents().then((res) => setEvents(res.events ?? [])).catch(() => setEvents([]))
    api.getTasks().then((res) => setTasks(res.tasks ?? [])).catch(() => setTasks([]))
    api.getContacts().then((res) => setRecentContacts((res.contacts ?? []).slice(0, 5))).catch(() => setRecentContacts([]))
    api.getAppearanceSettings().then((s) => setHideWidgets(s.hideWidgetPanel)).catch(() => undefined)
  }, [])

  const todayKey = dateKey(new Date())
  const todayEvents = events.filter((ev) => eventDayKey(ev) === todayKey).sort((a, b) => a.start.localeCompare(b.start))
  const openTasks = tasks.filter((tk) => !tk.completed).slice(0, 8)

  const quickAdd = async () => {
    if (!quickSummary.trim()) return
    setQuickBusy(true)
    try {
      const d = new Date(`${todayKey}T${quickTime}:00`)
      const end = new Date(d.getTime() + 60 * 60 * 1000)
      await api.createCalendarEvent({
        summary: quickSummary.trim(),
        start: d.toISOString(),
        end: end.toISOString(),
        calendarId: "calendar",
      })
      setQuickSummary("")
      const res = await api.getCalendarEvents()
      setEvents(res.events ?? [])
    } catch {
      /* best-effort */
    } finally {
      setQuickBusy(false)
    }
  }

  // QuickNoteWidget: capture a one-line note without leaving the dashboard.
  const quickAddNote = async () => {
    const body = quickNote.trim()
    if (!body) return
    try {
      await api.createNote({ title: body, body: "" })
      setQuickNote("")
    } catch {
      /* best-effort */
    }
  }

  // QuickContactWidget: add a name + email contact in two fields.
  const quickAddContact = async () => {
    const name = quickContactName.trim()
    if (!name) return
    try {
      await api.createContact({ name, email: quickContactEmail.trim(), is_group: false })
      setQuickContactName("")
      setQuickContactEmail("")
    } catch {
      /* best-effort */
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <CalendarDays className="h-6 w-6 text-primary" />
        <h1 className="text-2xl font-bold">{t("nav.today")}</h1>
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        {/* AppointmentsWidget: today's appointments. */}
        <section className="rounded-lg border bg-card p-4">
          <div className="flex items-center justify-between">
            <h2 className="flex items-center gap-2 font-medium">
              <CalendarDays className="h-4 w-4" /> {t("today.appointments")}
            </h2>
            <Button variant="ghost" size="sm" onClick={() => navigate("/calendar")}>
              {t("today.openCalendar")}
            </Button>
          </div>
          {todayEvents.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">{t("today.noAppointments")}</p>
          ) : (
            <ul className="mt-3 space-y-2">
              {todayEvents.map((ev) => (
                <li key={ev.uid} className="flex items-start gap-2 rounded-md border p-2">
                  <Clock className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0">
                    <p className="truncate font-medium">{ev.summary}</p>
                    <p className="text-xs text-muted-foreground">
                      {ev.allDay
                        ? t("calendar.allDay")
                        : new Date(ev.start).toLocaleTimeString(undefined, withTz({ hour: "2-digit", minute: "2-digit" }))}
                    </p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        {/* QuickAppointmentWidget: quick-add an appointment for today. */}
        {!hideWidgets && (
        <section className="rounded-lg border bg-card p-4">
          <h2 className="flex items-center gap-2 font-medium">
            <Plus className="h-4 w-4" /> {t("today.quickAppointment")}
          </h2>
          <div className="mt-3 space-y-2">
            <div className="space-y-1">
              <Label htmlFor="qa-summary">{t("calendar.title")}</Label>
              <Input
                id="qa-summary"
                value={quickSummary}
                onChange={(e) => setQuickSummary(e.target.value)}
                placeholder={t("calendar.titlePlaceholder")}
                onKeyDown={(e) => { if (e.key === "Enter") void quickAdd() }}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="qa-time">{t("calendar.start")}</Label>
              <Input
                id="qa-time"
                type="time"
                value={quickTime}
                onChange={(e) => setQuickTime(e.target.value)}
              />
            </div>
            <Button onClick={quickAdd} disabled={quickBusy || !quickSummary.trim()} className="w-full">
              <Plus className="mr-2 h-4 w-4" />
              {t("common.create")}
            </Button>
          </div>
        </section>
        )}

        {/* MailWidget: inbox unread count + jump-in. */}
        <section className="rounded-lg border bg-card p-4">
          <div className="flex items-center justify-between">
            <h2 className="flex items-center gap-2 font-medium">
              <Mail className="h-4 w-4" /> {t("nav.inbox")}
            </h2>
            <Button variant="ghost" size="sm" onClick={() => navigate("/inbox")}>
              {t("today.openInbox")}
            </Button>
          </div>
          <p className="mt-3 text-sm">
            {inboxUnread > 0
              ? t("today.unreadMail", { count: String(inboxUnread) })
              : t("today.noUnread")}
          </p>
        </section>

        {/* TasksWidget: outstanding tasks. */}
        <section className="rounded-lg border bg-card p-4">
          <div className="flex items-center justify-between">
            <h2 className="flex items-center gap-2 font-medium">
              <ListTodo className="h-4 w-4" /> {t("nav.tasks")}
            </h2>
            <Button variant="ghost" size="sm" onClick={() => navigate("/tasks")}>
              {t("today.openTasks")}
            </Button>
          </div>
          {openTasks.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">{t("today.noTasks")}</p>
          ) : (
            <ul className="mt-3 space-y-1.5">
              {openTasks.map((tk) => (
                <li key={tk.uid} className="flex items-center gap-2 text-sm">
                  <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                  <span className="truncate">{tk.summary}</span>
                  {tk.due && (
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      {new Date(tk.due).toLocaleDateString()}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>

        {/* QuickNoteWidget: capture a note in one line. */}
        {!hideWidgets && (
        <section className="rounded-lg border bg-card p-4">
          <div className="flex items-center justify-between">
            <h2 className="flex items-center gap-2 font-medium">
              <StickyNote className="h-4 w-4" /> {t("today.quickNote")}
            </h2>
            <Button variant="ghost" size="sm" onClick={() => navigate("/notes")}>
              {t("today.openNotes")}
            </Button>
          </div>
          <form
            className="mt-3 flex gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void quickAddNote()
            }}
          >
            <Input
              value={quickNote}
              onChange={(e) => setQuickNote(e.target.value)}
              placeholder={t("today.quickNotePlaceholder")}
            />
            <Button type="submit" size="sm" disabled={!quickNote.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              {t("common.add")}
            </Button>
          </form>
        </section>
        )}

        {/* QuickContactWidget: add a contact + recent contacts. */}
        {!hideWidgets && (
        <section className="rounded-lg border bg-card p-4">
          <div className="flex items-center justify-between">
            <h2 className="flex items-center gap-2 font-medium">
              <Users className="h-4 w-4" /> {t("today.quickContact")}
            </h2>
            <Button variant="ghost" size="sm" onClick={() => navigate("/contacts")}>
              {t("today.openContacts")}
            </Button>
          </div>
          <form
            className="mt-3 grid gap-2 sm:grid-cols-[1fr_1fr_auto]"
            onSubmit={(e) => {
              e.preventDefault()
              void quickAddContact()
            }}
          >
            <Input
              value={quickContactName}
              onChange={(e) => setQuickContactName(e.target.value)}
              placeholder={t("common.name")}
            />
            <Input
              value={quickContactEmail}
              onChange={(e) => setQuickContactEmail(e.target.value)}
              placeholder={t("common.email")}
            />
            <Button type="submit" size="sm" disabled={!quickContactName.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              {t("common.add")}
            </Button>
          </form>
          {recentContacts.length > 0 && (
            <ul className="mt-3 space-y-1.5">
              {recentContacts.map((c) => (
                <li key={c.id} className="flex items-center gap-2 text-sm">
                  <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                  <span className="truncate">{c.name}</span>
                  {c.email && <span className="ml-auto shrink-0 text-xs text-muted-foreground truncate">{c.email}</span>}
                </li>
              ))}
            </ul>
          )}
        </section>
        )}
      </div>
    </div>
  )
}