import { useState, useEffect } from "react"
import { useNavigate } from "react-router-dom"
import { Users, ChevronRight, Mail } from "lucide-react"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"
import { Skeleton } from "@/components/ui/skeleton"
import api, { SharedMailbox } from "@/utils/api"

export function SharedPage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { switchMailbox } = useMailbox()
  const [mailboxes, setMailboxes] = useState<SharedMailbox[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    api.getSharedMailboxes()
      .then((res) => {
        if (!cancelled) setMailboxes(res.shared_mailboxes ?? [])
      })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  const openMailbox = (mb: SharedMailbox) => {
    switchMailbox(mb.mailbox, mb.owner)
    navigate("/inbox")
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-3 border-b px-4 py-3">
        <Users className="h-5 w-5 text-muted-foreground" />
        <h1 className="font-semibold">{t("shared.title")}</h1>
      </div>

      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="p-4 space-y-2">
            {[1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-14 w-full rounded-md" />
            ))}
          </div>
        ) : mailboxes.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
            <Users className="h-12 w-12 mb-3 opacity-30" />
            <p className="text-sm">{t("shared.empty")}</p>
          </div>
        ) : (
          <div className="p-2">
            {mailboxes.map((mb) => (
              <button
                key={mb.owner + "/" + mb.mailbox}
                onClick={() => openMailbox(mb)}
                className="w-full flex items-center gap-3 px-3 py-3 rounded-md hover:bg-accent transition-colors text-left"
              >
                <Mail className="h-5 w-5 text-muted-foreground shrink-0" />
                <div className="flex-1 min-w-0">
                  <p className="font-medium truncate">{mb.mailbox}</p>
                  <p className="text-xs text-muted-foreground truncate">
                    {mb.owner && mb.owner !== mb.mailbox
                      ? t("shared.sharedBy", { owner: mb.owner })
                      : t("shared.sharedFolder")}
                  </p>
                </div>
                {mb.rights && (
                  <span className="text-xs rounded px-1.5 py-0.5 bg-muted text-muted-foreground shrink-0">
                    {mb.rights}
                  </span>
                )}
                <ChevronRight className="h-4 w-4 text-muted-foreground shrink-0" />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
