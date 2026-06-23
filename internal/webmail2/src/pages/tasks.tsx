import { useState, useEffect, useCallback } from "react"
import { ListTodo, Plus, Trash2, Edit, CalendarClock } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Checkbox } from "@/components/ui/checkbox"
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

function dueLabel(due?: string): string {
  if (!due) return ""
  const d = new Date(due.length === 10 ? `${due}T00:00:00` : due)
  if (isNaN(d.getTime())) return due
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })
}

interface TaskForm {
  summary: string
  due: string
  description: string
}

const emptyForm: TaskForm = { summary: "", due: "", description: "" }

export function TasksPage() {
  const { t } = useI18n()
  const [tasks, setTasks] = useState<Task[]>([])
  const [loading, setLoading] = useState(true)
  const [quickAdd, setQuickAdd] = useState("")
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<Task | null>(null)
  const [form, setForm] = useState<TaskForm>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<Task | null>(null)

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
  }, [load])

  const handleQuickAdd = async () => {
    const summary = quickAdd.trim()
    if (!summary) return
    setBusy(true)
    try {
      await api.createTask({ summary, completed: false })
      setQuickAdd("")
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.addFailed"))
    } finally {
      setBusy(false)
    }
  }

  const toggleComplete = async (task: Task) => {
    // Optimistic toggle, then persist the full task with the new state.
    setTasks((prev) => prev.map((t) => (t.uid === task.uid ? { ...t, completed: !t.completed } : t)))
    const payload: TaskInput = {
      summary: task.summary,
      due: task.due,
      description: task.description,
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

  const openEdit = (task: Task) => {
    setEditing(task)
    setForm({ summary: task.summary, due: task.due ?? "", description: task.description ?? "" })
  }

  const submitEdit = async () => {
    if (!editing) return
    if (!form.summary.trim()) {
      toast.error(t("tasks.titleRequired"))
      return
    }
    setBusy(true)
    try {
      await api.updateTask(editing.uid, {
        summary: form.summary.trim(),
        due: form.due || undefined,
        description: form.description || undefined,
        completed: editing.completed,
      })
      toast.success(t("tasks.taskUpdated"))
      setEditing(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.saveFailed"))
    } finally {
      setBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    setBusy(true)
    try {
      await api.deleteTask(deleteTarget.uid)
      toast.success(t("tasks.taskDeleted"))
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("tasks.deleteFailed"))
    } finally {
      setBusy(false)
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
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8 text-center">{t("common.loading")}</p>
      ) : tasks.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <ListTodo className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-medium">{t("tasks.noTasks")}</h3>
          <p className="text-muted-foreground mt-1">{t("tasks.emptyHint")}</p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card divide-y">
          {tasks.map((task) => (
            <div key={task.uid} className="flex items-start gap-3 p-3 hover:bg-accent/50 transition-colors">
              <Checkbox
                checked={task.completed}
                onCheckedChange={() => toggleComplete(task)}
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
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => openEdit(task)}>
                <Edit className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-destructive"
                onClick={() => setDeleteTarget(task)}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {/* Edit dialog */}
      <Dialog open={editing !== null} onOpenChange={(open) => { if (!open) setEditing(null) }}>
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
                value={form.due.length >= 10 ? form.due.slice(0, 10) : form.due}
                onChange={(e) => setForm({ ...form, due: e.target.value })}
              />
            </div>
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
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditing(null)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submitEdit} disabled={busy}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
