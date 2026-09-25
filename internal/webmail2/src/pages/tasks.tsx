import { useState, useEffect, useCallback } from "react"
import { ListTodo, Plus, Trash2, Edit, CalendarClock, Flag } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Checkbox } from "@/components/ui/checkbox"
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
import { toast } from "sonner"
import api, { type Task, type TaskInput } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"
import { useBusyGate } from "@/hooks/useBusyGate"
import { CategoryChips, toggledCategories, type CategoryOption } from "@/components/category-chips"
import { dateInputValue, emptyTaskForm, taskFormOf, taskInputOf, type TaskForm } from "@/utils/taskForm"

function dueLabel(due?: string): string {
  if (!due) return ""
  const d = new Date(due.length === 10 ? `${due}T00:00:00` : due)
  if (isNaN(d.getTime())) return due
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })
}

type Category = CategoryOption

// TaskFlagMenu quick-sets a task's due date, or clears it.
function TaskFlagMenu({ task, onFlag }: { task: Task; onFlag: (task: Task, when: string) => void }) {
  const { t } = useI18n()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("tasks.flag")}>
          <Flag className={task.due ? "h-4 w-4 text-primary" : "h-4 w-4"} />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={() => onFlag(task, ymd(new Date()))}>{t("tasks.flagToday")}</DropdownMenuItem>
        <DropdownMenuItem onClick={() => onFlag(task, ymd(addDays(new Date(), 1)))}>{t("tasks.flagTomorrow")}</DropdownMenuItem>
        <DropdownMenuItem onClick={() => onFlag(task, ymd(thisFriday()))}>{t("tasks.flagThisWeek")}</DropdownMenuItem>
        <DropdownMenuItem onClick={() => onFlag(task, ymd(nextMonday()))}>{t("tasks.flagNextWeek")}</DropdownMenuItem>
        {task.due && (
          <DropdownMenuItem className="text-destructive" onClick={() => onFlag(task, "")}>{t("tasks.flagNoDate")}</DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

interface TaskRowActions {
  onToggle: (task: Task) => void
  onFlag: (task: Task, when: string) => void
  onEdit: (task: Task) => void
  onDelete: (task: Task) => void
}

// TaskRow is one task with its completion box and its actions.
function TaskRow({ task, actions }: { task: Task; actions: TaskRowActions }) {
  return (
    <div className="flex items-start gap-3 p-3 hover:bg-accent/50 transition-colors">
      <Checkbox
        checked={task.completed}
        onCheckedChange={() => actions.onToggle(task)}
        className="mt-1"
      />
      <div className="flex-1 min-w-0">
        <p className={task.completed ? "font-medium line-through text-muted-foreground" : "font-medium"}>
          {task.summary}
        </p>
        {task.due && (
          <p className="flex items-center gap-1 text-xs text-muted-foreground">
            <CalendarClock className="h-3 w-3" />
            {dueLabel(task.due)}
          </p>
        )}
        {task.description && (
          <p className="text-sm text-muted-foreground truncate">{task.description}</p>
        )}
      </div>
      <TaskFlagMenu task={task} onFlag={actions.onFlag} />
      <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => actions.onEdit(task)}>
        <Edit className="h-4 w-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        className="h-8 w-8 text-destructive"
        onClick={() => actions.onDelete(task)}
      >
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
  )
}

// TaskList shows the loading notice, the empty state, or the visible tasks.
function TaskList({
  loading,
  tasks,
  hideCompleted,
  actions,
}: {
  loading: boolean
  tasks: Task[]
  hideCompleted: boolean
  actions: TaskRowActions
}) {
  const { t } = useI18n()
  if (loading) {
    return <p className="text-sm text-muted-foreground py-8 text-center">{t("common.loading")}</p>
  }
  const visible = tasks.filter((task) => !hideCompleted || !task.completed)
  if (visible.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-center">
        <div className="rounded-full bg-muted p-4">
          <ListTodo className="h-8 w-8 text-muted-foreground" />
        </div>
        <h3 className="mt-4 text-lg font-medium">{t("tasks.noTasks")}</h3>
        <p className="text-muted-foreground mt-1">{t("tasks.emptyHint")}</p>
      </div>
    )
  }
  return (
    <div className="rounded-lg border bg-card divide-y">
      {visible.map((task) => (
        <TaskRow key={task.uid} task={task} actions={actions} />
      ))}
    </div>
  )
}

// CategoryPicker toggles the master categories on the edited task.
function CategoryPicker({
  categories,
  selected,
  onToggle,
}: {
  categories: Category[]
  selected: string[]
  onToggle: (name: string, on: boolean) => void
}) {
  const { t } = useI18n()
  if (categories.length === 0) return null
  return (
    <div className="space-y-2">
      <Label>{t("tasks.categories")}</Label>
      <CategoryChips categories={categories} selected={selected} onToggle={onToggle} />
    </div>
  )
}

// AcceptStateNote shows an assigned task's acceptance state.
function AcceptStateNote({ task }: { task: Task | null }) {
  const { t } = useI18n()
  const state = task?.acceptState ?? 0
  if (!task?.owner || state <= 0) return null
  return (
    <p className="text-xs text-muted-foreground">
      {t("tasks.acceptState", { state: String(state) })}
    </p>
  )
}

// TaskEditDialog edits every field of one task.
function TaskEditDialog({
  editing,
  form,
  setForm,
  categories,
  busy,
  onClose,
  onSave,
}: {
  editing: Task | null
  form: TaskForm
  setForm: React.Dispatch<React.SetStateAction<TaskForm>>
  categories: Category[]
  busy: boolean
  onClose: () => void
  onSave: () => void
}) {
  const { t } = useI18n()
  const toggleCategory = (name: string, on: boolean) =>
    setForm((prev) => ({
      ...prev,
      categories: toggledCategories(prev.categories, name, on),
    }))
  return (
    <Dialog open={editing !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("tasks.editTask")}</DialogTitle>
          <DialogDescription>{t("tasks.caldavNote")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="task-summary">{t("tasks.title")}</Label>
            <Input
              id="task-summary"
              value={form.summary}
              onChange={(e) => setForm({ ...form, summary: e.target.value })}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="task-due">{t("tasks.dueDate")}</Label>
            <Input
              id="task-due"
              type="date"
              value={dateInputValue(form.due)}
              onChange={(e) => setForm({ ...form, due: e.target.value })}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="task-start">{t("tasks.startDate")}</Label>
            <Input
              id="task-start"
              type="date"
              value={dateInputValue(form.start)}
              onChange={(e) => setForm({ ...form, start: e.target.value })}
            />
          </div>
          <div className="flex items-center gap-6">
            <div className="space-y-2">
              <Label htmlFor="task-priority">{t("tasks.priority")}</Label>
              <select
                id="task-priority"
                className="h-9 rounded-md border border-input bg-transparent px-3 text-sm"
                value={form.priority}
                onChange={(e) => setForm({ ...form, priority: Number(e.target.value) })}
              >
                <option value={0}>{t("tasks.priorityLow")}</option>
                <option value={1}>{t("tasks.priorityNormal")}</option>
                <option value={2}>{t("tasks.priorityHigh")}</option>
              </select>
            </div>
            <label className="flex items-center gap-2 text-sm font-medium pt-6">
              <input
                type="checkbox"
                checked={form.reminder}
                onChange={(e) => setForm({ ...form, reminder: e.target.checked })}
              />
              {t("tasks.reminder")}
            </label>
          </div>
          <div className="flex items-center gap-6">
            <div className="space-y-2">
              <Label htmlFor="task-status">{t("tasks.status")}</Label>
              <select
                id="task-status"
                className="h-9 rounded-md border border-input bg-transparent px-3 text-sm"
                value={form.status}
                onChange={(e) => {
                  const s = Number(e.target.value)
                  setForm((prev) => ({ ...prev, status: s, percent: s === 2 ? 100 : prev.percent }))
                }}
              >
                <option value={0}>{t("tasks.statusNotStarted")}</option>
                <option value={1}>{t("tasks.statusInProgress")}</option>
                <option value={2}>{t("tasks.statusComplete")}</option>
                <option value={3}>{t("tasks.statusWaiting")}</option>
                <option value={4}>{t("tasks.statusDeferred")}</option>
              </select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="task-percent">{t("tasks.percent")}</Label>
              <Input
                id="task-percent"
                type="number"
                min={0}
                max={100}
                className="w-24"
                value={form.percent}
                onChange={(e) => setForm({ ...form, percent: Math.max(0, Math.min(100, Number(e.target.value) || 0)) })}
              />
            </div>
          </div>
          <CategoryPicker categories={categories} selected={form.categories} onToggle={toggleCategory} />
          <div className="space-y-2">
            <Label htmlFor="task-desc">{t("tasks.description")}</Label>
            <Textarea
              id="task-desc"
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              rows={3}
              placeholder={t("common.optional")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="task-recurrence">{t("tasks.recurrence")}</Label>
            <select
              id="task-recurrence"
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm"
              value={form.recurrence}
              onChange={(e) => setForm({ ...form, recurrence: e.target.value })}
            >
              <option value="">{t("tasks.recurrenceNone")}</option>
              <option value="FREQ=DAILY;INTERVAL=1">{t("tasks.recurrenceDaily")}</option>
              <option value="FREQ=WEEKLY;INTERVAL=1">{t("tasks.recurrenceWeekly")}</option>
              <option value="FREQ=MONTHLY;INTERVAL=1">{t("tasks.recurrenceMonthly")}</option>
              <option value="FREQ=YEARLY;INTERVAL=1">{t("tasks.recurrenceYearly")}</option>
            </select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="task-owner">{t("tasks.assignTo")}</Label>
            <Input
              id="task-owner"
              value={form.owner}
              onChange={(e) => setForm({ ...form, owner: e.target.value })}
              placeholder={t("tasks.assignToPlaceholder")}
            />
            <AcceptStateNote task={editing} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={onSave} disabled={busy}>
            {t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}


export function TasksPage() {
  const { t } = useI18n()
  const [tasks, setTasks] = useState<Task[]>([])
  const [allCategories, setAllCategories] = useState<Category[]>([])
  const [loading, setLoading] = useState(true)
  const [quickAdd, setQuickAdd] = useState("")
  // One mutation at a time: a button's disabled attribute is not the guard,
  // because a second click can arrive before React re-renders with the new state.
  const { busy, begin: beginMutation, end: endMutation } = useBusyGate()
  const [editing, setEditing] = useState<Task | null>(null)
  const [form, setForm] = useState<TaskForm>(emptyTaskForm)
  const [deleteTarget, setDeleteTarget] = useState<Task | null>(null)
  // hideCompleted is the to-do filter: when on, finished tasks drop out of the
  // list so the active work stays on screen (Outlook's To-Do view).
  const [hideCompleted, setHideCompleted] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getTasks()
      const list = (res.tasks ?? []).slice().sort((a, b) => {
        if (a.completed !== b.completed) return a.completed ? 1 : -1
        return (a.due || "~").localeCompare(b.due || "~")
      })
      setTasks(list)
    } catch {
      setTasks([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
    api.getCategories()
      .then((res) => setAllCategories(res.categories ?? []))
      .catch(() => setAllCategories([]))
  }, [load])

  const handleQuickAdd = async () => {
    const summary = quickAdd.trim()
    if (!summary) return
    if (!beginMutation()) return
    try {
      await api.createTask({ summary, completed: false })
      setQuickAdd("")
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.addFailed"))
    } finally {
      endMutation()
    }
  }

  const toggleComplete = async (task: Task) => {
    // Optimistic toggle, then persist the full task with the new state.
    setTasks((prev) => prev.map((t) => (t.uid === task.uid ? { ...t, completed: !t.completed } : t)))
    const payload: TaskInput = {
      summary: task.summary,
      start: task.start,
      due: task.due,
      status: task.status,
      percent: task.percent,
      priority: task.priority,
      reminder: task.reminder,
      categories: task.categories,
      recurrence: task.recurrence,
      description: task.description,
      owner: task.owner,
      completed: !task.completed,
    }
    try {
      await api.updateTask(task.uid, payload)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.updateFailed"))
      await load()
    }
  }

  // flagDue sets a task's due date to a quick target (today/tomorrow/this week's
  // Friday/next Monday) or clears it ("no date"), the TaskFlagsMenu quick-set.
  const flagDue = async (task: Task, when: string) => {
    try {
      await api.updateTask(task.uid, {
        summary: task.summary,
        start: task.start,
        due: when || undefined,
        status: task.status,
        percent: task.percent,
        priority: task.priority,
        reminder: task.reminder,
        categories: task.categories,
        description: task.description,
        owner: task.owner,
        completed: task.completed,
      })
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.updateFailed"))
      await load()
    }
  }

  const openEdit = (task: Task) => {
    setEditing(task)
    setForm(taskFormOf(task))
  }

  const submitEdit = async () => {
    if (!editing) return
    if (!form.summary.trim()) {
      toast.error(t("tasks.titleRequired"))
      return
    }
    if (!beginMutation()) return
    try {
      await api.updateTask(editing.uid, taskInputOf(form, editing.completed))
      toast.success(t("tasks.taskUpdated"))
      setEditing(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.saveFailed"))
    } finally {
      endMutation()
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    if (!beginMutation()) return
    try {
      await api.deleteTask(deleteTarget.uid)
      toast.success(t("tasks.taskDeleted"))
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.deleteFailed"))
    } finally {
      endMutation()
    }
  }

  return (
    <div className="space-y-4 max-w-2xl">
      <div className="flex items-center gap-2">
        <ListTodo className="h-6 w-6 text-primary" />
        <h1 className="text-2xl font-bold">{t("nav.tasks")}</h1>
      </div>

      <div className="flex items-center gap-2">
        <Input
          value={quickAdd}
          onChange={(e) => setQuickAdd(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void handleQuickAdd()
          }}
          placeholder={t("tasks.quickAddPlaceholder")}
        />
        <Button onClick={handleQuickAdd} disabled={busy}>
          <Plus className="mr-2 h-4 w-4" />
          {t("common.add")}
        </Button>
        <label className="ml-auto flex items-center gap-2 text-sm text-muted-foreground cursor-pointer">
          <input
            type="checkbox"
            checked={hideCompleted}
            onChange={(e) => setHideCompleted(e.target.checked)}
          />
          {t("tasks.hideCompleted")}
        </label>
      </div>

      <TaskList
        loading={loading}
        tasks={tasks}
        hideCompleted={hideCompleted}
        actions={{ onToggle: toggleComplete, onFlag: flagDue, onEdit: openEdit, onDelete: setDeleteTarget }}
      />

      <TaskEditDialog
        editing={editing}
        form={form}
        setForm={setForm}
        categories={allCategories}
        busy={busy}
        onClose={() => setEditing(null)}
        onSave={() => void submitEdit()}
      />

      {/* Delete confirmation */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("tasks.deleteTask")}</DialogTitle>
            <DialogDescription>{t("tasks.deleteConfirm", { summary: deleteTarget?.summary ?? "" })}</DialogDescription>
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
    </div>
  )
}

// ymd renders a Date as YYYY-MM-DD (the task due shape the backend parses).
function ymd(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, "0")
  const day = String(d.getDate()).padStart(2, "0")
  return `${d.getFullYear()}-${m}-${day}`
}

// addDays returns a Date n days after d.
function addDays(d: Date, n: number): Date {
  return new Date(d.getTime() + n * 86400000)
}

// thisFriday returns the Friday of the current week (today if it is Friday, the
// coming Friday otherwise).
function thisFriday(): Date {
  const d = new Date()
  const day = d.getDay() // 0 Sun .. 6 Sat
  const delta = (5 - day + 7) % 7
  return addDays(d, delta)
}

// nextMonday returns the next Monday strictly after today.
function nextMonday(): Date {
  const d = new Date()
  const day = d.getDay()
  const delta = day === 1 ? 1 : (8 - day) % 7
  return addDays(d, delta === 0 ? 7 : delta)
}
