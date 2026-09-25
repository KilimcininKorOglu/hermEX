import { useState, useEffect, useCallback, useRef } from "react"
import { Filter as FilterIcon, Plus, Pencil, Trash2, X, ArrowUp, ArrowDown, Download, Upload, Play } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogFooter,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { toast } from "sonner"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"
import type { Filter, FilterCondition, FilterAction, FilterInput } from "@/utils/api"
import { useBusyGate } from "@/hooks/useBusyGate"
import { LEVEL_FIELDS, VALUELESS_FIELDS, draftOf, emptyAction, emptyCondition, emptyDraft, validateDraft } from "@/utils/filterDraft"

// RUN_FOLDERS are the folders a manual filter run may sweep, matching the slugs the
// API resolves.
const RUN_FOLDERS = ["inbox", "spam", "trash", "sent", "drafts"] as const

type TFunc = (key: string, params?: Record<string, string>) => string

const conditionFields = (t: TFunc): { value: FilterCondition["field"]; label: string }[] => [
  { value: "from", label: t("common.from") },
  { value: "to", label: t("common.to") },
  { value: "cc", label: t("filters.field.cc") },
  { value: "subject", label: t("common.subject") },
  { value: "body", label: t("filters.field.body") },
  { value: "header", label: t("filters.field.header") },
  { value: "size", label: t("filters.field.size") },
  { value: "flag", label: t("filters.field.flag") },
  { value: "address", label: t("filters.field.address") },
  { value: "importance", label: t("filters.field.importance") },
  { value: "sensitivity", label: t("filters.field.sensitivity") },
  { value: "oof", label: t("filters.field.oof") },
]

// The fixed level options offered for importance and sensitivity conditions.
const levelOptions = (
  t: TFunc,
  field: FilterCondition["field"]
): { value: string; label: string }[] =>
  field === "importance"
    ? [
        { value: "high", label: t("filters.importance.high") },
        { value: "normal", label: t("filters.importance.normal") },
        { value: "low", label: t("filters.importance.low") },
      ]
    : [
        { value: "normal", label: t("filters.sensitivity.normal") },
        { value: "personal", label: t("filters.sensitivity.personal") },
        { value: "private", label: t("filters.sensitivity.private") },
        { value: "confidential", label: t("filters.sensitivity.confidential") },
      ]

const conditionOperators = (t: TFunc): { value: FilterCondition["operator"]; label: string }[] => [
  { value: "contains", label: t("filters.operator.contains") },
  { value: "equals", label: t("filters.operator.equals") },
  { value: "startsWith", label: t("filters.operator.startsWith") },
  { value: "endsWith", label: t("filters.operator.endsWith") },
  { value: "matches", label: t("filters.operator.matches") },
]

// The full canonical action vocabulary (semcore RuleActionKind). Every kind is
// editable so a rule created in Outlook/admin can be edited here without losing
// actions the editor does not recognize.
const actionTypes = (t: TFunc): { value: FilterAction["type"]; label: string }[] => [
  { value: "moveToFolder", label: t("filters.action.moveToFolder") },
  { value: "copyToFolder", label: t("filters.action.copyToFolder") },
  { value: "markRead", label: t("filters.action.markRead") },
  { value: "markImportant", label: t("filters.action.markImportant") },
  { value: "flag", label: t("filters.action.flag") },
  { value: "forward", label: t("filters.action.forward") },
  { value: "forwardAsAttachment", label: t("filters.action.forwardAsAttachment") },
  { value: "redirect", label: t("filters.action.redirect") },
  { value: "reject", label: t("filters.action.reject") },
  { value: "addHeader", label: t("filters.action.addHeader") },
  { value: "deleteHeader", label: t("filters.action.deleteHeader") },
  { value: "categorize", label: t("filters.action.categorize") },
  { value: "delete", label: t("filters.action.delete") },
  { value: "stop", label: t("filters.action.stop") },
  { value: "vacation", label: t("filters.action.vacation") },
]

// ConditionRows renders the editable field/operator/value rows shared by a rule's
// conditions and its exceptions. Importance/sensitivity pick a level from a list,
// flag/out-of-office take no value, and the operator only applies to text fields.
function ConditionRows({
  conditions,
  onUpdate,
  onRemove,
  allowEmpty,
  t,
}: {
  conditions: FilterCondition[]
  onUpdate: (index: number, patch: Partial<FilterCondition>) => void
  onRemove: (index: number) => void
  allowEmpty: boolean
  t: TFunc
}) {
  return (
    <>
      {conditions.map((cond, i) => {
        const isLevel = LEVEL_FIELDS.has(cond.field)
        const isValueless = VALUELESS_FIELDS.has(cond.field)
        const isText = !isLevel && !isValueless
        return (
          <div key={i} className="flex flex-wrap items-center gap-2">
            <Select value={cond.field} onValueChange={(v) => onUpdate(i, { field: v as FilterCondition["field"] })}>
              <SelectTrigger className="w-[120px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {conditionFields(t).map((f) => (
                  <SelectItem key={f.value} value={f.value}>
                    {f.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {isText && (
              <Select value={cond.operator} onValueChange={(v) => onUpdate(i, { operator: v as FilterCondition["operator"] })}>
                <SelectTrigger className="w-[130px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {conditionOperators(t).map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {cond.field === "header" && (
              <Input
                className="w-[140px]"
                placeholder={t("filters.headerNamePlaceholder")}
                value={cond.headerName ?? ""}
                onChange={(e) => onUpdate(i, { headerName: e.target.value })}
              />
            )}
            {isLevel && (
              <Select value={cond.value || "normal"} onValueChange={(v) => onUpdate(i, { value: v })}>
                <SelectTrigger className="min-w-[140px] flex-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {levelOptions(t, cond.field).map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {isText && (
              <Input
                className="min-w-[140px] flex-1"
                placeholder={t("filters.valuePlaceholder")}
                value={cond.value}
                onChange={(e) => onUpdate(i, { value: e.target.value })}
              />
            )}
            {(allowEmpty || conditions.length > 1) && (
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => onRemove(i)}>
                <X className="h-4 w-4" />
              </Button>
            )}
          </div>
        )
      })}
    </>
  )
}

type ActionTextKey = "target" | "forwardTo" | "message" | "headerName" | "headerValue" | "flagName"

interface ActionTextInput {
  key: ActionTextKey
  placeholder: string
  className: string
}

const WIDE = "min-w-[140px] flex-1"
const NARROW = "w-[150px]"

// ACTION_INPUTS lists, per action type, the text inputs its row shows, in order.
const ACTION_INPUTS: Partial<Record<FilterAction["type"], ActionTextInput[]>> = {
  moveToFolder: [{ key: "target", placeholder: "filters.targetFolderPlaceholder", className: WIDE }],
  copyToFolder: [{ key: "target", placeholder: "filters.targetFolderPlaceholder", className: WIDE }],
  forward: [{ key: "forwardTo", placeholder: "filters.destinationPlaceholder", className: WIDE }],
  forwardAsAttachment: [{ key: "forwardTo", placeholder: "filters.destinationPlaceholder", className: WIDE }],
  redirect: [{ key: "forwardTo", placeholder: "filters.destinationPlaceholder", className: WIDE }],
  reject: [{ key: "message", placeholder: "filters.rejectionPlaceholder", className: WIDE }],
  vacation: [{ key: "message", placeholder: "filters.autoReplyPlaceholder", className: WIDE }],
  categorize: [{ key: "target", placeholder: "filters.categoriesPlaceholder", className: WIDE }],
  addHeader: [
    { key: "headerName", placeholder: "filters.headerNamePlaceholder", className: NARROW },
    { key: "headerValue", placeholder: "filters.headerValuePlaceholder", className: "min-w-[120px] flex-1" },
  ],
  deleteHeader: [{ key: "headerName", placeholder: "filters.headerNamePlaceholder", className: NARROW }],
  flag: [{ key: "flagName", placeholder: "filters.flagNamePlaceholder", className: NARROW }],
}

// ActionRow edits one action: its type and the fields that type takes.
function ActionRow({
  action,
  removable,
  onUpdate,
  onRemove,
  t,
}: {
  action: FilterAction
  removable: boolean
  onUpdate: (patch: Partial<FilterAction>) => void
  onRemove: () => void
  t: TFunc
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        value={action.type}
        onValueChange={(v) =>
          onUpdate({ type: v as FilterAction["type"] })
        }
      >
        <SelectTrigger className="w-[160px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {actionTypes(t).map((a) => (
            <SelectItem key={a.value} value={a.value}>
              {a.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {(ACTION_INPUTS[action.type] ?? []).map((input) => (
        <Input
          key={input.key}
          className={input.className}
          placeholder={t(input.placeholder)}
          value={action[input.key] ?? ""}
          onChange={(e) => onUpdate({ [input.key]: e.target.value })}
        />
      ))}
      {action.type === "flag" && (
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          <Switch
            checked={action.clearFlag ?? false}
            onCheckedChange={(v) => onUpdate({ clearFlag: v })}
          />
          {t("filters.clear")}
        </label>
      )}
      {removable && (
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          onClick={onRemove}
        >
          <X className="h-4 w-4" />
        </Button>
      )}
    </div>
  )
}

// SectionTitle is an editor section label with an Add button.
function SectionTitle({ label, onAdd, t }: { label: string; onAdd: () => void; t: TFunc }) {
  return (
    <div className="flex items-center justify-between">
      <Label>{label}</Label>
      <Button variant="outline" size="sm" onClick={onAdd}>
        <Plus className="h-4 w-4 mr-1" />
        {t("common.add")}
      </Button>
    </div>
  )
}

// filterSummary describes a filter as its match mode and its row counts.
function filterSummary(filter: Filter, t: TFunc): string {
  return t(filter.matchAll ? "filters.summaryAll" : "filters.summaryAny", {
    conditionCount: String(filter.conditions.length),
    conditionWord: t(
      filter.conditions.length !== 1
        ? "filters.conditionPlural"
        : "filters.conditionSingular"
    ),
    actionCount: String(filter.actions.length),
    actionWord: t(
      filter.actions.length !== 1
        ? "filters.actionPlural"
        : "filters.actionSingular"
    ),
  })
}

interface FilterCardActions {
  onMove: (index: number, dir: -1 | 1) => void
  onToggle: (filter: Filter) => void
  onRun: (filterId: string) => void
  onEdit: (filter: Filter) => void
  onDelete: (filter: Filter) => void
}

// FilterCard is one saved filter with its order, enable, run, edit and delete controls.
function FilterCard({
  filter,
  index,
  count,
  running,
  actions,
  t,
}: {
  filter: Filter
  index: number
  count: number
  running: boolean
  actions: FilterCardActions
  t: TFunc
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="flex flex-col">
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            disabled={index === 0}
            onClick={() => actions.onMove(index, -1)}
            title={t("filters.moveUp")}
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            disabled={index === count - 1}
            onClick={() => actions.onMove(index, 1)}
            title={t("filters.moveDown")}
          >
            <ArrowDown className="h-4 w-4" />
          </Button>
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="font-medium">{filter.name}</span>
            {!filter.enabled && (
              <Badge variant="secondary" className="text-[10px]">
                {t("filters.disabled")}
              </Badge>
            )}
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            {filterSummary(filter, t)}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Switch
            checked={filter.enabled}
            onCheckedChange={() => actions.onToggle(filter)}
          />
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            disabled={running || !filter.enabled}
            title={t("filters.runThis")}
            onClick={() => actions.onRun(filter.id)}
          >
            <Play className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={() => actions.onEdit(filter)}
          >
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-destructive"
            onClick={() => actions.onDelete(filter)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}

// FilterList shows skeletons while loading, the empty state, or the filter cards.
function FilterList({
  loading,
  filters,
  running,
  actions,
  t,
}: {
  loading: boolean
  filters: Filter[]
  running: boolean
  actions: FilterCardActions
  t: TFunc
}) {
  if (loading) {
    return (
      <div className="space-y-3">
        {[1, 2].map((i) => (
          <div key={i} className="rounded-lg border p-4">
            <Skeleton className="h-5 w-48" />
            <Skeleton className="mt-2 h-3 w-full" />
          </div>
        ))}
      </div>
    )
  }
  if (filters.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-center">
        <div className="rounded-full bg-muted p-4">
          <FilterIcon className="h-8 w-8 text-muted-foreground" />
        </div>
        <h3 className="mt-4 text-lg font-semibold">{t("filters.empty.title")}</h3>
        <p className="text-sm text-muted-foreground">
          {t("filters.empty.description")}
        </p>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      {filters.map((filter, index) => (
        <FilterCard
          key={filter.id}
          filter={filter}
          index={index}
          count={filters.length}
          running={running}
          actions={actions}
          t={t}
        />
      ))}
    </div>
  )
}

// FilterEditor edits a draft filter's name, match mode, conditions, exceptions
// and actions.
function FilterEditor({
  draft,
  setDraft,
  t,
}: {
  draft: FilterInput
  setDraft: React.Dispatch<React.SetStateAction<FilterInput>>
  t: TFunc
}) {
  const exceptions = draft.exceptions ?? []
  const patchRow = <T,>(rows: T[], index: number, patch: Partial<T>): T[] =>
    rows.map((row, i) => (i === index ? { ...row, ...patch } : row))
  const dropRow = <T,>(rows: T[], index: number): T[] => rows.filter((_, i) => i !== index)
  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <Label htmlFor="filter-name">{t("common.name")}</Label>
        <Input
          id="filter-name"
          value={draft.name}
          onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          placeholder={t("filters.namePlaceholder")}
        />
      </div>

      <div className="flex items-center justify-between">
        <div>
          <p className="font-medium">{t("filters.matchAllConditions")}</p>
          <p className="text-sm text-muted-foreground">
            {t("filters.matchAllHint")}
          </p>
        </div>
        <Switch
          checked={draft.matchAll}
          onCheckedChange={(v) => setDraft({ ...draft, matchAll: v })}
        />
      </div>

      {/* Conditions */}
      <div className="space-y-3">
        <SectionTitle
          label={t("filters.conditions")}
          onAdd={() => setDraft({ ...draft, conditions: [...draft.conditions, emptyCondition()] })}
          t={t}
        />
        <ConditionRows
          conditions={draft.conditions}
          onUpdate={(index, patch) => setDraft((d) => ({ ...d, conditions: patchRow(d.conditions, index, patch) }))}
          onRemove={(index) => setDraft((d) => ({ ...d, conditions: dropRow(d.conditions, index) }))}
          allowEmpty={false}
          t={t}
        />
      </div>

      {/* Exceptions: the rule does NOT fire if any exception matches (optional) */}
      <div className="space-y-3">
        <SectionTitle
          label={t("filters.exceptions")}
          onAdd={() => setDraft({ ...draft, exceptions: [...exceptions, emptyCondition()] })}
          t={t}
        />
        {exceptions.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("filters.noExceptions")}</p>
        ) : (
          <ConditionRows
            conditions={exceptions}
            onUpdate={(index, patch) => setDraft((d) => ({ ...d, exceptions: patchRow(d.exceptions ?? [], index, patch) }))}
            onRemove={(index) => setDraft((d) => ({ ...d, exceptions: dropRow(d.exceptions ?? [], index) }))}
            allowEmpty={true}
            t={t}
          />
        )}
      </div>

      {/* Actions */}
      <div className="space-y-3">
        <SectionTitle
          label={t("common.actions")}
          onAdd={() => setDraft({ ...draft, actions: [...draft.actions, emptyAction()] })}
          t={t}
        />
        {draft.actions.map((action, i) => (
          <ActionRow
            key={i}
            action={action}
            removable={draft.actions.length > 1}
            onUpdate={(patch) => setDraft((d) => ({ ...d, actions: patchRow(d.actions, i, patch) }))}
            onRemove={() => setDraft({ ...draft, actions: dropRow(draft.actions, i) })}
            t={t}
          />
        ))}
      </div>
    </div>
  )
}

export function FiltersPage() {
  const { t } = useI18n()
  const [filters, setFilters] = useState<Filter[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [draft, setDraft] = useState<FilterInput>(emptyDraft())
  // One mutation at a time: a button's disabled attribute is not the guard,
  // because a second click can arrive before React re-renders with the new state.
  const { busy: saving, begin: beginMutation, end: endMutation } = useBusyGate()
  const [deleteTarget, setDeleteTarget] = useState<Filter | null>(null)
  const [transferring, setTransferring] = useState(false)
  const [running, setRunning] = useState(false)
  // runFolder is the folder a manual run sweeps. Filters are stored on the inbox,
  // so a rule written after the fact is applied to wherever the old mail actually
  // is by choosing that folder here.
  const [runFolder, setRunFolder] = useState("inbox")
  const importInputRef = useRef<HTMLInputElement>(null)

  const loadFilters = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.getFilters()
      setFilters(result.filters ?? [])
    } catch {
      setFilters([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadFilters()
  }, [loadFilters])

  const handleExport = useCallback(async () => {
    setTransferring(true)
    try {
      const skipped = await api.exportRules()
      if (skipped) {
        toast.warning(t("filters.toast.exportWarning"))
      } else {
        toast.success(t("filters.toast.exported"))
      }
    } catch {
      toast.error(t("filters.toast.exportFailed"))
    } finally {
      setTransferring(false)
    }
  }, [t])

  // handleRunNow applies the saved filters to the chosen folder's existing mail and
  // reports how many messages a rule acted on (the old webmail's "run now"). A
  // filter id runs only that one, so fixing a single filter does not re-run the rest.
  const handleRunNow = useCallback(
    async (filterId?: string) => {
      setRunning(true)
      try {
        const res = await api.runFilters({ folder: runFolder, filter: filterId })
        toast.success(
          t("filters.toast.ran", { affected: String(res.affected), evaluated: String(res.evaluated) }),
        )
      } catch {
        toast.error(t("filters.toast.runFailed"))
      } finally {
        setRunning(false)
      }
    },
    [runFolder, t],
  )

  const handleImportFile = useCallback(
    async (file: File) => {
      setTransferring(true)
      try {
        const result = await api.importRules(file)
        if (result.skippedRules > 0 || result.skippedElements > 0) {
          toast.warning(
            t("filters.toast.importPartial", {
              imported: String(result.imported),
              skipped: String(result.skippedRules + result.skippedElements),
            }),
          )
        } else {
          toast.success(t("filters.toast.imported", { imported: String(result.imported) }))
        }
        await loadFilters()
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("filters.toast.importFailed"))
      } finally {
        setTransferring(false)
      }
    },
    [t, loadFilters],
  )

  // Move a filter up/down in priority order and persist the new order.
  const moveFilter = async (index: number, dir: -1 | 1) => {
    const target = index + dir
    if (target < 0 || target >= filters.length) return
    const reordered = [...filters]
    const [item] = reordered.splice(index, 1)
    reordered.splice(target, 0, item)
    setFilters(reordered)
    try {
      await api.reorderFilters(reordered.map((f) => f.id))
    } catch (err) {
      console.error("Failed to reorder filters:", err)
      toast.error(t("filters.toast.reorderFailed"))
      loadFilters()
    }
  }

  const openCreate = () => {
    setEditingId(null)
    setDraft(emptyDraft())
    setDialogOpen(true)
  }

  const openEdit = (filter: Filter) => {
    setEditingId(filter.id)
    setDraft(draftOf(filter))
    setDialogOpen(true)
  }

  const handleSave = async () => {
    const error = validateDraft(draft)
    if (error) {
      toast.error(t(error))
      return
    }
    if (!beginMutation()) return
    try {
      if (editingId) {
        await api.updateFilter(editingId, draft)
        toast.success(t("filters.toast.updated"))
      } else {
        // Send the whole draft: the create handler stores the body as given,
        // so an absent `enabled` would store the new filter disabled.
        await api.createFilter(draft)
        toast.success(t("filters.toast.created"))
      }
      setDialogOpen(false)
      await loadFilters()
    } catch {
      toast.error(t("filters.toast.saveFailed"))
    } finally {
      endMutation()
    }
  }

  const handleToggle = async (filter: Filter) => {
    try {
      // Send the full filter: the update handler overwrites matchAll with
      // the request value, so a partial body would silently reset it.
      await api.updateFilter(filter.id, {
        name: filter.name,
        enabled: !filter.enabled,
        matchAll: filter.matchAll,
        conditions: filter.conditions,
        actions: filter.actions,
      })
      await loadFilters()
    } catch {
      toast.error(t("filters.toast.updateFailed"))
    }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await api.deleteFilter(deleteTarget.id)
      toast.success(t("filters.toast.deleted"))
      await loadFilters()
    } catch {
      toast.error(t("filters.toast.deleteFailed"))
    } finally {
      setDeleteTarget(null)
    }
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="rounded-full bg-muted p-2">
            <FilterIcon className="h-5 w-5" />
          </div>
          <div>
            <h2 className="text-2xl font-bold">{t("nav.filters")}</h2>
            <p className="text-sm text-muted-foreground">
              {t("filters.description")}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <input
            ref={importInputRef}
            type="file"
            accept=".rwz"
            className="hidden"
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = "" // allow re-selecting the same file
              if (file) handleImportFile(file)
            }}
          />
          <Button
            variant="outline"
            onClick={() => importInputRef.current?.click()}
            disabled={transferring}
            title={t("filters.importHint")}
          >
            <Upload className="h-4 w-4 mr-1" />
            {t("filters.import")}
          </Button>
          <Button
            variant="outline"
            onClick={handleExport}
            disabled={transferring || filters.length === 0}
            title={t("filters.exportHint")}
          >
            <Download className="h-4 w-4 mr-1" />
            {t("filters.export")}
          </Button>
          <Select value={runFolder} onValueChange={setRunFolder}>
            <SelectTrigger className="w-36" title={t("filters.runFolderHint")}>
              <SelectValue placeholder={t("filters.runFolder")} />
            </SelectTrigger>
            <SelectContent>
              {RUN_FOLDERS.map((f) => (
                <SelectItem key={f} value={f}>
                  {t(`nav.${f}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            onClick={() => handleRunNow()}
            disabled={running || filters.length === 0}
            title={t("filters.runNowHint")}
          >
            <Play className="h-4 w-4 mr-1" />
            {t("filters.runNow")}
          </Button>
          <Button onClick={openCreate}>
            <Plus className="h-4 w-4 mr-1" />
            {t("filters.newFilter")}
          </Button>
        </div>
      </div>

      <FilterList
        loading={loading}
        filters={filters}
        running={running}
        actions={{ onMove: moveFilter, onToggle: handleToggle, onRun: handleRunNow, onEdit: openEdit, onDelete: setDeleteTarget }}
        t={t}
      />

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editingId ? t("filters.editTitle") : t("filters.newFilter")}</DialogTitle>
            <DialogDescription>
              {t("filters.dialogDescription")}
            </DialogDescription>
          </DialogHeader>

          <FilterEditor draft={draft} setDraft={setDraft} t={t} />

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)} disabled={saving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleSave} disabled={saving}>
              {editingId ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("filters.deleteTitle")}</DialogTitle>
            <DialogDescription>
              {t("filters.deleteConfirm", { name: deleteTarget?.name || "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDelete}>
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
