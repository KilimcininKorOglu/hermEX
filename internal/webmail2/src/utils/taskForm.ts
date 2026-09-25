import type { Task, TaskInput } from "@/utils/api"

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
  return {
    summary: task.summary,
    start: task.start ?? "",
    due: task.due ?? "",
    status: task.status ?? 0,
    percent: task.percent ?? 0,
    priority: task.priority ?? 1,
    reminder: task.reminder ?? false,
    categories: task.categories ?? [],
    recurrence: task.recurrence ?? "",
    owner: task.owner ?? "",
    description: task.description ?? "",
  }
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

// dateInputValue trims an RFC 3339 value to the YYYY-MM-DD a date input takes.
export function dateInputValue(value: string): string {
  return value.length >= 10 ? value.slice(0, 10) : value
}
