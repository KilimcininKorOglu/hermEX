import { useState, useEffect, useCallback } from "react"
import { useNavigate } from "react-router-dom"
import {
  AlertCircle,
  Trash2,
  Eraser,
  MoreHorizontal,
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

interface SpamEmail {
  id: string
  from: string
  fromEmail: string
  subject: string
  preview: string
  date: string
  read: boolean
}

function splitAddress(value: string): { name: string; email: string } {
  const parts = value.split("<")
  if (parts.length > 1) {
    const email = parts[1].replace(">", "").trim()
    return { name: parts[0].trim() || email, email }
  }
  return { name: value, email: value }
}

export function SpamPage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [loading, setLoading] = useState(true)
  const [emails, setEmails] = useState<SpamEmail[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const loadSpam = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const result = await api.getMail("spam")
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
        }
      }))
    } catch {
      setEmails([])
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadSpam()
  }, [loadSpam])

  // Realtime: silently refetch when the server pushes a mailbox change.
  useMailEvents(() => {
    loadSpam(true).catch(() => undefined)
  })

  const toggleSelect = (id: string) => {
    const newSelected = new Set(selected)
    if (newSelected.has(id)) {
      newSelected.delete(id)
    } else {
      newSelected.add(id)
    }
    setSelected(newSelected)
  }

  const handleDelete = async () => {
    const ids = Array.from(selected)
    try {
      await Promise.all(ids.map((id) => api.deleteMail(id)))
      toast.success(
        ids.length === 1
          ? t("spam.deletedOne", { count: String(ids.length) })
          : t("spam.deletedMany", { count: String(ids.length) })
      )
      setSelected(new Set())
      await loadSpam()
    } catch {
      toast.error(t("spam.deleteFailed"))
    }
  }

  // handleEmptySpam permanently discards every message in the Junk folder in one
  // server-side call (not a loop over the loaded page).
  const handleEmptySpam = async () => {
    try {
      await api.emptyFolder("spam")
      toast.success(t("spam.emptied"))
      setSelected(new Set())
      await loadSpam()
    } catch {
      toast.error(t("spam.emptyFailed"))
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <AlertCircle className="h-5 w-5 text-red-500" />
          <h1 className="text-xl font-semibold">{t("nav.spam")}</h1>
          <Badge variant="destructive">{emails.length}</Badge>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="text-destructive"
            onClick={handleDelete}
            disabled={selected.size === 0 || loading}
          >
            <Trash2 className="h-4 w-4 mr-1" />
            {t("common.delete")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={handleEmptySpam}
            disabled={emails.length === 0 || loading}
          >
            <Eraser className="h-4 w-4 mr-1" />
            {t("spam.empty")}
          </Button>
        </div>
      </div>

      <div className="rounded-lg border border-destructive/20 bg-destructive/10 p-4">
        <p className="text-sm text-destructive">
          {t("spam.description")}
        </p>
      </div>

      {loading ? (
        <div className="space-y-4">
          {[1, 2, 3].map((i) => (
            <div key={i} className="flex items-start gap-4 p-4 rounded-lg border">
              <Skeleton className="h-4 w-4" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-64" />
                <Skeleton className="h-3 w-full" />
              </div>
            </div>
          ))}
        </div>
      ) : emails.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <AlertCircle className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("spam.noSpam")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("spam.emptyDescription")}
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
                checked={selected.has(email.id)}
                onCheckedChange={() => toggleSelect(email.id)}
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  {!email.read && (
                    <span className="h-2 w-2 rounded-full bg-red-500 shrink-0" />
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
                  <DropdownMenuItem onClick={handleDelete} className="text-destructive">
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
