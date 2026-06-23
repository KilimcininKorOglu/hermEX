import { useState, useEffect, useCallback, type MouseEvent } from "react"
import { Clock, Trash2, RefreshCw, AlertTriangle } from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import api, { type ScheduledMailItem } from "@/utils/api"

export function ScheduledPage() {
  const { t } = useI18n()
  const [items, setItems] = useState<ScheduledMailItem[]>([])
  const [loading, setLoading] = useState(true)

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      setItems(await api.listScheduled())
    } catch {
      setItems([])
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const handleCancel = async (id: string, e: MouseEvent) => {
    e.stopPropagation()
    try {
      await api.cancelScheduled(id)
      setItems((prev) => prev.filter((m) => m.id !== id))
      toast.success(t("scheduled.canceled"))
    } catch {
      toast.error(t("scheduled.cancelFailed"))
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("nav.scheduled")}</h2>
        <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => load()}>
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
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-muted p-4">
              <Clock className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">{t("scheduled.empty")}</h3>
            <p className="text-sm text-muted-foreground">{t("scheduled.emptyHint")}</p>
          </div>
        ) : (
          <div className="divide-y">
            {items.map((m) => (
              <div key={m.id} className="group flex items-center gap-3 p-4 transition-colors hover:bg-accent/50">
                <Clock className="h-4 w-4 text-muted-foreground" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{m.subject || t("scheduled.noSubject")}</span>
                    {m.status === "failed" && (
                      <Badge variant="destructive" className="gap-1">
                        <AlertTriangle className="h-3 w-3" />
                        {t("scheduled.statusFailed")}
                      </Badge>
                    )}
                  </div>
                  <div className="flex items-center gap-2 text-sm text-muted-foreground">
                    <span className="truncate">{t("common.to")}: {m.to.join(", ") || "—"}</span>
                  </div>
                  {m.status === "failed" && m.error && (
                    <div className="truncate text-xs text-destructive">{m.error}</div>
                  )}
                </div>
                <span className="whitespace-nowrap text-sm text-muted-foreground" title={t("scheduled.sendAt")}>
                  {formatAbsolute(m.sendAt)}
                </span>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-destructive opacity-0 group-hover:opacity-100"
                  onClick={(e) => handleCancel(m.id, e)}
                  title={t("scheduled.cancel")}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
