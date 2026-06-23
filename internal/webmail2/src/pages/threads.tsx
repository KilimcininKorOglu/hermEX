import { useState, useEffect } from "react"
import { useNavigate } from "react-router-dom"
import { MessagesSquare, ChevronDown, ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"

interface ThreadGroup {
  key: string
  subject: string
  messages: Mail[]
  participants: string[]
  lastDate: string
  unread: number
}

// normalizeSubject strips repeated Re:/Fwd: prefixes so replies group together.
function normalizeSubject(subject: string): string {
  return subject.replace(/^(\s*(re|fwd|fw)\s*:\s*)+/i, "").trim()
}

export function ThreadsPage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [threads, setThreads] = useState<ThreadGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      setLoading(true)
      try {
        const res = await api.getMail("inbox")
        if (cancelled) return
        const mails = res.emails ?? []
        const map = new Map<string, Mail[]>()
        for (const m of mails) {
          const key = (normalizeSubject(m.subject) || "(no subject)").toLowerCase()
          const arr = map.get(key) ?? []
          arr.push(m)
          map.set(key, arr)
        }
        const groups: ThreadGroup[] = Array.from(map.values()).map((msgs) => ({
          key: (normalizeSubject(msgs[0].subject) || "(no subject)").toLowerCase(),
          subject: normalizeSubject(msgs[0].subject),
          messages: msgs,
          participants: Array.from(new Set(msgs.map((m) => m.from))),
          lastDate: msgs[msgs.length - 1]?.date ?? "",
          unread: msgs.filter((m) => !m.read).length,
        }))
        // Multi-message conversations first.
        groups.sort((a, b) => b.messages.length - a.messages.length)
        setThreads(groups)
      } catch (err) {
        console.error("Failed to load threads:", err)
        if (!cancelled) setThreads([])
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <MessagesSquare className="h-5 w-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{t("nav.conversations")}</h1>
      </div>

      <div className="rounded-lg border bg-card divide-y">
        {loading ? (
          [1, 2, 3].map((i) => (
            <div key={i} className="flex items-center gap-4 p-4">
              <Skeleton className="h-4 w-4" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-48" />
                <Skeleton className="h-3 w-full" />
              </div>
            </div>
          ))
        ) : threads.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-muted p-4">
              <MessagesSquare className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">{t("threads.noConversations")}</h3>
            <p className="text-sm text-muted-foreground">{t("threads.inboxEmpty")}</p>
          </div>
        ) : (
          threads.map((thread) => (
            <div key={thread.key}>
              <button
                className="flex w-full items-center gap-3 p-4 text-left transition-colors hover:bg-accent/50"
                onClick={() => setExpanded(expanded === thread.key ? null : thread.key)}
              >
                {expanded === thread.key ? (
                  <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
                ) : (
                  <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className={cn("truncate", thread.unread > 0 ? "font-semibold" : "font-medium")}>
                      {thread.subject || t("threads.noSubject")}
                    </span>
                    <Badge
                      variant="secondary"
                      className="text-[10px]"
                      title={t(thread.messages.length !== 1 ? "threads.messagesCount" : "threads.messageCount", {
                        count: String(thread.messages.length),
                      })}
                    >
                      {thread.messages.length}
                    </Badge>
                    {thread.unread > 0 && (
                      <Badge className="text-[10px]">
                        {t(thread.unread !== 1 ? "threads.newCountPlural" : "threads.newCount", {
                          count: String(thread.unread),
                        })}
                      </Badge>
                    )}
                  </div>
                  <p className="truncate text-xs text-muted-foreground">
                    {thread.participants.join(", ")}
                  </p>
                </div>
                <span className="shrink-0 whitespace-nowrap text-xs text-muted-foreground">
                  {thread.lastDate}
                </span>
              </button>

              {expanded === thread.key && (
                <div className="divide-y border-t bg-muted/20">
                  {thread.messages.map((m) => (
                    <button
                      key={m.id}
                      className="flex w-full items-center gap-3 py-2 pl-11 pr-4 text-left text-sm transition-colors hover:bg-accent/50"
                      onClick={() => navigate(`/email/${m.id}`)}
                    >
                      <span className={cn("truncate", !m.read && "font-medium")}>{m.from}</span>
                      <span className="ml-auto shrink-0 whitespace-nowrap text-xs text-muted-foreground">
                        {formatAbsolute(m.date)}
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}

export default ThreadsPage
