import { useCallback, useEffect, useState } from "react"
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
import api, { type Reminder } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"

// ReminderOverlay is the reminder popup (the reference ReminderStore + ReminderDialog):
// it polls the server's due-reminder list, and snoozes/dismisses each reminder
// server-side (persistent PidLidReminderSet/Time mutations) rather than in session
// memory. It is mounted once in the Layout so reminders surface on every page.
export function ReminderOverlay() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [due, setDue] = useState<Reminder[]>([])
  const [open, setOpen] = useState(false)
  const [snoozeChoice, setSnoozeChoice] = useState("5") // minutes, default 5 min

  // refresh reloads the server's due-reminder list. The server already filters to
  // fired-and-not-dismissed reminders (dismiss clears the flag, snooze advances the
  // time), so the client keeps no session state. Best-effort: a failed poll retries.
  const refresh = useCallback(async () => {
    try {
      const res = await api.getReminders()
      const list = res.reminders ?? []
      setDue(list)
      setOpen(list.length > 0)
    } catch {
      /* best-effort: a failed reminder poll silently retries next tick */
    }
  }, [])

  useEffect(() => {
    void refresh()
    const id = setInterval(() => void refresh(), 60 * 1000)
    return () => clearInterval(id)
  }, [refresh])

  // snoozeAll postpones every shown reminder by the chosen minutes, then reloads so
  // they drop off the list until the snooze window elapses.
  const snoozeAll = async () => {
    const minutes = Number(snoozeChoice) || 5
    await Promise.all(due.map((rem) => api.snoozeReminder(rem.id, minutes).catch(() => undefined)))
    await refresh()
  }

  // dismissOne / dismissAll clear reminders server-side so they never fire again.
  const dismissOne = async (id: string) => {
    await api.dismissReminder(id).catch(() => undefined)
    await refresh()
  }
  const dismissAll = async () => {
    await Promise.all(due.map((rem) => api.dismissReminder(rem.id).catch(() => undefined)))
    await refresh()
  }

  // openItem navigates to the calendar or task list depending on the reminder type.
  const openItem = (rem: Reminder) => {
    setOpen(false)
    navigate(rem.type === "task" ? "/tasks" : "/calendar")
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Bell className="h-5 w-5" />
            {t("reminder.title")}
          </DialogTitle>
          <DialogDescription>{t("reminder.description")}</DialogDescription>
        </DialogHeader>
        <ul className="space-y-2 py-2">
          {due.map((rem) => (
            <li key={rem.id} className="flex items-start justify-between gap-2 rounded-lg border p-3">
              <div className="min-w-0">
                <p className="truncate font-medium">{rem.subject}</p>
                {rem.start && (
                  <p className="flex items-center gap-1 text-sm text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    {new Date(rem.start).toLocaleString()}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 gap-1">
                <Button variant="ghost" size="sm" onClick={() => openItem(rem)}>
                  {t("reminder.open")}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => void dismissOne(rem.id)}>
                  {t("reminder.dismiss")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={snoozeChoice} onValueChange={setSnoozeChoice}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="5">5 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="10">10 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="15">15 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="30">30 {t("reminder.minutes")}</SelectItem>
              <SelectItem value="60">60 {t("reminder.minutes")}</SelectItem>
            </SelectContent>
          </Select>
          <Button variant="outline" onClick={() => void snoozeAll()}>
            {t("reminder.snooze")}
          </Button>
          <Button onClick={() => void dismissAll()} className="ml-auto">
            {t("reminder.dismissAll")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
