import type { Task, TaskInput } from "@/utils/api"
import { addDaysToKey, weekdayOfKey, zonedDayKey } from "@/utils/date"

// TaskForm is the task edit dialog's state: every optional field is filled so
// the controlled inputs never switch between controlled and uncontrolled.
export interface TaskForm {
  summary: string
  start: string
  due: string
  status: number
  percent: number
  priority: number
  reminder: boolean
  categories: string[]
  recurrence: string
  owner: string
  description: string
}

export const emptyTaskForm: TaskForm = { summary: "", start: "", due: "", status: 0, percent: 0, priority: 1, reminder: false, categories: [], recurrence: "", owner: "", description: "" }

// taskFormOf fills the edit form from a stored task, with the defaults for an
// absent field (priority 1 is Normal).
export function taskFormOf(task: Task): TaskForm {
  const form: TaskForm = { ...emptyTaskForm, categories: [] }
  for (const key of Object.keys(emptyTaskForm) as (keyof TaskForm)[]) {
    const value = task[key]
    if (value !== undefined && value !== null) Object.assign(form, { [key]: value })
  }
  return form
}

// taskInputOf builds the update payload from the edit form. Empty text fields
// are sent as absent, and completed carries the task's current state, which
// the form does not edit.
export function taskInputOf(form: TaskForm, completed: boolean): TaskInput {
  return {
    summary: form.summary.trim(),
    start: form.start || undefined,
    due: form.due || undefined,
    status: form.status,
    percent: form.percent,
    priority: form.priority,
    reminder: form.reminder,
    categories: form.categories.length > 0 ? form.categories : undefined,
    recurrence: form.recurrence || undefined,
    description: form.description || undefined,
    owner: form.owner || undefined,
    completed,
  }
}

// QuickDue is the day each quick flag of the task list sets, as YYYY-MM-DD.
export interface QuickDue {
  today: string
  tomorrow: string
  thisWeek: string
  nextWeek: string
}

// quickDueDays finds the quick-flag days from the day it is now in the display
// zone, the zone the page shows every time in: this week is the coming Friday
// (today on a Friday), next week the following Monday.
export function quickDueDays(now: Date = new Date()): QuickDue {
  const today = zonedDayKey(now)
  const weekday = weekdayOfKey(today)
  const toMonday = weekday === 1 ? 1 : (8 - weekday) % 7
  return {
    today,
    tomorrow: addDaysToKey(today, 1),
    thisWeek: addDaysToKey(today, (5 - weekday + 7) % 7),
    nextWeek: addDaysToKey(today, toMonday === 0 ? 7 : toMonday),
  }
}

// dateInputValue trims an RFC 3339 value to the YYYY-MM-DD a date input takes.
export function dateInputValue(value: string): string {
  return value.length >= 10 ? value.slice(0, 10) : value
}
