import { useState, useEffect, useCallback, type MouseEvent } from "react"
import { useNavigate } from "react-router-dom"
import {
  FileText,
  Trash2,
  RefreshCw,
  ChevronLeft,
  ChevronRight,
  Edit,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import { useMailEvents } from "@/utils/mailEvents"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import { toast } from "sonner"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"

interface Draft {
  id: string
  to: string
  subject: string
  preview: string
  date: string
}

export function DraftsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [drafts, setDrafts] = useState<Draft[]>([])
  const [selectedDrafts, setSelectedDrafts] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(true)

  const loadDrafts = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const result = await api.getMail("drafts")
      const mails = result.emails ?? []
      setDrafts(mails.map((mail: Mail) => ({
        id: mail.id,
        to: (mail.to && mail.to[0]) || "",
        subject: mail.subject,
        preview: mail.preview,
        date: mail.date,
      })))
    } catch {
      setDrafts([])
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadDrafts()
  }, [loadDrafts])

  // Realtime: silently refetch when the server pushes a mailbox change.
  useMailEvents(() => {
    loadDrafts(true).catch(() => undefined)
  })

  const toggleSelectAll = () => {
    if (selectedDrafts.size === drafts.length) {
      setSelectedDrafts(new Set())
    } else {
      setSelectedDrafts(new Set(drafts.map((e) => e.id)))
    }
  }

  const toggleSelect = (id: string) => {
    const newSelected = new Set(selectedDrafts)
    if (newSelected.has(id)) {
      newSelected.delete(id)
    } else {
      newSelected.add(id)
    }
    setSelectedDrafts(newSelected)
  }

  const handleDelete = async () => {
    const ids = Array.from(selectedDrafts)
    try {
      await Promise.all(ids.map((id) => api.deleteMail(id)))
      toast.success(t(ids.length !== 1 ? "drafts.draftsDeleted" : "drafts.draftDeleted", { count: String(ids.length) }))
      setSelectedDrafts(new Set())
      await loadDrafts()
    } catch {
      toast.error(t("drafts.deleteFailed"))
    }
  }

  const handleEdit = (id: string) => {
    navigate(`/compose?draft=${id}`)
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Checkbox
            checked={selectedDrafts.size === drafts.length && drafts.length > 0}
            onCheckedChange={toggleSelectAll}
          />
          {selectedDrafts.size > 0 ? (
            <>
              <span className="text-sm text-muted-foreground">
                {t("drafts.selectedCount", { count: String(selectedDrafts.size) })}
              </span>
              <Separator orientation="vertical" className="h-4" />
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-destructive"
                onClick={handleDelete}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </>
          ) : null}
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          onClick={() => loadDrafts()}
        >
          <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
        </Button>
      </div>

      <div className="rounded-lg border bg-card">
        {loading ? (
          <div className="divide-y">
            {[1].map((i) => (
              <div key={i} className="flex items-center gap-4 p-4">
                <Skeleton className="h-4 w-4" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-32" />
                  <Skeleton className="h-3 w-full" />
                </div>
              </div>
            ))}
          </div>
        ) : drafts.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-muted p-4">
              <FileText className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">{t("drafts.noDrafts")}</h3>
            <p className="text-sm text-muted-foreground">
              {t("drafts.noDraftsHint")}
            </p>
          </div>
        ) : (
          <div className="divide-y">
            {drafts.map((draft) => (
              <div
                key={draft.id}
                className="group flex cursor-pointer items-center gap-3 p-4 transition-colors hover:bg-accent/50"
                onClick={() => handleEdit(draft.id)}
              >
                <Checkbox
                  checked={selectedDrafts.has(draft.id)}
                  onCheckedChange={() => toggleSelect(draft.id)}
                  onClick={(e: MouseEvent) => e.stopPropagation()}
                />
                <FileText className="h-4 w-4 text-muted-foreground" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">
                      {draft.subject || t("drafts.noSubject")}
                    </span>
                  </div>
                  <div className="flex items-center gap-2 text-sm text-muted-foreground">
                    <span className="truncate">{t("common.to")}: {draft.to || t("drafts.noRecipient")}</span>
                    <span className="truncate">— {draft.preview}</span>
                  </div>
                </div>
                <span className="whitespace-nowrap text-sm text-muted-foreground">
                  {formatAbsolute(draft.date)}
                </span>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 opacity-0 group-hover:opacity-100"
                  onClick={(e) => {
                    e.stopPropagation()
                    handleEdit(draft.id)
                  }}
                >
                  <Edit className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">
          {t(drafts.length !== 1 ? "drafts.draftsCount" : "drafts.draftCount", { count: String(drafts.length) })}
        </span>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="icon" disabled>
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button variant="outline" size="icon" disabled>
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
