import { useState, useEffect, useMemo, useRef } from "react"
import { useNavigate } from "react-router-dom"
import { useMailbox } from "@/contexts/MailboxContext"
import {
  Star,
  Archive,
  Trash2,
  MailOpen,
  CheckCheck,
  Upload,
  Paperclip,
  RefreshCw,
  Loader2,
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  ChevronRight as ChevronRightIcon,
  Filter,
  MoreHorizontal,
  List,
  LayoutGrid,
  ArrowUpDown,
  MessagesSquare,
  PanelRight,
} from "lucide-react"
import { WelcomeBanner } from "@/components/welcome-banner"
import { EmailDetailPage } from "@/pages/email-detail"
import { useI18n } from "@/hooks/useI18n"
import { formatAbsolute } from "@/utils/date"
import { getCookie, setCookie } from "@/utils/cookies"
import { getShortcutMode } from "@/utils/shortcutMode"
import { getMailColumns } from "@/utils/mailListColumns"
import type { MailListColumns } from "@/utils/api"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "sonner"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"
import { useBulkSelection } from "@/hooks/useBulkSelection"
import { BulkActionBar, type BulkAction } from "@/components/bulk-action-bar"

interface Email {
  id: string
  from: string
  fromEmail: string
  subject: string
  preview: string
  date: string
  read: boolean
  starred: boolean
  hasAttachments: boolean
  folder: string
  labels: string[]
  importance?: string
  size: number
  followupStatus?: number
}

// toEmail projects an API Mail row onto the inbox row model.
function toEmail(mail: Mail): Email {
  return {
    id: mail.id,
    from: mail.fromName || mail.from,
    fromEmail: mail.from,
    subject: mail.subject,
    preview: mail.preview,
    date: mail.date,
    read: mail.read,
    starred: mail.starred,
    hasAttachments: mail.hasAttachments,
    folder: mail.folder.toLowerCase(),
    labels: mail.labels ?? [],
    importance: mail.importance,
    size: mail.size,
    followupStatus: mail.followupStatus,
  }
}

// formatSize renders a message size as a compact human-readable string
// (reference the mail-list Size column). Bytes below 1 KB show as "< 1 KB".
function formatSize(bytes: number): string {
  if (!bytes || bytes < 1024) return "< 1 KB"
  const kb = bytes / 1024
  if (kb < 1024) return `${Math.round(kb)} KB`
  return `${(kb / 1024).toFixed(1)} MB`
}

interface ThreadGroup {
  key: string
  subject: string
  messages: Email[]
  participants: string[]
  lastDate: string
  unread: number
}

type ViewMode = "list" | "compact"
type ViewType = "list" | "conversations"
type SortOption = "date" | "from" | "subject"
type SortDir = "asc" | "desc"

interface InboxPageProps {
  folder?: string
}

export function InboxPage({ folder = "inbox" }: InboxPageProps) {
  const navigate = useNavigate()
  const { t } = useI18n()
  // Inbox data comes from the shared MailboxContext so the sidebar unread
  // badge and header notifications stay in sync with actions taken here.
  const { inboxEmails, inboxUnread, inboxTotal, inboxPageSize, inboxLoading, inboxNavMode, inboxHasMore, inboxLoadingMore, loadMoreInbox, setInboxQuery, refreshInbox, patchInbox, removeFromInbox } = useMailbox()
  const sel = useBulkSelection()
  const [activeFilter, setActiveFilter] = useState("all")
  const loading = inboxLoading
  // Row density (list = comfortable, compact = dense). The toolbar toggle persists
  // the choice in a cookie so it survives a reload (the old density preference).
  const [viewMode, setViewModeState] = useState<ViewMode>(() =>
    getCookie("hermex-view-mode") === "compact" ? "compact" : "list"
  )
  const setViewMode = (m: ViewMode) => {
    setViewModeState(m)
    setCookie("hermex-view-mode", m)
  }
  // Preview pane: "none" opens a message on its own page; "right" reads it inline
  // beside the list. The choice persists in a cookie (client UI preference).
  const [previewPane, setPreviewPane] = useState<"none" | "right">(() =>
    getCookie("hermex-preview-pane") === "right" ? "right" : "none"
  )
  // Message-list column visibility (reference MailGridColumnModel). The DB-backed
  // set is mirrored to a cookie so the first render reads it synchronously; a
  // change in Settings fires "mail-columns-changed" to re-read live.
  const [columns, setColumns] = useState<MailListColumns>(() => getMailColumns())
  useEffect(() => {
    const onCols = () => setColumns(getMailColumns())
    document.addEventListener("mail-columns-changed", onCols)
    return () => document.removeEventListener("mail-columns-changed", onCols)
  }, [])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const setPreview = (p: "none" | "right") => {
    setPreviewPane(p)
    setCookie("hermex-preview-pane", p)
    if (p === "none") setSelectedId(null)
  }
  const [viewType, setViewType] = useState<ViewType>("list")
  const [expandedThreads, setExpandedThreads] = useState<Set<string>>(new Set())
  const [sortBy, setSortBy] = useState<SortOption>("date")
  const [sortDir, setSortDir] = useState<SortDir>("desc")
  const [page, setPage] = useState(0)
  // Sentinel row at the bottom of the list; when it scrolls into view in
  // infinite mode, the observer asks the context to append the next block.
  const loadMoreRef = useRef<HTMLDivElement | null>(null)

  // Reset to the first page when the folder or filter changes.
  useEffect(() => {
    setPage(0)
  }, [folder, activeFilter])

  // Infinite-scroll trigger: observe the sentinel and load the next block when
  // it becomes visible. Re-armed whenever the callback identity or has-more
  // state changes so it stops firing once the folder is fully loaded.
  useEffect(() => {
    if (inboxNavMode !== "infinite" || !inboxHasMore) return
    const el = loadMoreRef.current
    if (!el) return
    const obs = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadMoreInbox()
      },
      { rootMargin: "400px" }
    )
    obs.observe(el)
    return () => obs.disconnect()
  }, [inboxNavMode, inboxHasMore, loadMoreInbox])

  // Push the folder/filter/sort/page selection to the shared inbox query, which
  // refetches the matching server page. "starred" is a filter, not a real folder.
  useEffect(() => {
    setInboxQuery({
      filter: folder === "starred" ? "starred" : activeFilter,
      sort: sortBy,
      dir: sortDir,
      page,
    })
  }, [folder, activeFilter, sortBy, sortDir, page, setInboxQuery])

  // A mail moved from elsewhere (e.g. drag-drop onto a sidebar folder) fires the
  // "hermex:mail-changed" event; refresh the current folder so it disappears.
  useEffect(() => {
    const onChanged = () => refreshInbox()
    window.addEventListener("hermex:mail-changed", onChanged)
    return () => window.removeEventListener("hermex:mail-changed", onChanged)
  }, [refreshInbox])

  // The welcome banner stays dismissed across visits: its closed state lives in a
  // client-readable cookie (the web UI uses cookies, not localStorage).
  const [showWelcome, setShowWelcome] = useState(() => getCookie("hermex-welcome-dismissed") !== "1")

  // Derive the displayed list from the shared inbox state. The starred view is
  // the same inbox dataset filtered to flagged messages.
  // inboxEmails is already the server page for the current folder/filter/sort.
  const emails: Email[] = useMemo(() => inboxEmails.map(toEmail), [inboxEmails])

  // j/k list navigation: j → next email, k → previous, Enter → open. Ignored while
  // typing in an input/textarea so ordinary text entry never hijacks the keys.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (getShortcutMode() === "off") return
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (emails.length === 0) return
      const idx = selectedId ? emails.findIndex((em) => em.id === selectedId) : -1
      if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault()
        const next = idx < 0 ? 0 : Math.min(idx + 1, emails.length - 1)
        setSelectedId(emails[next].id)
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault()
        const prev = idx < 0 ? 0 : Math.max(idx - 1, 0)
        setSelectedId(emails[prev].id)
      } else if (e.key === "Enter" && selectedId) {
        e.preventDefault()
        navigate(`/email/${selectedId}`)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [emails, selectedId, navigate])

  // Conversations are grouped server-side; the list view is server-paged so the
  // current page alone is not enough. Fetch the grouped inbox when the view opens.
  const [threadGroups, setThreadGroups] = useState<ThreadGroup[]>([])
  useEffect(() => {
    if (viewType !== "conversations") return
    let cancelled = false
    api.getThreads()
      .then((res) => {
        if (cancelled) return
        setThreadGroups((res.threads ?? []).map((th) => ({
          key: th.key,
          subject: th.subject,
          messages: th.messages.map(toEmail),
          participants: th.participants,
          lastDate: th.lastDate,
          unread: th.unread,
        })))
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [viewType])

  const toggleThread = (key: string) => {
    setExpandedThreads((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const allIds = emails.map((e) => e.id)

  const toggleStar = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    const email = emails.find((em) => em.id === id)
    if (!email) return
    const next = !email.starred
    try {
      await api.setFlag(id, "\\Flagged", next)
      patchInbox([id], { starred: next })
    } catch (err) {
      console.error("Failed to update star:", err)
      toast.error(t("inbox.failedToUpdateStar"))
    }
  }

  const markAsRead = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    try {
      await api.setFlag(id, "\\Seen", true)
      patchInbox([id], { read: true })
    } catch (err) {
      console.error("Failed to mark message as read:", err)
      toast.error(t("inbox.failedToMarkAsRead"))
    }
  }

  const handleRefresh = async () => {
    await refreshInbox()
    toast.success(t("inbox.inboxRefreshed"))
  }

  const archiveEmails = async (ids: string[]) => {
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.moveMail(id, "archive")))
      removeFromInbox(ids)
      sel.clear()
      toast.success(t(ids.length !== 1 ? "inbox.messagesArchived" : "inbox.messageArchived", { count: String(ids.length) }))
    } catch (err) {
      console.error("Failed to archive messages:", err)
      toast.error(t("inbox.failedToArchive"))
    }
  }

  const handleArchive = () => archiveEmails(sel.ids)

  const deleteEmails = async (ids: string[]) => {
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.deleteMail(id)))
      removeFromInbox(ids)
      sel.clear()
      toast.success(t(ids.length !== 1 ? "inbox.messagesMovedToTrash" : "inbox.messageMovedToTrash", { count: String(ids.length) }))
    } catch (err) {
      console.error("Failed to delete messages:", err)
      toast.error(t("inbox.failedToDelete"))
    }
  }

  const handleDelete = () => deleteEmails(sel.ids)

  const handleMarkRead = async () => {
    const ids = sel.ids
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.setFlag(id, "\\Seen", true)))
      patchInbox(ids, { read: true })
      sel.clear()
      toast.success(t(ids.length !== 1 ? "inbox.messagesMarkedAsRead" : "inbox.messageMarkedAsRead", { count: String(ids.length) }))
    } catch (err) {
      console.error("Failed to mark messages as read:", err)
      toast.error(t("inbox.failedToMarkAsRead"))
    }
  }

  const bulkActions: BulkAction[] = [
    { key: "archive", label: t("common.archive"), icon: Archive, onClick: handleArchive },
    { key: "markRead", label: t("common.markRead"), icon: MailOpen, onClick: handleMarkRead },
    { key: "delete", label: t("common.delete"), icon: Trash2, onClick: handleDelete, destructive: true },
  ]

  const handleMarkAllRead = async () => {
    try {
      const { marked } = await api.markAllRead(folder)
      refreshInbox()
      toast.success(t("inbox.allMarkedAsRead", { count: String(marked ?? 0) }))
    } catch (err) {
      console.error("Failed to mark all as read:", err)
      toast.error(t("inbox.failedToMarkAsRead"))
    }
  }

  const importInputRef = useRef<HTMLInputElement>(null)
  const onImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ""
    if (!file) return
    try {
      const dataUrl = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(reader.result as string)
        reader.onerror = () => reject(reader.error)
        reader.readAsDataURL(file)
      })
      const base64 = dataUrl.split(",")[1] ?? ""
      await api.importEml(base64, folder === "starred" ? "inbox" : folder)
      refreshInbox()
      toast.success(t("inbox.imported"))
    } catch {
      toast.error(t("inbox.importFailed"))
    }
  }

  // The server already filtered, sorted, and paged this set, so emails IS the
  // current page; the pager and badge use the whole-folder counts from context.
  const unreadCount = inboxUnread
  const totalPages = Math.max(1, Math.ceil(inboxTotal / inboxPageSize))
  const currentPage = Math.min(page, totalPages - 1)
  const pageEmails = emails

  const EmailRow = ({ email }: { email: Email }) => (
    <div
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData("text/x-hermex-mail", email.id)
        e.dataTransfer.effectAllowed = "move"
      }}
      className={cn(
        "group flex cursor-pointer items-center gap-3 transition-all duration-200",
        viewMode === "list" ? "p-4 hover:bg-accent/50" : "p-2 hover:bg-accent/50",
        !email.read && viewMode === "list" && "bg-accent/5",
        sel.isSelected(email.id) && "bg-primary/5",
        previewPane === "right" && selectedId === email.id && "bg-primary/10"
      )}
      onClick={() => {
        if (previewPane === "right") setSelectedId(email.id)
        else navigate(`/email/${email.id}`)
      }}
    >
      <Checkbox
        checked={sel.isSelected(email.id)}
        onCheckedChange={() => sel.toggle(email.id)}
        onClick={(e) => e.stopPropagation()}
      />

      <Button
        variant="ghost"
        size="icon"
        className={cn(
          "h-8 w-8 shrink-0 transition-colors",
          email.starred ? "text-amber-500" : "text-muted-foreground hover:text-foreground"
        )}
        onClick={(e) => toggleStar(email.id, e)}
      >
        <Star className={cn("h-4 w-4", email.starred && "fill-current")} />
      </Button>

      <div className={cn("flex-1 min-w-0", viewMode === "compact" && "flex items-center gap-4")}>
        <div className="flex items-center gap-2">
          <span className={cn("text-sm", !email.read ? "font-semibold" : "font-normal")}>
            {viewMode === "list" ? email.from : email.from.split(" ")[0]}
          </span>
          {columns.categories && email.labels.slice(0, viewMode === "compact" ? 0 : 1).map((label) => (
            <Badge key={label} variant="secondary" className="text-[10px] px-1.5 py-0">
              {label}
            </Badge>
          ))}
        </div>
        {viewMode === "list" && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <span className={cn(!email.read && "text-foreground font-medium")}>
              {email.subject}
            </span>
            {columns.preview && <span className="truncate">— {email.preview}</span>}
          </div>
        )}
      </div>

      <div className={cn("flex items-center gap-2 shrink-0", viewMode === "compact" && "flex-row-reverse")}>
        {columns.attachment && email.hasAttachments && (
          <Paperclip className="h-4 w-4 text-muted-foreground" />
        )}
        {columns.importance && email.importance === "high" && (
          <span className="text-red-500 font-bold text-xs" title={t("compose.importanceHigh")}>!</span>
        )}
        {columns.importance && email.importance === "low" && (
          <span className="text-muted-foreground text-xs" title={t("compose.importanceLow")}>↓</span>
        )}
        {columns.flag && email.followupStatus === 2 && (
          <span className="text-red-500 text-xs" title={t("inbox.columns.flag")}>⚑</span>
        )}
        {columns.size && (
          <span className="text-xs text-muted-foreground whitespace-nowrap tabular-nums">
            {formatSize(email.size)}
          </span>
        )}
        {!email.read && viewMode === "list" && (
          <span className="h-2 w-2 rounded-full bg-primary" />
        )}
        <span className={cn(
          "text-xs text-muted-foreground whitespace-nowrap",
          viewMode === "compact" && "w-12 text-right"
        )}>
          {formatAbsolute(email.date)}
        </span>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 opacity-0 group-hover:opacity-100"
              onClick={(e) => e.stopPropagation()}
            >
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={(e) => markAsRead(email.id, e)}>
              <MailOpen className="mr-2 h-4 w-4" />
              {t("common.markRead")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={(e) => toggleStar(email.id, e)}>
              <Star className={cn("mr-2 h-4 w-4", email.starred && "fill-current")} />
              {email.starred ? t("inbox.removeStar") : t("inbox.addStar")}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={(e) => {
                e.stopPropagation()
                archiveEmails([email.id])
              }}
            >
              <Archive className="mr-2 h-4 w-4" />
              {t("common.archive")}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="text-destructive"
              onClick={(e) => {
                e.stopPropagation()
                deleteEmails([email.id])
              }}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  )

  return (
    <div className="space-y-4">
      {showWelcome && folder === "inbox" && (
        <WelcomeBanner onDismiss={() => { setCookie("hermex-welcome-dismissed", "1"); setShowWelcome(false) }} />
      )}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Checkbox
            checked={sel.allSelected(allIds)}
            onCheckedChange={() => sel.toggleAll(allIds)}
          />

          {sel.count > 0 ? (
            <BulkActionBar ids={sel.ids} actions={bulkActions} onClear={sel.clear} />
          ) : (
            <div className="flex items-center gap-1">
              <input
                ref={importInputRef}
                type="file"
                accept=".eml,message/rfc822"
                className="hidden"
                onChange={onImportFile}
              />
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={handleMarkAllRead} title={t("inbox.markAllRead")}>
                <CheckCheck className="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => importInputRef.current?.click()} title={t("inbox.import")}>
                <Upload className="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={handleRefresh}>
                <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
              </Button>
            </div>
          )}

          {unreadCount > 0 && activeFilter === "all" && (
            <Badge variant="secondary" className="ml-2">
              {t("inbox.unreadCount", { count: String(unreadCount) })}
            </Badge>
          )}
        </div>

        <div className="flex items-center gap-2">
          <Tabs value={activeFilter} onValueChange={setActiveFilter}>
            <TabsList>
              <TabsTrigger value="all">{t("common.all")}</TabsTrigger>
              <TabsTrigger value="unread">{t("inbox.unread")}</TabsTrigger>
              <TabsTrigger value="starred">{t("nav.starred")}</TabsTrigger>
            </TabsList>
          </Tabs>

          <Separator orientation="vertical" className="h-6" />

          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-8 w-8" title={t("inbox.sort")}>
                <ArrowUpDown className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setSortBy("date")}>
                {t("common.date")} {sortBy === "date" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setSortBy("from")}>
                {t("inbox.sender")} {sortBy === "from" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setSortBy("subject")}>
                {t("common.subject")} {sortBy === "subject" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => setSortDir((d) => (d === "asc" ? "desc" : "asc"))}>
                {sortDir === "asc" ? t("inbox.ascending") : t("inbox.descending")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>

          <div className="flex border rounded-md">
            <Button
              variant={viewMode === "list" ? "secondary" : "ghost"}
              size="icon"
              className="h-8 w-8 rounded-r-none"
              onClick={() => setViewMode("list")}
            >
              <List className="h-4 w-4" />
            </Button>
            <Button
              variant={viewMode === "compact" ? "secondary" : "ghost"}
              size="icon"
              className="h-8 w-8 rounded-l-none"
              onClick={() => setViewMode("compact")}
            >
              <LayoutGrid className="h-4 w-4" />
            </Button>
          </div>

          <Button
            variant={viewType === "conversations" ? "secondary" : "ghost"}
            size="icon"
            className="h-8 w-8"
            title={t("inbox.conversations")}
            onClick={() => setViewType((v) => (v === "list" ? "conversations" : "list"))}
          >
            <MessagesSquare className="h-4 w-4" />
          </Button>

          <Button
            variant={previewPane === "right" ? "secondary" : "ghost"}
            size="icon"
            className="h-8 w-8"
            title={t("inbox.previewPane")}
            onClick={() => setPreview(previewPane === "right" ? "none" : "right")}
          >
            <PanelRight className="h-4 w-4" />
          </Button>
        </div>
      </div>

      <div className={cn(previewPane === "right" && "flex items-start gap-4")}>
        <div className={cn("space-y-4", previewPane === "right" ? "w-2/5 min-w-0" : "flex-1")}>
      <div className={cn(
        "rounded-lg border bg-card",
        viewMode === "compact" && "divide-y"
      )}>
        {loading ? (
          <div className={cn(viewMode === "list" ? "divide-y" : "")}>
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i} className={cn("flex items-start gap-4", viewMode === "list" ? "p-4" : "p-2")}>
                <Skeleton className="h-4 w-4" />
                <Skeleton className="h-4 w-4" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-32" />
                  {viewMode === "list" && <Skeleton className="h-3 w-full" />}
                </div>
              </div>
            ))}
          </div>
        ) : inboxTotal === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-muted p-4">
              <Filter className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">{t("inbox.noEmails")}</h3>
            <p className="text-sm text-muted-foreground">
              {folder === "starred" || activeFilter === "starred"
                ? t("inbox.noStarredMessages")
                : activeFilter === "unread"
                ? t("inbox.noUnreadMessages")
                : t("inbox.inboxEmpty")}
            </p>
          </div>
        ) : viewType === "conversations" ? (
          <div className="divide-y">
            {threadGroups.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16 text-center">
                <div className="rounded-full bg-muted p-4">
                  <MessagesSquare className="h-8 w-8 text-muted-foreground" />
                </div>
                <h3 className="mt-4 text-lg font-semibold">{t("threads.noConversations")}</h3>
              </div>
            ) : (
              threadGroups.map((thread) => (
                <div key={thread.key}>
                  {/* Thread header — click to expand */}
                  <div
                    className="flex cursor-pointer items-center gap-3 p-4 hover:bg-accent/50 transition-all"
                    onClick={() => toggleThread(thread.key)}
                  >
                    <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0">
                      {expandedThreads.has(thread.key) ? (
                        <ChevronDown className="h-4 w-4" />
                      ) : (
                        <ChevronRightIcon className="h-4 w-4" />
                      )}
                    </Button>

                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className={cn("text-sm font-semibold truncate", thread.unread > 0 && "text-foreground")}>
                          {thread.subject || t("common.noSubject")}
                        </span>
                        {thread.unread > 0 && (
                          <span className="h-2 w-2 rounded-full bg-primary shrink-0" />
                        )}
                      </div>
                      <div className="flex items-center gap-2 text-xs text-muted-foreground truncate">
                        <span>{thread.participants.join(", ")}</span>
                        <span>·</span>
                        <span>{thread.messages.length} {thread.messages.length === 1 ? t("threads.messageCount", { count: String(thread.messages.length) }) : t("threads.messagesCount", { count: String(thread.messages.length) })}</span>
                      </div>
                    </div>

                    <span className="text-xs text-muted-foreground shrink-0">
                      {formatAbsolute(thread.lastDate)}
                    </span>
                  </div>

                  {/* Expanded: individual email rows */}
                  {expandedThreads.has(thread.key) && (
                    <div className="bg-accent/5">
                      {thread.messages.map((email) => (
                        <EmailRow key={email.id} email={email} />
                      ))}
                    </div>
                  )}
                </div>
              ))
            )}
          </div>
        ) : (
          <div className={cn(viewMode === "list" ? "divide-y" : "")}>
            {pageEmails.map((email) => (
              <EmailRow key={email.id} email={email} />
            ))}
          </div>
        )}
      </div>

      {inboxNavMode === "infinite" ? (
        <div className="flex flex-col items-center gap-2">
          <span className="text-sm text-muted-foreground">
            {t("inbox.loadedCount", { loaded: String(pageEmails.length), total: String(inboxTotal) })}
          </span>
          {/* Sentinel: the observer loads the next block when this scrolls in. */}
          <div ref={loadMoreRef} className="h-1 w-full" />
          {inboxLoadingMore && (
            <span className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              {t("inbox.loadingMore")}
            </span>
          )}
        </div>
      ) : (
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">
            {t(inboxTotal !== 1 ? "inbox.messagesCount" : "inbox.messageCount", { count: String(inboxTotal) })}
            {totalPages > 1 && ` · ${t("inbox.pageOf", { current: String(currentPage + 1), total: String(totalPages) })}`}
          </span>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="icon"
              disabled={currentPage <= 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <Button
              variant="outline"
              size="icon"
              disabled={currentPage >= totalPages - 1}
              onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
            >
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}
        </div>
        {previewPane === "right" && (
          <div className="min-w-0 flex-1 rounded-lg border bg-card overflow-auto max-h-[calc(100vh-9rem)]">
            {selectedId ? (
              <EmailDetailPage id={selectedId} embedded />
            ) : (
              <div className="p-12 text-center text-sm text-muted-foreground">{t("inbox.selectMessage")}</div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
