import { useState, useEffect, useCallback, useRef } from "react"
import { Filter as FilterIcon, Plus, Pencil, Trash2, X, ArrowUp, ArrowDown, Download, Upload } from "lucide-react"
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

type TFunc = (key: string, params?: Record<string, string>) => string

const conditionFields = (t: TFunc): { value: FilterCondition["field"]; label: string }[] => [
  { value: "from", label: t("common.from") },
  { value: "to", label: t("common.to") },
  { value: "subject", label: t("common.subject") },
  { value: "body", label: t("filters.field.body") },
  { value: "header", label: t("filters.field.header") },
  { value: "size", label: t("filters.field.size") },
  { value: "flag", label: t("filters.field.flag") },
  { value: "address", label: t("filters.field.address") },
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
  { value: "delete", label: t("filters.action.delete") },
  { value: "stop", label: t("filters.action.stop") },
  { value: "vacation", label: t("filters.action.vacation") },
]

// Action types whose forward/redirect address lives in forwardTo.
const FORWARD_TYPES = new Set<FilterAction["type"]>(["forward", "forwardAsAttachment", "redirect"])

function emptyCondition(): FilterCondition {
  return { field: "from", operator: "contains", value: "" }
}

function emptyAction(): FilterAction {
  return { type: "moveToFolder", target: "" }
}

function emptyDraft(): FilterInput {
  return {
    name: "",
    enabled: true,
    matchAll: true,
    conditions: [emptyCondition()],
    actions: [emptyAction()],
  }
}

export function FiltersPage() {
  const { t } = useI18n()
  const [filters, setFilters] = useState<Filter[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [draft, setDraft] = useState<FilterInput>(emptyDraft())
  const [saving, setSaving] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Filter | null>(null)
  const [transferring, setTransferring] = useState(false)
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
    setDraft({
      name: filter.name,
      enabled: filter.enabled,
      matchAll: filter.matchAll,
      conditions: filter.conditions.length ? filter.conditions : [emptyCondition()],
      actions: filter.actions.length ? filter.actions : [emptyAction()],
    })
    setDialogOpen(true)
  }

  const updateCondition = (index: number, patch: Partial<FilterCondition>) => {
    setDraft((d) => ({
      ...d,
      conditions: d.conditions.map((c, i) => (i === index ? { ...c, ...patch } : c)),
    }))
  }

  const updateAction = (index: number, patch: Partial<FilterAction>) => {
    setDraft((d) => ({
      ...d,
      actions: d.actions.map((a, i) => (i === index ? { ...a, ...patch } : a)),
    }))
  }

  const validate = (): string | null => {
    if (!draft.name.trim()) return t("filters.validation.nameRequired")
    if (draft.conditions.length === 0) return t("filters.validation.conditionRequired")
    for (const c of draft.conditions) {
      if (!c.value.trim()) return t("filters.validation.conditionValue")
      if (c.field === "header" && !c.headerName?.trim()) {
        return t("filters.validation.headerName")
      }
    }
    if (draft.actions.length === 0) return t("filters.validation.actionRequired")
    for (const a of draft.actions) {
      if ((a.type === "moveToFolder" || a.type === "copyToFolder") && !a.target?.trim()) {
        return t("filters.validation.targetFolder")
      }
      if (FORWARD_TYPES.has(a.type) && !a.forwardTo?.trim()) {
        return t("filters.validation.destinationAddress")
      }
      if (a.type === "reject" && !a.message?.trim()) {
        return t("filters.validation.rejectMessage")
      }
      if (a.type === "addHeader" && (!a.headerName?.trim() || !a.headerValue?.trim())) {
        return t("filters.validation.addHeaderFields")
      }
      if (a.type === "deleteHeader" && !a.headerName?.trim()) {
        return t("filters.validation.deleteHeaderName")
      }
      if (a.type === "flag" && !a.flagName?.trim()) {
        return t("filters.validation.flagName")
      }
    }
    return null
  }

  const handleSave = async () => {
    const error = validate()
    if (error) {
      toast.error(error)
      return
    }
    setSaving(true)
    try {
      if (editingId) {
        await api.updateFilter(editingId, draft)
        toast.success(t("filters.toast.updated"))
      } else {
        // Create does not accept `enabled` (new filters are enabled by
        // default) and the backend rejects unknown JSON fields.
        await api.createFilter({
          name: draft.name,
          matchAll: draft.matchAll,
          conditions: draft.conditions,
          actions: draft.actions,
        })
        toast.success(t("filters.toast.created"))
      }
      setDialogOpen(false)
      await loadFilters()
    } catch {
      toast.error(t("filters.toast.saveFailed"))
    } finally {
      setSaving(false)
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
          <Button onClick={openCreate}>
            <Plus className="h-4 w-4 mr-1" />
            {t("filters.newFilter")}
          </Button>
        </div>
      </div>

      {loading ? (
        <div className="space-y-3">
          {[1, 2].map((i) => (
            <div key={i} className="rounded-lg border p-4">
              <Skeleton className="h-5 w-48" />
              <Skeleton className="mt-2 h-3 w-full" />
            </div>
          ))}
        </div>
      ) : filters.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <FilterIcon className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("filters.empty.title")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("filters.empty.description")}
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {filters.map((filter, index) => (
            <div key={filter.id} className="rounded-lg border bg-card p-4">
              <div className="flex items-start justify-between gap-4">
                <div className="flex flex-col">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6"
                    disabled={index === 0}
                    onClick={() => moveFilter(index, -1)}
                    title={t("filters.moveUp")}
                  >
                    <ArrowUp className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6"
                    disabled={index === filters.length - 1}
                    onClick={() => moveFilter(index, 1)}
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
                    {t(filter.matchAll ? "filters.summaryAll" : "filters.summaryAny", {
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
                    })}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Switch
                    checked={filter.enabled}
                    onCheckedChange={() => handleToggle(filter)}
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    onClick={() => openEdit(filter)}
                  >
                    <Pencil className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-destructive"
                    onClick={() => setDeleteTarget(filter)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editingId ? t("filters.editTitle") : t("filters.newFilter")}</DialogTitle>
            <DialogDescription>
              {t("filters.dialogDescription")}
            </DialogDescription>
          </DialogHeader>

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
              <div className="flex items-center justify-between">
                <Label>{t("filters.conditions")}</Label>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setDraft({ ...draft, conditions: [...draft.conditions, emptyCondition()] })
                  }
                >
                  <Plus className="h-4 w-4 mr-1" />
                  {t("common.add")}
                </Button>
              </div>
              {draft.conditions.map((cond, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={cond.field}
                    onValueChange={(v) =>
                      updateCondition(i, { field: v as FilterCondition["field"] })
                    }
                  >
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
                  <Select
                    value={cond.operator}
                    onValueChange={(v) =>
                      updateCondition(i, { operator: v as FilterCondition["operator"] })
                    }
                  >
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
                  {cond.field === "header" && (
                    <Input
                      className="w-[140px]"
                      placeholder={t("filters.headerNamePlaceholder")}
                      value={cond.headerName ?? ""}
                      onChange={(e) => updateCondition(i, { headerName: e.target.value })}
                    />
                  )}
                  <Input
                    className="min-w-[140px] flex-1"
                    placeholder={t("filters.valuePlaceholder")}
                    value={cond.value}
                    onChange={(e) => updateCondition(i, { value: e.target.value })}
                  />
                  {draft.conditions.length > 1 && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() =>
                        setDraft({
                          ...draft,
                          conditions: draft.conditions.filter((_, idx) => idx !== i),
                        })
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              ))}
            </div>

            {/* Actions */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>{t("common.actions")}</Label>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setDraft({ ...draft, actions: [...draft.actions, emptyAction()] })
                  }
                >
                  <Plus className="h-4 w-4 mr-1" />
                  {t("common.add")}
                </Button>
              </div>
              {draft.actions.map((action, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={action.type}
                    onValueChange={(v) =>
                      updateAction(i, { type: v as FilterAction["type"] })
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
                  {(action.type === "moveToFolder" || action.type === "copyToFolder") && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder={t("filters.targetFolderPlaceholder")}
                      value={action.target ?? ""}
                      onChange={(e) => updateAction(i, { target: e.target.value })}
                    />
                  )}
                  {FORWARD_TYPES.has(action.type) && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder={t("filters.destinationPlaceholder")}
                      value={action.forwardTo ?? ""}
                      onChange={(e) => updateAction(i, { forwardTo: e.target.value })}
                    />
                  )}
                  {action.type === "reject" && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder={t("filters.rejectionPlaceholder")}
                      value={action.message ?? ""}
                      onChange={(e) => updateAction(i, { message: e.target.value })}
                    />
                  )}
                  {action.type === "vacation" && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder={t("filters.autoReplyPlaceholder")}
                      value={action.message ?? ""}
                      onChange={(e) => updateAction(i, { message: e.target.value })}
                    />
                  )}
                  {(action.type === "addHeader" || action.type === "deleteHeader") && (
                    <Input
                      className="w-[150px]"
                      placeholder={t("filters.headerNamePlaceholder")}
                      value={action.headerName ?? ""}
                      onChange={(e) => updateAction(i, { headerName: e.target.value })}
                    />
                  )}
                  {action.type === "addHeader" && (
                    <Input
                      className="min-w-[120px] flex-1"
                      placeholder={t("filters.headerValuePlaceholder")}
                      value={action.headerValue ?? ""}
                      onChange={(e) => updateAction(i, { headerValue: e.target.value })}
                    />
                  )}
                  {action.type === "flag" && (
                    <>
                      <Input
                        className="w-[150px]"
                        placeholder={t("filters.flagNamePlaceholder")}
                        value={action.flagName ?? ""}
                        onChange={(e) => updateAction(i, { flagName: e.target.value })}
                      />
                      <label className="flex items-center gap-2 text-sm text-muted-foreground">
                        <Switch
                          checked={action.clearFlag ?? false}
                          onCheckedChange={(v) => updateAction(i, { clearFlag: v })}
                        />
                        {t("filters.clear")}
                      </label>
                    </>
                  )}
                  {draft.actions.length > 1 && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() =>
                        setDraft({
                          ...draft,
                          actions: draft.actions.filter((_, idx) => idx !== i),
                        })
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              ))}
            </div>
          </div>

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
