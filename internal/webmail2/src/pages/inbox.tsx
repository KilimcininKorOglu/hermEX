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
import { emptyListKey, formatSize, listKeysActive, nextListIndex, rowTone, type ViewMode } from "@/utils/inboxList"
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

interface ThreadGroup {
  key: string
  subject: string
  messages: Email[]
  participants: string[]
  lastDate: string
  unread: number
}

type ViewType = "list" | "conversations"
type SortOption = "date" | "from" | "subject"
type SortDir = "asc" | "desc"

interface InboxPageProps {
  folder?: string
}

interface EmailRowProps {
  email: Email
  viewMode: ViewMode
  columns: MailListColumns
  selected: boolean
  previewed: boolean
  t: (key: string, params?: Record<string, string>) => string
  onToggleSelect: () => void
  onOpen: () => void
  onToggleStar: (e: React.MouseEvent) => void
  onMarkRead: (e: React.MouseEvent) => void
  onArchive: () => void
  onDelete: () => void
}

type TFunc = (key: string, params?: Record<string, string>) => string

// RowSender shows the sender and, in the list view, the first category.
function RowSender({ email, viewMode, columns }: { email: Email; viewMode: ViewMode; columns: MailListColumns }) {
  const list = viewMode === "list"
  return (
    <div className="flex items-center gap-2">
      <span className={cn("text-sm", !email.read ? "font-semibold" : "font-normal")}>
        {list ? email.from : email.from.split(" ")[0]}
      </span>
      {columns.categories && email.labels.slice(0, list ? 1 : 0).map((label) => (
        <Badge key={label} variant="secondary" className="text-[10px] px-1.5 py-0">
          {label}
        </Badge>
      ))}
    </div>
  )
}

// RowSubject shows the subject and the preview snippet (list view only).
function RowSubject({ email, columns }: { email: Email; columns: MailListColumns }) {
  return (
    <div className="flex items-center gap-2 text-sm text-muted-foreground">
      <span className={cn(!email.read && "text-foreground font-medium")}>
        {email.subject}
      </span>
      {/* A message with no snippet, and one indexed before the column
          existed, would otherwise render a dangling separator. */}
      {columns.preview && email.preview && <span className="truncate">- {email.preview}</span>}
    </div>
  )
}

// ImportanceMark shows a high or low importance glyph.
function ImportanceMark({ importance, t }: { importance?: string; t: TFunc }) {
  if (importance === "high") {
    return <span className="text-red-500 font-bold text-xs" title={t("compose.importanceHigh")}>!</span>
  }
  if (importance === "low") {
    return <span className="text-muted-foreground text-xs" title={t("compose.importanceLow")}>↓</span>
  }
  return null
}

// RowIndicators shows the attachment, importance, flag and size columns.
function RowIndicators({ email, columns, t }: { email: Email; columns: MailListColumns; t: TFunc }) {
  return (
    <>
      {columns.attachment && email.hasAttachments && (
        <Paperclip className="h-4 w-4 text-muted-foreground" />
      )}
      {columns.importance && <ImportanceMark importance={email.importance} t={t} />}
      {columns.flag && email.followupStatus === 2 && (
        <span className="text-red-500 text-xs" title={t("inbox.columns.flag")}>⚑</span>
      )}
      {columns.size && (
        <span className="text-xs text-muted-foreground whitespace-nowrap tabular-nums">
          {formatSize(email.size)}
        </span>
      )}
    </>
  )
}

// RowDate shows the unread dot (list view) and the received date.
function RowDate({ email, viewMode }: { email: Email; viewMode: ViewMode }) {
  return (
    <>
      {!email.read && viewMode === "list" && (
        <span className="h-2 w-2 rounded-full bg-primary" />
      )}
      <span className={cn(
        "text-xs text-muted-foreground whitespace-nowrap",
        viewMode === "compact" && "w-12 text-right"
      )}>
        {formatAbsolute(email.date)}
      </span>
    </>
  )
}

// stopThen runs action without letting the click reach the row.
const stopThen = (action: () => void) => (e: React.MouseEvent) => {
  e.stopPropagation()
  action()
}

// RowMenu is a message row's actions menu.
function RowMenu({
  starred,
  t,
  onMarkRead,
  onToggleStar,
  onArchive,
  onDelete,
}: {
  starred: boolean
  t: TFunc
  onMarkRead: (e: React.MouseEvent) => void
  onToggleStar: (e: React.MouseEvent) => void
  onArchive: () => void
  onDelete: () => void
}) {
  return (
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
        <DropdownMenuItem onClick={onMarkRead}>
          <MailOpen className="mr-2 h-4 w-4" />
          {t("common.markRead")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={onToggleStar}>
          <Star className={cn("mr-2 h-4 w-4", starred && "fill-current")} />
          {starred ? t("inbox.removeStar") : t("inbox.addStar")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={stopThen(onArchive)}>
          <Archive className="mr-2 h-4 w-4" />
          {t("common.archive")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem className="text-destructive" onClick={stopThen(onDelete)}>
          <Trash2 className="mr-2 h-4 w-4" />
          {t("common.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// EmailRow lives at module scope on purpose. A component declared inside
// InboxPage is a NEW component type on every render, so React unmounts every row
// and mounts a fresh one for any state change, which loses the scroll position
// and any open row menu. It therefore takes what it needs as props rather than
// reading the page's own state.
function EmailRow({
  email,
  viewMode,
  columns,
  selected,
  previewed,
  t,
  onToggleSelect,
  onOpen,
  onToggleStar,
  onMarkRead,
  onArchive,
  onDelete,
}: EmailRowProps) {
  // A completed follow-up flag is not tinted, because the point of the tint is
  // what still needs doing.
  const flagged = email.followupStatus === 2
  const compact = viewMode === "compact"
  return (
    <div
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData("text/x-hermex-mail", email.id)
        e.dataTransfer.effectAllowed = "move"
      }}
      className={cn(
        "group flex cursor-pointer items-center gap-3 transition-all duration-200",
        compact ? "p-2" : "p-4",
        // Marker the unread-border CSS rule keys off (gated by the DB-backed
        // display toggle reflected on <html>); harmless when the toggle is off.
        !email.read && "hermex-unread",
        rowTone({ read: email.read, flagged, viewMode, selected, previewed })
      )}
      onClick={onOpen}
    >
      <Checkbox
        checked={selected}
        onCheckedChange={onToggleSelect}
        onClick={(e) => e.stopPropagation()}
      />

      <Button
        variant="ghost"
        size="icon"
        className={cn(
          "h-8 w-8 shrink-0 transition-colors",
          email.starred ? "text-amber-500" : "text-muted-foreground hover:text-foreground"
        )}
        onClick={onToggleStar}
      >
        <Star className={cn("h-4 w-4", email.starred && "fill-current")} />
      </Button>

      <div className={cn("flex-1 min-w-0", compact && "flex items-center gap-4")}>
        <RowSender email={email} viewMode={viewMode} columns={columns} />
        {!compact && <RowSubject email={email} columns={columns} />}
      </div>

      <div className={cn("flex items-center gap-2 shrink-0", compact && "flex-row-reverse")}>
        <RowIndicators email={email} columns={columns} t={t} />
        <RowDate email={email} viewMode={viewMode} />
        <RowMenu
          starred={email.starred}
          t={t}
          onMarkRead={onMarkRead}
          onToggleStar={onToggleStar}
          onArchive={onArchive}
          onDelete={onDelete}
        />
      </div>
    </div>
  )
}

// useListKeys binds j/k list navigation: j → next email, k → previous, Enter →
// open. Ignored while typing in an input/textarea so ordinary text entry never
// hijacks the keys.
function useListKeys(emails: Email[], selectedId: string | null, select: (id: string) => void) {
  const navigate = useNavigate()
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!listKeysActive(e) || emails.length === 0) return
      const idx = selectedId ? emails.findIndex((em) => em.id === selectedId) : -1
      const next = nextListIndex(e.key, idx, emails.length)
      if (next !== null) {
        e.preventDefault()
        select(emails[next].id)
        return
      }
      if (e.key === "Enter" && selectedId) {
        e.preventDefault()
        navigate(`/email/${selectedId}`)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [emails, selectedId, navigate, select])
}

// useThreadGroups fetches the grouped inbox while the conversations view is
// open. Conversations are grouped server-side; the list view is server-paged so
// the current page alone is not enough.
function useThreadGroups(viewType: ViewType): ThreadGroup[] {
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
  return threadGroups
}

// readDataUrl reads a file as a data: URL.
function readDataUrl(file: File): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as string)
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

// FolderActions are the mark-all-read, import and refresh buttons shown while
// nothing is selected.
function FolderActions({
  loading,
  t,
  onMarkAllRead,
  onImport,
  onRefresh,
}: {
  loading: boolean
  t: TFunc
  onMarkAllRead: () => void
  onImport: (file: File) => void
  onRefresh: () => void
}) {
  const importInputRef = useRef<HTMLInputElement>(null)
  return (
    <div className="flex items-center gap-1">
      <input
        ref={importInputRef}
        type="file"
        accept=".eml,message/rfc822"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0]
          e.target.value = ""
          if (file) onImport(file)
        }}
      />
      <Button variant="ghost" size="icon" className="h-8 w-8" onClick={onMarkAllRead} title={t("inbox.markAllRead")}>
        <CheckCheck className="h-4 w-4" />
      </Button>
      <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => importInputRef.current?.click()} title={t("inbox.import")}>
        <Upload className="h-4 w-4" />
      </Button>
      <Button variant="ghost" size="icon" className="h-8 w-8" onClick={onRefresh}>
        <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
      </Button>
    </div>
  )
}

// SortMenu picks the sort column and direction.
function SortMenu({
  sortBy,
  sortDir,
  t,
  onSortBy,
  onToggleDir,
}: {
  sortBy: SortOption
  sortDir: SortDir
  t: TFunc
  onSortBy: (s: SortOption) => void
  onToggleDir: () => void
}) {
  const options: [SortOption, string][] = [
    ["date", t("common.date")],
    ["from", t("inbox.sender")],
    ["subject", t("common.subject")],
  ]
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("inbox.sort")}>
          <ArrowUpDown className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {options.map(([value, label]) => (
          <DropdownMenuItem key={value} onClick={() => onSortBy(value)}>
            {label} {sortBy === value && "✓"}
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onToggleDir}>
          {sortDir === "asc" ? t("inbox.ascending") : t("inbox.descending")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// DensityToggle switches between the comfortable and the compact row density.
function DensityToggle({ viewMode, onChange }: { viewMode: ViewMode; onChange: (m: ViewMode) => void }) {
  return (
    <div className="flex border rounded-md">
      <Button
        variant={viewMode === "list" ? "secondary" : "ghost"}
        size="icon"
        className="h-8 w-8 rounded-r-none"
        onClick={() => onChange("list")}
      >
        <List className="h-4 w-4" />
      </Button>
      <Button
        variant={viewMode === "compact" ? "secondary" : "ghost"}
        size="icon"
        className="h-8 w-8 rounded-l-none"
        onClick={() => onChange("compact")}
      >
        <LayoutGrid className="h-4 w-4" />
      </Button>
    </div>
  )
}

// ToggleButton is an icon button shown pressed while on.
function ToggleButton({ on, title, icon: Icon, onClick }: { on: boolean; title: string; icon: React.ElementType; onClick: () => void }) {
  return (
    <Button
      variant={on ? "secondary" : "ghost"}
      size="icon"
      className="h-8 w-8"
      title={title}
      onClick={onClick}
    >
      <Icon className="h-4 w-4" />
    </Button>
  )
}

// ListSkeleton is the message list while it loads.
function ListSkeleton({ viewMode }: { viewMode: ViewMode }) {
  const list = viewMode === "list"
  return (
    <div className={cn(list ? "divide-y" : "")}>
      {[1, 2, 3, 4, 5].map((i) => (
        <div key={i} className={cn("flex items-start gap-4", list ? "p-4" : "p-2")}>
          <Skeleton className="h-4 w-4" />
          <Skeleton className="h-4 w-4" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-32" />
            {list && <Skeleton className="h-3 w-full" />}
          </div>
        </div>
      ))}
    </div>
  )
}

// EmptyNotice is a centred icon with a title and an optional line under it.
function EmptyNotice({ icon: Icon, title, text }: { icon: React.ElementType; title: string; text?: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="rounded-full bg-muted p-4">
        <Icon className="h-8 w-8 text-muted-foreground" />
      </div>
      <h3 className="mt-4 text-lg font-semibold">{title}</h3>
      {text && <p className="text-sm text-muted-foreground">{text}</p>}
    </div>
  )
}

// ThreadItem is one conversation: a header that expands to its message rows.
function ThreadItem({
  thread,
  expanded,
  t,
  onToggle,
  renderRow,
}: {
  thread: ThreadGroup
  expanded: boolean
  t: TFunc
  onToggle: () => void
  renderRow: (email: Email) => React.ReactNode
}) {
  const count = thread.messages.length
  const unread = thread.unread > 0
  return (
    <div>
      {/* Thread header, click to expand */}
      <div
        className="flex cursor-pointer items-center gap-3 p-4 hover:bg-accent/50 transition-all"
        onClick={onToggle}
      >
        <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0">
          {expanded ? (
            <ChevronDown className="h-4 w-4" />
          ) : (
            <ChevronRightIcon className="h-4 w-4" />
          )}
        </Button>

        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className={cn("text-sm font-semibold truncate", unread && "text-foreground")}>
              {thread.subject || t("common.noSubject")}
            </span>
            {unread && (
              <span className="h-2 w-2 rounded-full bg-primary shrink-0" />
            )}
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground truncate">
            <span>{thread.participants.join(", ")}</span>
            <span>·</span>
            <span>{t(count === 1 ? "threads.messageCount" : "threads.messagesCount", { count: String(count) })}</span>
          </div>
        </div>

        <span className="text-xs text-muted-foreground shrink-0">
          {formatAbsolute(thread.lastDate)}
        </span>
      </div>

      {/* Expanded: individual email rows */}
      {expanded && (
        <div className="bg-accent/5">
          {thread.messages.map((email) => renderRow(email))}
        </div>
      )}
    </div>
  )
}

// ThreadList is the conversations view.
function ThreadList({
  threads,
  t,
  renderRow,
}: {
  threads: ThreadGroup[]
  t: TFunc
  renderRow: (email: Email) => React.ReactNode
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }
  if (threads.length === 0) {
    return (
      <div className="divide-y">
        <EmptyNotice icon={MessagesSquare} title={t("threads.noConversations")} />
      </div>
    )
  }
  return (
    <div className="divide-y">
      {threads.map((thread) => (
        <ThreadItem
          key={thread.key}
          thread={thread}
          expanded={expanded.has(thread.key)}
          t={t}
          onToggle={() => toggle(thread.key)}
          renderRow={renderRow}
        />
      ))}
    </div>
  )
}

// InfiniteFooter shows the loaded count and the sentinel the observer watches.
function InfiniteFooter({
  loaded,
  total,
  loadingMore,
  sentinelRef,
  t,
}: {
  loaded: number
  total: number
  loadingMore: boolean
  sentinelRef: React.RefObject<HTMLDivElement | null>
  t: TFunc
}) {
  return (
    <div className="flex flex-col items-center gap-2">
      <span className="text-sm text-muted-foreground">
        {t("inbox.loadedCount", { loaded: String(loaded), total: String(total) })}
      </span>
      {/* Sentinel: the observer loads the next block when this scrolls in. */}
      <div ref={sentinelRef} className="h-1 w-full" />
      {loadingMore && (
        <span className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          {t("inbox.loadingMore")}
        </span>
      )}
    </div>
  )
}

// PagerFooter shows the message count and the previous/next page buttons.
function PagerFooter({
  total,
  currentPage,
  totalPages,
  t,
  onPage,
}: {
  total: number
  currentPage: number
  totalPages: number
  t: TFunc
  onPage: (update: (p: number) => number) => void
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-sm text-muted-foreground">
        {t(total !== 1 ? "inbox.messagesCount" : "inbox.messageCount", { count: String(total) })}
        {totalPages > 1 && ` · ${t("inbox.pageOf", { current: String(currentPage + 1), total: String(totalPages) })}`}
      </span>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="icon"
          disabled={currentPage <= 0}
          onClick={() => onPage((p) => Math.max(0, p - 1))}
        >
          <ChevronLeft className="h-4 w-4" />
        </Button>
        <Button
          variant="outline"
          size="icon"
          disabled={currentPage >= totalPages - 1}
          onClick={() => onPage((p) => Math.min(totalPages - 1, p + 1))}
        >
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

// MessageListBody shows the list while it loads, the empty notice, the
// conversations view, or the current page of message rows.
function MessageListBody({
  loading,
  viewMode,
  total,
  emptyKey,
  viewType,
  threads,
  emails,
  t,
  renderRow,
}: {
  loading: boolean
  viewMode: ViewMode
  total: number
  emptyKey: string
  viewType: ViewType
  threads: ThreadGroup[]
  emails: Email[]
  t: TFunc
  renderRow: (email: Email) => React.ReactNode
}) {
  if (loading) return <ListSkeleton viewMode={viewMode} />
  if (total === 0) return <EmptyNotice icon={Filter} title={t("inbox.noEmails")} text={t(emptyKey)} />
  if (viewType === "conversations") return <ThreadList threads={threads} t={t} renderRow={renderRow} />
  return (
    <div className={cn(viewMode === "list" ? "divide-y" : "")}>
      {emails.map((email) => renderRow(email))}
    </div>
  )
}

// InboxWelcome shows the welcome banner on the inbox until it is dismissed. Its
// closed state lives in a client-readable cookie (the web UI uses cookies, not
// localStorage), so it stays dismissed across visits.
function InboxWelcome({ folder }: { folder: string }) {
  const [show, setShow] = useState(() => getCookie("hermex-welcome-dismissed") !== "1")
  if (!show || folder !== "inbox") return null
  return <WelcomeBanner onDismiss={() => { setCookie("hermex-welcome-dismissed", "1"); setShow(false) }} />
}

// UnreadBadge shows the folder's unread count on the unfiltered view.
function UnreadBadge({ count, activeFilter, t }: { count: number; activeFilter: string; t: TFunc }) {
  if (count <= 0 || activeFilter !== "all") return null
  return (
    <Badge variant="secondary" className="ml-2">
      {t("inbox.unreadCount", { count: String(count) })}
    </Badge>
  )
}

// PreviewPane reads the selected message beside the list.
function PreviewPane({ selectedId, t }: { selectedId: string | null; t: TFunc }) {
  return (
    <div className="min-w-0 flex-1 rounded-lg border bg-card overflow-auto max-h-[calc(100vh-9rem)]">
      {selectedId ? (
        <EmailDetailPage id={selectedId} embedded />
      ) : (
        <div className="p-12 text-center text-sm text-muted-foreground">{t("inbox.selectMessage")}</div>
      )}
    </div>
  )
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

  // Derive the displayed list from the shared inbox state. The starred view is
  // the same inbox dataset filtered to flagged messages.
  // inboxEmails is already the server page for the current folder/filter/sort.
  const emails: Email[] = useMemo(() => inboxEmails.map(toEmail), [inboxEmails])

  useListKeys(emails, selectedId, setSelectedId)

  const threadGroups = useThreadGroups(viewType)

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

  const onImportFile = async (file: File) => {
    try {
      const dataUrl = await readDataUrl(file)
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

  // emailRow binds the page's state and handlers to one row. The row component
  // itself is at module scope, so binding happens here rather than through a
  // closure inside it.
  const emailRow = (email: Email) => (
    <EmailRow
      key={email.id}
      email={email}
      viewMode={viewMode}
      columns={columns}
      selected={sel.isSelected(email.id)}
      previewed={previewPane === "right" && selectedId === email.id}
      t={t}
      onToggleSelect={() => sel.toggle(email.id)}
      onOpen={() => {
        if (previewPane === "right") setSelectedId(email.id)
        else navigate(`/email/${email.id}`)
      }}
      onToggleStar={(e) => toggleStar(email.id, e)}
      onMarkRead={(e) => markAsRead(email.id, e)}
      onArchive={() => archiveEmails([email.id])}
      onDelete={() => deleteEmails([email.id])}
    />
  )

  return (
    <div className="space-y-4">
      <InboxWelcome folder={folder} />
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Checkbox
            checked={sel.allSelected(allIds)}
            onCheckedChange={() => sel.toggleAll(allIds)}
          />

          {sel.count > 0 ? (
            <BulkActionBar ids={sel.ids} actions={bulkActions} onClear={sel.clear} />
          ) : (
            <FolderActions
              loading={loading}
              t={t}
              onMarkAllRead={handleMarkAllRead}
              onImport={(file) => void onImportFile(file)}
              onRefresh={handleRefresh}
            />
          )}

          <UnreadBadge count={unreadCount} activeFilter={activeFilter} t={t} />
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

          <SortMenu
            sortBy={sortBy}
            sortDir={sortDir}
            t={t}
            onSortBy={setSortBy}
            onToggleDir={() => setSortDir((d) => (d === "asc" ? "desc" : "asc"))}
          />

          <DensityToggle viewMode={viewMode} onChange={setViewMode} />

          <ToggleButton
            on={viewType === "conversations"}
            title={t("inbox.conversations")}
            icon={MessagesSquare}
            onClick={() => setViewType((v) => (v === "list" ? "conversations" : "list"))}
          />

          <ToggleButton
            on={previewPane === "right"}
            title={t("inbox.previewPane")}
            icon={PanelRight}
            onClick={() => setPreview(previewPane === "right" ? "none" : "right")}
          />
        </div>
      </div>

      <div className={cn(previewPane === "right" && "flex items-start gap-4")}>
        <div className={cn("space-y-4", previewPane === "right" ? "w-2/5 min-w-0" : "flex-1")}>
      <div className={cn(
        "rounded-lg border bg-card",
        viewMode === "compact" && "divide-y"
      )}>
        <MessageListBody
          loading={loading}
          viewMode={viewMode}
          total={inboxTotal}
          emptyKey={emptyListKey(folder, activeFilter)}
          viewType={viewType}
          threads={threadGroups}
          emails={pageEmails}
          t={t}
          renderRow={emailRow}
        />
      </div>

      {inboxNavMode === "infinite" ? (
        <InfiniteFooter
          loaded={pageEmails.length}
          total={inboxTotal}
          loadingMore={inboxLoadingMore}
          sentinelRef={loadMoreRef}
          t={t}
        />
      ) : (
        <PagerFooter total={inboxTotal} currentPage={currentPage} totalPages={totalPages} t={t} onPage={setPage} />
      )}
        </div>
        {previewPane === "right" && <PreviewPane selectedId={selectedId} t={t} />}
      </div>
    </div>
  )
}
