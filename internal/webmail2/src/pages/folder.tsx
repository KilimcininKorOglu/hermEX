import { useState, useEffect, useCallback } from "react"
import { useParams, useNavigate } from "react-router-dom"
import {
  FolderOpen,
  Trash2,
  MoreHorizontal,
  Star,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import { useMailEvents } from "@/utils/mailEvents"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"
import { useBulkSelection } from "@/hooks/useBulkSelection"
import { BulkActionBar, type BulkAction } from "@/components/bulk-action-bar"

interface FolderEmail {
  id: string
  from: string
  fromEmail: string
  subject: string
  preview: string
  date: string
  read: boolean
  starred: boolean
}

// splitAddress turns "Name <addr@x>" or "addr@x" into {name, email}.
function splitAddress(value: string): { name: string; email: string } {
  const parts = value.split("<")
  if (parts.length > 1) {
    const email = parts[1].replace(">", "").trim()
    return { name: parts[0].trim() || email, email }
  }
  return { name: value, email: value }
}

export function FolderPage() {
  const { t } = useI18n()
  const { type } = useParams()
  const navigate = useNavigate()
  const [loading, setLoading] = useState(true)
  const [unavailable, setUnavailable] = useState(false)
  const [emails, setEmails] = useState<FolderEmail[]>([])
  const sel = useBulkSelection()

  // The folder name comes straight from the route; it maps to a real mailbox.
  const pageTitle = type ? type.charAt(0).toUpperCase() + type.slice(1) : t("folder.title")
  const pageColor = "text-muted-foreground"

  const loadFolder = useCallback(async (silent = false) => {
    if (!type) return
    if (!silent) setLoading(true)
    setUnavailable(false)
    try {
      const result = await api.getMail(type)
      const mails = result.emails ?? []
      setEmails(mails.map((mail: Mail) => {
        const sender = splitAddress(mail.from || "")
        return {
          id: mail.id,
          from: sender.name,
          fromEmail: sender.email,
          subject: mail.subject,
          preview: mail.preview,
          date: mail.date,
          read: mail.read,
          starred: mail.starred,
        }
      }))
    } catch {
      setEmails([])
      setUnavailable(true)
    } finally {
      if (!silent) setLoading(false)
    }
  }, [type])

  useEffect(() => {
    loadFolder()
  }, [loadFolder])

  // Realtime: silently refetch this folder when the server pushes a change.
  useMailEvents(() => {
    loadFolder(true).catch(() => undefined)
  })

  const handleDelete = async () => {
    const ids = sel.ids
    try {
      await Promise.all(ids.map((id) => api.deleteMail(id)))
      toast.success(t(ids.length !== 1 ? "folder.messagesDeletedCount" : "folder.messageDeletedCount", { count: String(ids.length) }))
      sel.clear()
      await loadFolder()
    } catch {
      toast.error(t("folder.deleteFailed"))
    }
  }

  const bulkActions: BulkAction[] = [
    { key: "delete", label: t("common.remove"), icon: Trash2, onClick: handleDelete, destructive: true },
  ]

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <FolderOpen className={cn("h-5 w-5", pageColor)} />
          <h1 className="text-xl font-semibold">{pageTitle}</h1>
          <Badge variant="secondary">{emails.length}</Badge>
        </div>
        <BulkActionBar ids={sel.ids} actions={bulkActions} onClear={sel.clear} />
      </div>

      {loading ? (
        <div className="space-y-4">
          {[1, 2].map((i) => (
            <div key={i} className="flex items-start gap-4 p-4 rounded-lg border">
              <Skeleton className="h-4 w-4" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-64" />
                <Skeleton className="h-3 w-full" />
              </div>
            </div>
          ))}
        </div>
      ) : unavailable ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <FolderOpen className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("folder.unavailableTitle", { name: pageTitle })}</h3>
          <p className="text-sm text-muted-foreground">
            {t("folder.unavailableDescription")}
          </p>
        </div>
      ) : emails.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <FolderOpen className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("folder.emptyTitle", { name: pageTitle })}</h3>
          <p className="text-sm text-muted-foreground">
            {t("folder.emptyDescription")}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border bg-card divide-y">
          {emails.map((email) => (
            <div
              key={email.id}
              className={cn(
                "flex items-start gap-3 p-4 cursor-pointer transition-colors hover:bg-accent/50",
                !email.read && "bg-accent/10"
              )}
              onClick={() => navigate(`/email/${email.id}`)}
            >
              <Checkbox
                className="mt-1"
                checked={sel.isSelected(email.id)}
                onClick={(e) => e.stopPropagation()}
                onCheckedChange={() => sel.toggle(email.id)}
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  {email.starred && <Star className="h-4 w-4 fill-amber-400 text-amber-400" />}
                  {!email.read && (
                    <span className="h-2 w-2 rounded-full bg-primary shrink-0" />
                  )}
                  <span className="font-medium">{email.from}</span>
                </div>
                <div className="text-sm">
                  <span className="font-medium">{email.subject}</span>
                  <span className="text-muted-foreground"> — {email.preview}</span>
                </div>
                <div className="text-xs text-muted-foreground mt-1">
                  {formatAbsolute(email.date)}
                </div>
              </div>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <MoreHorizontal className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem
                    className="text-destructive"
                    onClick={async (e) => {
                      e.stopPropagation()
                      try {
                        await api.deleteMail(email.id)
                        toast.success(t("folder.messageDeleted"))
                        await loadFolder()
                      } catch {
                        toast.error(t("folder.deleteMessageFailed"))
                      }
                    }}
                  >
                    <Trash2 className="h-4 w-4 mr-2" />
                    {t("common.delete")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
