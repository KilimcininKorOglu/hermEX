import { useState, useEffect, useCallback } from "react"
import { useParams, useNavigate } from "react-router-dom"
import { Search } from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import api from "@/utils/api"

interface SavedSearchRow {
  id: string
  from: string
  subject: string
  preview: string
  date: string
  folder: string
  read: boolean
}

/**
 * SavedSearchPage renders the live results of a persistent saved search. It
 * resolves the folder's display name and runs the server-side definition, so
 * the listing always reflects messages currently matching the saved criteria.
 */
export function SavedSearchPage() {
  const { t } = useI18n()
  const { id } = useParams()
  const navigate = useNavigate()
  const [loading, setLoading] = useState(true)
  const [rows, setRows] = useState<SavedSearchRow[]>([])
  const [name, setName] = useState("")

  const load = useCallback(async () => {
    if (!id) return
    setLoading(true)
    try {
      const [res, list] = await Promise.all([
        api.getSearchFolderResults(id),
        api.listSearchFolders(),
      ])
      const sf = (list.search_folders ?? []).find((s) => s.id === id)
      setName(sf?.name ?? "")
      const mapped = (res.emails ?? []).map((e) => ({
        id: e.id,
        from: e.fromName || e.from,
        subject: e.subject,
        preview: e.preview || e.body?.substring(0, 100) || "",
        date: e.date,
        folder: e.folder,
        read: e.read,
      }))
      setRows(mapped)
    } catch {
      setRows([])
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="flex items-center gap-2 mb-4">
        <Search className="h-5 w-5 text-muted-foreground" />
        <h1 className="text-xl font-semibold">{name || t("savedSearch.title")}</h1>
      </div>

      {loading ? (
        <div className="space-y-2">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <div className="rounded-lg border bg-card p-10 text-center text-muted-foreground">
          {t("savedSearch.empty")}
        </div>
      ) : (
        <div>
          <div className="text-sm text-muted-foreground mb-3">
            {rows.length === 1
              ? t("savedSearch.resultCount", { count: String(rows.length) })
              : t("savedSearch.resultCountPlural", { count: String(rows.length) })}
          </div>
          <div className="rounded-lg border bg-card divide-y">
            {rows.map((email) => (
              <div
                key={email.id}
                className={cn(
                  "flex items-start gap-3 p-4 cursor-pointer transition-colors hover:bg-accent/50",
                  !email.read && "bg-accent/10"
                )}
                onClick={() => navigate(`/email/${email.id}`)}
              >
                <Checkbox className="mt-1" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    {!email.read && <span className="h-2 w-2 rounded-full bg-primary shrink-0" />}
                    <span className="font-medium">{email.from}</span>
                    <Badge variant="outline" className="text-[10px]">
                      {email.folder}
                    </Badge>
                  </div>
                  <div className="text-sm">
                    <span className="font-medium">{email.subject}</span>
                    <span className="text-muted-foreground"> — {email.preview}</span>
                  </div>
                  <div className="text-xs text-muted-foreground mt-1">{email.date}</div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
