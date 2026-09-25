import { useEffect, useRef, useState } from "react"
import { FileText, Mail, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { RichTextEditor } from "@/components/RichTextEditor"
import { useI18n } from "@/hooks/useI18n"
import api, { type SignatureEntry, type TemplateEntry } from "@/utils/api"
import { SettingSection } from "./setting-layout"

type EditorHandle = { getHTML: () => string; setHTML: (html: string) => void }

// SnippetDraft is the add/edit form shared by signatures and templates.
interface SnippetDraft {
  name: string
  subject: string
  body: string
  isHtml: boolean
}

const EMPTY_DRAFT: SnippetDraft = { name: "", subject: "", body: "", isHtml: false }

// SaveKeys names the translation keys a save reports through.
interface SaveKeys {
  nameRequired: string
  saved: string
  saveFailed: string
}

const signatureSaveKeys: SaveKeys = {
  nameRequired: "settings.signature.nameRequired",
  saved: "settings.signature.saved",
  saveFailed: "settings.signature.saveFailed",
}

const templateSaveKeys: SaveKeys = {
  nameRequired: "settings.template.nameRequired",
  saved: "settings.template.saved",
  saveFailed: "settings.template.saveFailed",
}

// useSnippetDraft holds the add/edit form and reads the body from the rich-text
// editor while the form is in HTML mode.
function useSnippetDraft() {
  const [draft, setDraft] = useState<SnippetDraft>(EMPTY_DRAFT)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const bodyRef = useRef<EditorHandle | null>(null)

  const patch = (change: Partial<SnippetDraft>) => setDraft((d) => ({ ...d, ...change }))
  const reset = () => {
    setDraft(EMPTY_DRAFT)
    setError(null)
  }
  const edit = (next: SnippetDraft) => {
    setDraft(next)
    setError(null)
  }
  const body = () => (draft.isHtml && bodyRef.current ? bodyRef.current.getHTML() : draft.body)

  // save runs a store call for a named draft and clears the form when it lands.
  const save = async (store: (name: string, body: string) => Promise<void>, keys: SaveKeys, t: (k: string) => string) => {
    const name = draft.name.trim()
    if (!name) { setError(t(keys.nameRequired)); return }
    setError(null)
    setSaving(true)
    try {
      await store(name, body())
      reset()
      toast.success(t(keys.saved))
    } catch {
      toast.error(t(keys.saveFailed))
    } finally {
      setSaving(false)
    }
  }

  return { draft, patch, reset, edit, saving, error, bodyRef, save }
}

type Draft = ReturnType<typeof useSnippetDraft>

function FormatBadge({ isHtml }: { isHtml: boolean }) {
  return <span className="text-xs rounded px-1.5 py-0.5 bg-muted font-mono">{isHtml ? "HTML" : "TXT"}</span>
}

// SnippetRow is one stored signature or template with its edit and delete
// buttons.
function SnippetRow({ isHtml, name, detail, onEdit, onDelete }: {
  isHtml: boolean
  name: string
  detail: React.ReactNode
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useI18n()
  return (
    <div className="flex items-center justify-between rounded-md border px-3 py-2">
      <div className="flex items-center gap-2 min-w-0">
        <FormatBadge isHtml={isHtml} />
        <span className="truncate text-sm font-medium">{name}</span>
        {detail}
      </div>
      <div className="flex items-center gap-1 ml-2">
        <Button variant="ghost" size="sm" onClick={onEdit}>{t("common.edit")}</Button>
        <Button variant="ghost" size="sm" onClick={onDelete}>
          <Trash2 className="h-4 w-4 text-destructive" />
        </Button>
      </div>
    </div>
  )
}

// SnippetBody is the body field: the rich-text editor in HTML mode, a textarea
// otherwise.
function SnippetBody({ form, htmlPlaceholder, textPlaceholder }: { form: Draft; htmlPlaceholder: string; textPlaceholder: string }) {
  if (form.draft.isHtml) {
    return (
      <RichTextEditor
        ref={form.bodyRef}
        value={form.draft.body}
        onChange={(body) => form.patch({ body })}
        placeholder={htmlPlaceholder}
      />
    )
  }
  return (
    <Textarea
      value={form.draft.body}
      onChange={(e) => form.patch({ body: e.target.value })}
      placeholder={textPlaceholder}
      rows={4}
    />
  )
}

// SnippetForm is the add/edit form. withSubject adds the template subject.
function SnippetForm({ form, keys, withSubject, onSave }: {
  form: Draft
  keys: { edit: string; addNew: string; name: string; subject?: string; bodyHtml: string; body: string }
  withSubject: boolean
  onSave: () => void
}) {
  const { t } = useI18n()
  const { draft } = form
  return (
    <div className="space-y-2 rounded-md border p-3">
      <p className="text-sm font-medium">{draft.name ? t(keys.edit) : t(keys.addNew)}</p>
      <div className="flex items-center gap-2">
        <Input value={draft.name} onChange={(e) => form.patch({ name: e.target.value })} placeholder={t(keys.name)} className="max-w-xs" />
        {withSubject && (
          <Input
            value={draft.subject}
            onChange={(e) => form.patch({ subject: e.target.value })}
            placeholder={t(keys.subject ?? "")}
            className="max-w-xs"
          />
        )}
        <label className="flex items-center gap-1.5 text-sm cursor-pointer">
          <input type="checkbox" checked={draft.isHtml} onChange={(e) => form.patch({ isHtml: e.target.checked })} />
          {t("settings.signature.isHtml")}
        </label>
      </div>
      {form.error && <p className="text-xs text-destructive">{form.error}</p>}
      <SnippetBody form={form} htmlPlaceholder={t(keys.bodyHtml)} textPlaceholder={t(keys.body)} />
      <div className="flex items-center gap-2">
        <Button onClick={onSave} disabled={form.saving || !draft.name.trim()}>
          {form.saving ? t("common.saving") : t("common.save")}
        </Button>
        {draft.name && (
          <Button variant="ghost" size="sm" onClick={form.reset}>{t("common.cancel")}</Button>
        )}
      </div>
    </div>
  )
}

// preview shortens a signature body for its list row.
function preview(body: string): string {
  return body.length > 60 ? `${body.slice(0, 60)}…` : body
}

// useStoredList loads a list once and keeps it for local edits. The loader is a
// module-level function, so the effect runs once.
function useStoredList<T>(load: () => Promise<T[]>) {
  const [items, setItems] = useState<T[]>([])
  useEffect(() => {
    let cancelled = false
    load()
      .then((list) => { if (!cancelled) setItems(list) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [load])
  return [items, setItems] as const
}

const loadSignatures = async () => (await api.getSignatures()).signatures ?? []
const loadTemplates = async () => (await api.getTemplates()).templates ?? []

// SignatureSection manages the outgoing-mail signatures (backed by /api/v1/signatures).
export function SignatureSection() {
  const { t } = useI18n()
  const [signatures, setSignatures] = useStoredList<SignatureEntry>(loadSignatures)
  const form = useSnippetDraft()

  const save = () => form.save(async (name, body) => {
    await api.saveSignature({ name, body, is_html: form.draft.isHtml, ord: 0 })
    setSignatures(await loadSignatures())
  }, signatureSaveKeys, t)

  const remove = async (name: string) => {
    try {
      await api.deleteSignature(name)
      setSignatures((prev) => prev.filter((s) => s.name !== name))
      toast.success(t("settings.signature.deleted"))
    } catch {
      toast.error(t("settings.signature.deleteFailed"))
    }
  }

  return (
    <SettingSection icon={Mail} title={t("settings.signature.title")} description={t("settings.signature.description")}>
      <div className="space-y-4">
        {signatures.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm font-medium">{t("settings.signature.current")}</p>
            {signatures.map((sig) => (
              <SnippetRow
                key={sig.name}
                isHtml={sig.is_html}
                name={sig.name}
                detail={<span className="text-xs text-muted-foreground truncate hidden sm:inline">{preview(sig.body)}</span>}
                onEdit={() => form.edit({ name: sig.name, subject: "", body: sig.body, isHtml: sig.is_html })}
                onDelete={() => void remove(sig.name)}
              />
            ))}
          </div>
        )}
        <SnippetForm
          form={form}
          withSubject={false}
          keys={{
            edit: "settings.signature.edit",
            addNew: "settings.signature.addNew",
            name: "settings.signature.namePlaceholder",
            bodyHtml: "settings.signature.placeholderHtml",
            body: "settings.signature.placeholder",
          }}
          onSave={() => void save()}
        />
      </div>
    </SettingSection>
  )
}

// TemplateSection manages the message templates (backed by /api/v1/templates).
export function TemplateSection() {
  const { t } = useI18n()
  const [templates, setTemplates] = useStoredList<TemplateEntry>(loadTemplates)
  const form = useSnippetDraft()

  const save = () => form.save(async (name, body) => {
    await api.saveTemplate({ name, subject: form.draft.subject, body, is_html: form.draft.isHtml })
    setTemplates(await loadTemplates())
  }, templateSaveKeys, t)

  const remove = async (name: string) => {
    try {
      await api.deleteTemplate(name)
      setTemplates((prev) => prev.filter((tpl) => tpl.name !== name))
      toast.success(t("settings.template.deleted"))
    } catch {
      toast.error(t("settings.template.deleteFailed"))
    }
  }

  return (
    <SettingSection icon={FileText} title={t("settings.template.title")} description={t("settings.template.description")}>
      <div className="space-y-4">
        {templates.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm font-medium">{t("settings.template.current")}</p>
            {templates.map((tpl) => (
              <SnippetRow
                key={tpl.name}
                isHtml={tpl.is_html}
                name={tpl.name}
                detail={tpl.subject && (
                  <span className="text-xs text-muted-foreground truncate hidden sm:inline">
                    {t("settings.template.subject")}: {tpl.subject}
                  </span>
                )}
                onEdit={() => form.edit({ name: tpl.name, subject: tpl.subject, body: tpl.body, isHtml: tpl.is_html })}
                onDelete={() => void remove(tpl.name)}
              />
            ))}
          </div>
        )}
        <SnippetForm
          form={form}
          withSubject
          keys={{
            edit: "settings.template.edit",
            addNew: "settings.template.addNew",
            name: "settings.template.namePlaceholder",
            subject: "settings.template.subjectPlaceholder",
            bodyHtml: "settings.template.bodyPlaceholderHtml",
            body: "settings.template.bodyPlaceholder",
          }}
          onSave={() => void save()}
        />
      </div>
    </SettingSection>
  )
}
