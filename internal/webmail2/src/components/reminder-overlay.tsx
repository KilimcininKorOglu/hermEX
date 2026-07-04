import { useEffect, useState } from "react"
import { useNavigate } from "react-router-dom"
import { Bell, Clock } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import api, { type CalendarEvent } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"

// DueReminder is a calendar event whose reminder has fired: the reminder lead
// time has elapsed (start - reminderMinutes <= now) but the event has not ended
// long ago (start + 1h, after which a popup is noise).
type DueReminder = CalendarEvent

// reminderDueAt returns the instant the reminder should fire (start minus the
// lead time), or undefined when the event carries no reminder.
function reminderDueAt(ev: CalendarEvent): number | undefined {
  if (!ev.reminderMinutes || ev.reminderMinutes <= 0) return undefined
  const start = new Date(ev.start).getTime()
  if (isNaN(start)) return undefined
  return start - ev.reminderMinutes * 60 * 1000
}

// ReminderOverlay is the calendar reminder engine (the ReminderStore + popup): it
// polls the calendar, fires a snooze/dismiss dialog the instant a reminder is due,
// and keeps dismissed/snoozed state in memory for the session. It is mounted once
// in the Layout so reminders surface on every page, not only the calendar.
export function ReminderOverlay() {
  const { t } = useI18n()
  const navigate = useNavigate()
  // dismissed holds event uids the user dismissed this session; snoozed holds a
  // uid -> instant-to-recheck map (the reminder re-fires after the snooze window).
  const [dismissed] = useState<Set<string>>(new Set())
  const [snoozed, setSnoozed] = useState<Record<string, number>>({})
  const [due, setDue] = useState<DueReminder[]>([])
  const [open, setOpen] = useState(false)
  const [snoozeChoice, setSnoozeChoice] = useState("300") // seconds, default 5 min

  useEffect(() => {
    let cancelled = false
    // check loads the calendar, finds newly-due reminders not yet dismissed or
    // snoozed-past-now, and surfaces them. It runs immediately and every minute.
    const check = async () => {
      const now = Date.now()
      try {
        const res = await api.getCalendarEvents()
        const events = (res.events ?? []).filter((ev) => {
          const dueAt = reminderDueAt(ev)
          if (dueAt === undefined) return false
          const start = new Date(ev.start).getTime()
          if (isNaN(start)) return false
          if (dueAt > now) return false // not yet time to remind
          if (now - start > 60 * 60 * 1000) return false // event started >1h ago
          if (dismissed.has(ev.uid)) return false
          const snoozeUntil = snoozed[ev.uid]
          if (snoozeUntil !== undefined && snoozeUntil > now) return false
          return true
        })
        if (cancelled) return
        if (events.length > 0) {
          setDue(events)
          setOpen(true)
        }
      } catch {
        /* best-effort: a failed calendar poll silently retries next tick */
      }
    }
    void check()
    const id = setInterval(check, 60 * 1000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [dismissed, snoozed])

  const snooze = () => {
    const secs = Number(snoozeChoice) || 300
    const until = Date.now() + secs * 1000
    setSnoozed((prev) => {
      const next = { ...prev }
      for (const ev of due) next[ev.uid] = until
      return next
    })
    setOpen(false)
  }

  const dismiss = () => {
    for (const ev of due) dismissed.add(ev.uid)
    setOpen(false)
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) dismiss() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Bell className="h-5 w-5" />
            {t("reminder.title")}
          </DialogTitle>
          <DialogDescription>{t("reminder.description")}</DialogDescription>
        </DialogHeader>
        <ul className="space-y-2 py-2">
          {due.map((ev) => (
            <li key={ev.uid} className="rounded-lg border p-3">
              <p className="font-medium">{ev.summary}</p>
              <p className="flex items-center gap-1 text-sm text-muted-foreground">
                <Clock className="h-3.5 w-3.5" />
                {new Date(ev.start).toLocaleString()}
              </p>
              {ev.location && (
                <p className="text-sm text-muted-foreground">{ev.location}</p>
              )}
            </li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={snoozeChoice} onValueChange={setSnoozeChoice}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="300">5 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="600">10 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="900">15 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="1800">30 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="3600">60 {t("reminder.minutes")}</SelectItem>
            </SelectContent>
          </Select>
          <Button variant="outline" onClick={snooze}>
            {t("reminder.snooze")}
          </Button>
          <Button variant="outline" onClick={() => { setOpen(false); navigate("/calendar") }}>
            {t("reminder.open")}
          </Button>
          <Button onClick={dismiss} className="ml-auto">
            {t("reminder.dismiss")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
