import { useState, useEffect, useCallback } from "react"
import { useNavigate } from "react-router-dom"
import {
  MailOpen,
  RefreshCw,
  ChevronLeft,
  ChevronRight,
  Paperclip,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import { useMailEvents } from "@/utils/mailEvents"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"
import { useBulkSelection } from "@/hooks/useBulkSelection"
import { BulkActionBar } from "@/components/bulk-action-bar"

interface Email {
  id: string
  to: string
  toEmail: string
  subject: string
  preview: string
  date: string
  read: boolean
  starred: boolean
  hasAttachments: boolean
}

// splitAddress turns "Name <addr@x>" or "addr@x" into {name, email}.
function splitAddress(value: string): { name: string; email: string } {
  const parts = value.split("<")
  if (parts.length > 1) {
    return { name: parts[0].trim() || parts[1].replace(">", "").trim(), email: parts[1].replace(">", "").trim() }
  }
  return { name: value, email: value }
}

export function SentPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [emails, setEmails] = useState<Email[]>([])
  const sel = useBulkSelection()
  const [loading, setLoading] = useState(true)

  const loadSent = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const result = await api.getMail("sent")
      const mails = result.emails ?? []
      setEmails(mails.map((mail: Mail) => {
        const recipient = splitAddress((mail.to && mail.to[0]) || "")
        return {
          id: mail.id,
          to: recipient.name,
          toEmail: recipient.email,
          subject: mail.subject,
          preview: mail.preview,
          date: mail.date,
          read: mail.read,
          starred: mail.starred,
          hasAttachments: mail.hasAttachments,
        }
      }))
    } catch {
      setEmails([])
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadSent()
  }, [loadSent])

  // Realtime: silently refetch when the server pushes a mailbox change.
  useMailEvents(() => {
    loadSent(true).catch(() => undefined)
  })

  const allIds = emails.map((e) => e.id)

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Checkbox
            checked={sel.allSelected(allIds)}
            onCheckedChange={() => sel.toggleAll(allIds)}
          />
          <BulkActionBar ids={sel.ids} onClear={sel.clear} />
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          onClick={() => loadSent()}
          aria-label={t("common.refresh")}
          title={t("common.refresh")}
        >
          <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
        </Button>
      </div>

      <div className="rounded-lg border bg-card">
        {loading ? (
          <div className="divide-y">
            {[1, 2].map((i) => (
              <div key={i} className="flex items-start gap-4 p-4">
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
              <MailOpen className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">{t("sent.noSentEmails")}</h3>
            <p className="text-sm text-muted-foreground">
              {t("sent.emptyDescription")}
            </p>
          </div>
        ) : (
          <div className="divide-y">
            {emails.map((email) => (
              <div
                key={email.id}
                className="group flex cursor-pointer items-start gap-3 p-4 transition-colors hover:bg-accent/50"
                onClick={() => navigate(`/email/${email.id}`)}
              >
                <Checkbox
                  checked={sel.isSelected(email.id)}
                  onCheckedChange={() => sel.toggle(email.id)}
                  onClick={(e) => e.stopPropagation()}
                />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{t("common.to")}: {email.to}</span>
                    {email.hasAttachments && (
                      <Paperclip className="h-3 w-3 text-muted-foreground" />
                    )}
                  </div>
                  <div className="flex items-center gap-2 text-sm text-muted-foreground">
                    <span className="font-medium">{email.subject}</span>
                    <span className="truncate">— {email.preview}</span>
                  </div>
                </div>
                <span className="whitespace-nowrap text-sm text-muted-foreground">
                  {formatAbsolute(email.date)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">
          {t(emails.length !== 1 ? "sent.messagesCount" : "sent.messageCount", { count: String(emails.length) })}
        </span>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="icon" disabled aria-label={t("common.previous")}>
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button variant="outline" size="icon" disabled aria-label={t("common.next")}>
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
