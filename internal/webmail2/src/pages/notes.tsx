import { useState, useEffect, useCallback } from "react"
import { StickyNote, Plus, Trash2, Edit } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import api, { type Note } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"

interface NoteForm {
  title: string
  body: string
}

const emptyForm: NoteForm = { title: "", body: "" }

export function NotesPage() {
  const { t } = useI18n()
  const [notes, setNotes] = useState<Note[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<Note | null>(null)
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState<NoteForm>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<Note | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getNotes()
      setNotes(res.notes ?? [])
    } catch {
      setNotes([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const openCreate = () => {
    setCreating(true)
    setForm(emptyForm)
  }

  const openEdit = (note: Note) => {
    setEditing(note)
    setForm({ title: note.title ?? "", body: note.body ?? "" })
  }

  const submitCreate = async () => {
    if (!form.title.trim() && !form.body.trim()) {
      toast.error(t("notes.titleOrBodyRequired"))
      return
    }
    setBusy(true)
    try {
      await api.createNote({ title: form.title.trim(), body: form.body })
      toast.success(t("notes.noteCreated"))
      setCreating(false)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("notes.createFailed"))
    } finally {
      setBusy(false)
    }
  }

  const submitEdit = async () => {
    if (!editing) return
    if (!form.title.trim() && !form.body.trim()) {
      toast.error(t("notes.titleOrBodyRequired"))
      return
    }
    setBusy(true)
    try {
      await api.updateNote(editing.id, { title: form.title.trim(), body: form.body })
      toast.success(t("notes.noteUpdated"))
      setEditing(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("notes.saveFailed"))
    } finally {
      setBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    setBusy(true)
    try {
      await api.deleteNote(deleteTarget.id)
      toast.success(t("notes.noteDeleted"))
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("notes.deleteFailed"))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-4 max-w-3xl">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <StickyNote className="h-6 w-6 text-primary" />
          <h1 className="text-2xl font-bold">{t("nav.notes")}</h1>
        </div>
        <Button onClick={openCreate}>
          <Plus className="mr-2 h-4 w-4" />
          {t("notes.newNote")}
        </Button>
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8 text-center">{t("common.loading")}</p>
      ) : notes.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <StickyNote className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-medium">{t("notes.noNotes")}</h3>
          <p className="text-muted-foreground mt-1">{t("notes.emptyDescription")}</p>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {notes.map((note) => (
            <div
              key={note.id}
              className="group flex flex-col rounded-lg border bg-card p-4 hover:bg-accent/50 transition-colors"
            >
              <div className="flex items-start justify-between gap-2">
                <p className="font-medium break-words">{note.title || t("notes.untitled")}</p>
                <div className="flex shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => openEdit(note)}>
                    <Edit className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-destructive"
                    onClick={() => setDeleteTarget(note)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
              {note.body && (
                <p className="mt-2 whitespace-pre-wrap text-sm text-muted-foreground line-clamp-6">{note.body}</p>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Create dialog */}
      <Dialog open={creating} onOpenChange={(open) => { if (!open) setCreating(false) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("notes.newNoteTitle")}</DialogTitle>
            <DialogDescription>{t("notes.sharedDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="note-title">{t("notes.titleLabel")}</Label>
              <Input
                id="note-title"
                value={form.title}
                onChange={(e) => setForm({ ...form, title: e.target.value })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="note-body">{t("notes.bodyLabel")}</Label>
              <Textarea
                id="note-body"
                value={form.body}
                onChange={(e) => setForm({ ...form, body: e.target.value })}
                rows={6}
                placeholder={t("notes.bodyPlaceholder")}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreating(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submitCreate} disabled={busy}>
              {t("common.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit dialog */}
      <Dialog open={editing !== null} onOpenChange={(open) => { if (!open) setEditing(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("notes.editNote")}</DialogTitle>
            <DialogDescription>{t("notes.sharedDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="edit-note-title">{t("notes.titleLabel")}</Label>
              <Input
                id="edit-note-title"
                value={form.title}
                onChange={(e) => setForm({ ...form, title: e.target.value })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-note-body">{t("notes.bodyLabel")}</Label>
              <Textarea
                id="edit-note-body"
                value={form.body}
                onChange={(e) => setForm({ ...form, body: e.target.value })}
                rows={6}
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
            <DialogTitle>{t("notes.deleteNote")}</DialogTitle>
            <DialogDescription>
              {t("notes.deleteConfirm", { title: deleteTarget?.title || t("notes.untitled") })}
            </DialogDescription>
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
