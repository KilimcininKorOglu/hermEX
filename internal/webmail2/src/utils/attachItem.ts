// attachItem serializes calendar-adjacent items (tasks, notes) so the compose
// view can embed them as attachments (reference attachitem). Calendar events
// are exported by the backend (oxcical, faithful to VTIMEZONE/recurrence); only
// tasks and notes are serialized here, where no backend export exists.
import { Task, Note } from "@/utils/api"

// escapeICalText escapes a value for an iCalendar text field per RFC 5545:
// backslash, semicolon, and comma are escaped, and newlines become "\n".
export function escapeICalText(s: string): string {
  return s
    .replace(/\\/g, "\\\\")
    .replace(/;/g, "\\;")
    .replace(/,/g, "\\,")
    .replace(/\r\n|\r|\n/g, "\\n")
}

// icalDateTime renders an RFC3339 or YYYY-MM-DD value as an iCalendar UTC
// timestamp (…Z) for date-times, or a DATE value for a bare calendar date.
export function icalDateTime(v: string): { value: string; isDate: boolean } {
  const bareDate = /^\d{4}-\d{2}-\d{2}$/.test(v.trim())
  if (bareDate) {
    return { value: v.replace(/-/g, ""), isDate: true }
  }
  const d = new Date(v)
  if (isNaN(d.getTime())) return { value: "", isDate: false }
  const z = d.toISOString().replace(/[-:]/g, "").replace(/\.\d{3}Z$/, "Z")
  return { value: z, isDate: false }
}

// taskStatus maps the numeric task status onto an iCalendar VTODO STATUS.
function taskStatus(t: Task): string {
  if (t.completed || t.status === 2) return "COMPLETED"
  if (t.status === 1) return "IN-PROCESS"
  if (t.status === 3 || t.status === 4) return "NEEDS-ACTION"
  return "NEEDS-ACTION"
}

// taskPriority maps 0=low/1=normal/2=high onto iCalendar's 1..9 PRIORITY
// (1 highest, 5 normal, 9 lowest); 0 (undefined) is emitted as unset.
function taskPriority(t: Task): number {
  switch (t.priority) {
    case 2:
      return 1
    case 0:
      return 9
    default:
      return 5
  }
}

// taskToVTodo renders a task as a single-VTODO VCALENDAR document.
export function taskToVTodo(t: Task): string {
  const lines: string[] = [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//hermEX//webmail2//EN",
    "BEGIN:VTODO",
    `UID:${escapeICalText(t.uid || "task")}`,
    `SUMMARY:${escapeICalText(t.summary || "")}`,
  ]
  if (t.description) lines.push(`DESCRIPTION:${escapeICalText(t.description)}`)
  if (t.start) {
    const s = icalDateTime(t.start)
    if (s.value) lines.push(s.isDate ? `DTSTART;VALUE=DATE:${s.value}` : `DTSTART:${s.value}`)
  }
  if (t.due) {
    const d = icalDateTime(t.due)
    if (d.value) lines.push(d.isDate ? `DUE;VALUE=DATE:${d.value}` : `DUE:${d.value}`)
  }
  lines.push(`STATUS:${taskStatus(t)}`)
  lines.push(`PRIORITY:${taskPriority(t)}`)
  if (typeof t.percent === "number") lines.push(`PERCENT-COMPLETE:${Math.max(0, Math.min(100, t.percent))}`)
  if (t.recurrence) lines.push(`RRULE:${t.recurrence}`)
  if (t.categories && t.categories.length) lines.push(`CATEGORIES:${t.categories.map(escapeICalText).join(",")}`)
  lines.push("END:VTODO", "END:VCALENDAR")
  return lines.join("\r\n") + "\r\n"
}

// noteToText renders a note as a plain-text document: the title, a blank line,
// then the body. A note with no title falls back to just the body.
export function noteToText(n: Note): string {
  const title = (n.title || "").trim()
  const body = n.body || ""
  return title ? `${title}\r\n\r\n${body}` : body
}

// safeItemName strips a display label down to a filesystem/header-safe base.
export function safeItemName(s: string, fallback: string): string {
  const base = (s || "").trim().replace(/[^\w.\- ]+/g, "_").slice(0, 60).replace(/^[_.]+|[_.]+$/g, "")
  return base || fallback
}
