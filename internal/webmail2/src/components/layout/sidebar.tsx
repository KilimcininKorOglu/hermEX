import { useState, useEffect, useCallback } from "react"
import { NavLink, useLocation, useNavigate } from "react-router-dom"
import {
  Inbox,
  Send,
  FileText,
  Clock,
  Trash2,
  Star,
  AlertCircle,
  Settings,
  ChevronLeft,
  ChevronRight,
  PenSquare,
  FolderOpen,
  FolderPlus,
  Pencil,
  MoreHorizontal,
  CalendarDays,
  ListTodo,
  StickyNote,
  Users,
  Search,
  Mail,
  Filter,
  MessagesSquare,
  ChevronDown,
  ChevronUp,
  Bookmark,
  BookmarkPlus,
  Share2,
  Eraser,
  StarOff,
} from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Separator } from "@/components/ui/separator"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"
import api, { type SearchFolder } from "@/utils/api"
import { ShareFolderDialog } from "@/components/share-folder-dialog"

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
  mobileOpen?: boolean
  onMobileClose?: () => void
}

interface NavItem {
  icon: React.ElementType
  label: string
  path: string
  count?: number
  color?: string
  shortcut?: string
  badgeColor?: string
}

// label holds an i18n key (nav.*) resolved at render time via t().
const mainNavItems: NavItem[] = [
  { icon: CalendarDays, label: "nav.today", path: "/today" },
  { icon: Inbox, label: "nav.inbox", path: "/inbox", shortcut: "gi" },
  { icon: MessagesSquare, label: "nav.conversations", path: "/threads" },
  { icon: Search, label: "nav.search", path: "/search", shortcut: "/" },
  { icon: Star, label: "nav.starred", path: "/starred", shortcut: "gs" },
  { icon: Send, label: "nav.sent", path: "/sent", shortcut: "gt" },
  { icon: FileText, label: "nav.drafts", path: "/drafts", shortcut: "gd" },
  { icon: Clock, label: "nav.scheduled", path: "/scheduled" },
  { icon: Trash2, label: "nav.trash", path: "/trash", shortcut: "gT" },
  { icon: Users, label: "nav.shared", path: "/shared" },
  { icon: FolderOpen, label: "nav.publicFolders", path: "/public-folders" },
  { icon: Users, label: "nav.contacts", path: "/contacts" },
  { icon: CalendarDays, label: "nav.calendar", path: "/calendar" },
  { icon: ListTodo, label: "nav.tasks", path: "/tasks" },
  { icon: StickyNote, label: "nav.notes", path: "/notes" },
  { icon: Filter, label: "nav.filters", path: "/filters" },
  { icon: Users, label: "nav.groups", path: "/groups" },
]

// Standard mailboxes already shown in the main nav (or as Spam below); excluded
// from the dynamic custom-folder list.
const standardMailboxes = new Set(["inbox", "sent", "drafts", "trash", "junk", "scheduled"])

// EMPTY_SF_FORM is the blank saved-search criteria form (reset on open/create).
const EMPTY_SF_FORM = {
  name: "",
  from: "",
  subject: "",
  body: "",
  dateFrom: "",
  dateTo: "",
  hasAttachment: false,
  baseFolders: "",
}

const folderItems: NavItem[] = [
  { icon: AlertCircle, label: "nav.spam", path: "/spam", color: "text-red-500" },
]

// Shared mailbox item for display
interface SharedMailboxItem {
  owner: string
  mailbox: string
  rights?: string
}

// hasCount reports whether a nav item carries a non-zero count badge.
const hasCount = (item: NavItem): boolean => (item.count ?? 0) > 0

// NavItemExtras renders the shortcut hint and count badge of an expanded nav item.
const NavItemExtras = ({ item, label, isActive }: { item: NavItem; label: string; isActive: boolean }) => (
  <>
    <span className="flex-1">{label}</span>
    {item.shortcut && (
      <kbd className="hidden group-hover:inline-flex items-center gap-0.5 rounded border px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground bg-muted">
        <span>⌘</span>{item.shortcut}
      </kbd>
    )}
    {hasCount(item) && (
      <Badge
        variant={isActive ? "default" : "secondary"}
        className="h-5 min-w-[20px] px-1.5 text-xs"
      >
        {item.count}
      </Badge>
    )}
  </>
)

// CollapsedCount renders the corner count badge of a collapsed nav item.
const CollapsedCount = ({ item }: { item: NavItem }) =>
  hasCount(item) ? (
    <Badge
      variant="default"
      className="absolute -right-1 -top-1 h-4 w-4 p-0 flex items-center justify-center text-[10px]"
    >
      {item.count}
    </Badge>
  ) : null

// NavItemTooltip wraps a collapsed nav item with its label and shortcut.
const NavItemTooltip = ({ label, shortcut, children }: { label: string; shortcut?: string; children: React.ReactNode }) => (
  <Tooltip delayDuration={0}>
    <TooltipTrigger asChild>
      {children}
    </TooltipTrigger>
    <TooltipContent side="right" className="flex items-center gap-3">
      {label}
      {shortcut && (
        <kbd className="rounded border px-1.5 py-0.5 text-xs font-mono bg-muted">
          ⌘{shortcut}
        </kbd>
      )}
    </TooltipContent>
  </Tooltip>
)

const NavItemComponent = ({ item, isExpanded }: { item: NavItem; isExpanded: boolean }) => {
  const location = useLocation()
  const { t } = useI18n()
  const isActive = location.pathname === item.path
  // nav.* labels resolve to translations; custom folder names fall through t()
  // unchanged (t returns the key when no translation exists).
  const label = t(item.label)

  const content = (
    <NavLink
      to={item.path}
      className={cn(
        "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group relative",
        isActive
          ? "bg-primary/10 text-primary shadow-sm"
          : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      )}
    >
      <item.icon
        className={cn(
          "h-5 w-5 shrink-0 transition-colors",
          item.color || (isActive ? "text-primary" : "text-muted-foreground group-hover:text-foreground")
        )}
      />
      {isExpanded ? <NavItemExtras item={item} label={label} isActive={isActive} /> : <CollapsedCount item={item} />}
    </NavLink>
  )

  if (!isExpanded) {
    return <NavItemTooltip label={label} shortcut={item.shortcut}>{content}</NavItemTooltip>
  }

  return content
}

// Shared mailbox item component with visual distinction
const SharedMailboxItemComponent = ({ 
  item, 
  isExpanded, 
  isActive,
  onClick 
}: { 
  item: SharedMailboxItem
  isExpanded: boolean
  isActive: boolean
  onClick: () => void
}) => {
  const { t } = useI18n()
  const content = (
    <button
      onClick={onClick}
      className={cn(
        "w-full flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group relative",
        isActive
          ? "bg-purple-500/10 text-purple-600 dark:text-purple-400 shadow-sm"
          : "text-muted-foreground hover:bg-purple-500/5 hover:text-purple-600 dark:hover:text-purple-400"
      )}
    >
      <Mail
        className={cn(
          "h-5 w-5 shrink-0 transition-colors",
          isActive ? "text-purple-600 dark:text-purple-400" : "text-purple-400 group-hover:text-purple-500"
        )}
      />
      {isExpanded && (
        <>
          <span className="flex-1 text-left truncate">{item.mailbox}</span>
          <span className="text-xs text-muted-foreground truncate max-w-[80px]">
            {item.owner}
          </span>
        </>
      )}
    </button>
  )

  if (!isExpanded) {
    return (
      <Tooltip delayDuration={0}>
        <TooltipTrigger asChild>
          {content}
        </TooltipTrigger>
        <TooltipContent side="right" className="flex flex-col gap-1">
          <span className="font-medium">{item.mailbox}</span>
          <span className="text-xs text-muted-foreground">{t("sidebar.shared", { owner: item.owner })}</span>
        </TooltipContent>
      </Tooltip>
    )
  }

  return content
}

// errorText returns an error's own message, or fallback for a non-Error throw.
const errorText = (err: unknown, fallback: string): string =>
  err instanceof Error ? err.message : fallback

type SavedSearchForm = typeof EMPTY_SF_FORM

// savedSearchFormOf fills the criteria form from a stored saved search.
const savedSearchFormOf = (sf: SearchFolder): SavedSearchForm => ({
  name: sf.name,
  from: sf.from ?? "",
  subject: sf.subject ?? "",
  body: sf.body ?? "",
  dateFrom: sf.date_from ?? "",
  dateTo: sf.date_to ?? "",
  hasAttachment: sf.has_attachment === true,
  baseFolders: (sf.base_folders ?? []).join(", "),
})

// savedSearchInput builds the API payload from the criteria form; empty
// criteria are left out so the server does not match on them.
const savedSearchInput = (form: SavedSearchForm, name: string) => ({
  name,
  from: form.from.trim() || undefined,
  subject: form.subject.trim() || undefined,
  body: form.body.trim() || undefined,
  date_from: form.dateFrom || undefined,
  date_to: form.dateTo || undefined,
  has_attachment: form.hasAttachment ? true : undefined,
  base_folders: form.baseFolders
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean),
})

// SidebarLogo renders the brand mark and, when expanded, the collapse button.
const SidebarLogo = ({ isExpanded, onToggle }: { isExpanded: boolean; onToggle: () => void }) => (
  <div className="flex h-16 shrink-0 items-center justify-between border-b px-4">
    <div className={cn("flex items-center gap-3", !isExpanded && "justify-center w-full")}>
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
        <svg
          viewBox="0 0 24 24"
          className="h-5 w-5 text-primary-foreground"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="M3 8l7.89 5.26a2 2 0 002.22 0L21 8M5 19h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z" />
        </svg>
      </div>
      {isExpanded && (
        <span className="font-semibold text-lg tracking-tight">hermEX</span>
      )}
    </div>
    {isExpanded && (
      <Button
        variant="ghost"
        size="icon"
        className="h-8 w-8"
        onClick={onToggle}
      >
        <ChevronLeft className="h-4 w-4" />
      </Button>
    )}
  </div>
)

// ComposeButton opens the compose page.
const ComposeButton = ({ isExpanded }: { isExpanded: boolean }) => {
  const navigate = useNavigate()
  const { t } = useI18n()
  return (
    <div className="shrink-0 p-3">
      <Button
        className={cn(
          "w-full bg-gradient-to-r from-primary to-primary/90 hover:from-primary/90 hover:to-primary shadow-lg shadow-primary/25 transition-all",
          !isExpanded && "px-0 justify-center"
        )}
        size={isExpanded ? "default" : "icon"}
        onClick={() => navigate("/compose")}
      >
        <PenSquare className="h-4 w-4" />
        {isExpanded && <span className="ml-2">{t("nav.compose")}</span>}
      </Button>
    </div>
  )
}

// SharedMailboxesSection lists the mailboxes shared with the caller, as an
// expandable list in the expanded sidebar and as one icon in the collapsed one.
const SharedMailboxesSection = ({ isExpanded }: { isExpanded: boolean }) => {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { user } = useAuth()
  const { currentMailbox, switchMailbox, sharedMailboxes } = useMailbox()
  const [sharedExpanded, setSharedExpanded] = useState(true)

  if (sharedMailboxes.length === 0) return null

  const isInSharedContext = currentMailbox.type === 'shared'
  const toggle = () => setSharedExpanded(!sharedExpanded)

  // Handle switching to a shared mailbox: point the mail context at the owner,
  // then land on the inbox, which now renders the shared mailbox's messages.
  const handleSharedMailboxClick = (mb: SharedMailboxItem) => {
    switchMailbox(mb.mailbox, mb.owner)
    navigate('/inbox')
  }

  // Handle switching back to personal mailbox
  const handlePersonalMailboxClick = () => {
    if (user?.email) {
      navigate('/inbox')
      switchMailbox(user.email)
    }
  }

  if (!isExpanded) {
    const countKey = sharedMailboxes.length > 1 ? "sidebar.sharedCountPlural" : "sidebar.sharedCount"
    return (
      <div className="space-y-1 px-1">
        <Tooltip delayDuration={0}>
          <TooltipTrigger asChild>
            <button
              onClick={toggle}
              className="w-full flex items-center justify-center rounded-lg p-2 text-purple-500 hover:bg-purple-500/10 transition-colors"
            >
              <Mail className="h-5 w-5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">
            <span>{t(countKey, { count: String(sharedMailboxes.length) })}</span>
          </TooltipContent>
        </Tooltip>
      </div>
    )
  }

  return (
    <>
      <Separator className="my-3" />
      <button
        onClick={toggle}
        className="flex items-center justify-between w-full px-3 py-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider hover:text-foreground transition-colors"
      >
        <span className="flex items-center gap-2">
          <Mail className="h-4 w-4 text-purple-500" />
          {t("sidebar.sharedMailboxes")}
        </span>
        {sharedExpanded ? (
          <ChevronUp className="h-4 w-4" />
        ) : (
          <ChevronDown className="h-4 w-4" />
        )}
      </button>

      {sharedExpanded && (
        <div className="space-y-1">
          {/* Personal mailbox entry when in shared context */}
          {isInSharedContext && (
            <button
              onClick={handlePersonalMailboxClick}
              className="w-full flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group bg-primary/5 hover:bg-primary/10 text-primary"
            >
              <Users className="h-5 w-5 shrink-0 text-primary" />
              <span className="flex-1 text-left">{t("sidebar.myMailbox")}</span>
              <Badge variant="secondary" className="text-xs">{t("nav.personal")}</Badge>
            </button>
          )}

          {sharedMailboxes.map((mb) => (
            <SharedMailboxItemComponent
              key={`${mb.owner}:${mb.mailbox}`}
              item={mb}
              isExpanded={isExpanded}
              isActive={isInSharedContext && currentMailbox.owner === mb.owner}
              onClick={() => handleSharedMailboxClick(mb)}
            />
          ))}
        </div>
      )}
    </>
  )
}

// FavoritesSection lists the pinned favourite folders.
const FavoritesSection = ({ favorites }: { favorites: string[] }) => {
  const { t } = useI18n()
  if (favorites.length === 0) return null
  return (
    <div className="mb-2">
      <p className="px-3 pb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t("nav.favorites")}
      </p>
      {favorites.map((name) => {
        const path = `/folder/${encodeURIComponent(name)}`
        return (
          <NavLink
            key={"fav-" + path}
            to={path}
            className={({ isActive }) =>
              cn(
                "group mx-1 flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-all",
                isActive ? "bg-primary/10 text-primary" : "text-foreground hover:bg-accent"
              )
            }
          >
            <Star className="h-5 w-5 shrink-0 fill-amber-400 text-amber-400" />
            <span className="flex-1 truncate">{name}</span>
          </NavLink>
        )
      })}
    </div>
  )
}

// SectionHeader renders a sidebar section title with one icon action.
const SectionHeader = ({
  label,
  actionLabel,
  icon: Icon,
  onAction,
  className,
}: {
  label: string
  actionLabel: string
  icon: React.ElementType
  onAction: () => void
  className?: string
}) => (
  <div className={cn("flex items-center justify-between px-3 pb-2", className)}>
    <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
      {label}
    </p>
    <Tooltip delayDuration={0}>
      <TooltipTrigger asChild>
        <button
          onClick={onAction}
          className="text-muted-foreground hover:text-foreground"
          aria-label={actionLabel}
        >
          <Icon className="h-4 w-4" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="right">{actionLabel}</TooltipContent>
    </Tooltip>
  </div>
)

// FolderDropTarget renders a standard folder that accepts a dragged message.
const FolderDropTarget = ({ item, isExpanded }: { item: NavItem; isExpanded: boolean }) => (
  <div
    onDragOver={(e) => { e.preventDefault(); e.dataTransfer.dropEffect = "move" }}
    onDrop={(e) => {
      e.preventDefault()
      const id = e.dataTransfer.getData("text/x-hermex-mail")
      if (!id) return
      const slug = item.path.replace(/^\//, "")
      api.moveMail(id, slug).then(() => window.dispatchEvent(new Event("hermex:mail-changed"))).catch(() => undefined)
    }}
  >
    <NavItemComponent item={item} isExpanded={isExpanded} />
  </div>
)

// RowLink is the link part of a sidebar row that also carries an actions menu.
const RowLink = ({ to, isActive, icon: Icon, label }: { to: string; isActive: boolean; icon: React.ElementType; label: string }) => (
  <NavLink
    to={to}
    className={cn(
      "flex flex-1 items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium min-w-0",
      isActive ? "text-primary" : "text-muted-foreground group-hover:text-accent-foreground"
    )}
  >
    <Icon className="h-5 w-5 shrink-0" />
    <span className="flex-1 truncate">{label}</span>
  </NavLink>
)

// RowActions is the hover-revealed actions menu of a sidebar row.
const RowActions = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <button
        className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-foreground px-1"
        aria-label={label}
      >
        <MoreHorizontal className="h-4 w-4" />
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end">
      {children}
    </DropdownMenuContent>
  </DropdownMenu>
)

// ActionRow wraps a row link and its actions menu with the active highlight.
const ActionRow = ({ isActive, children }: { isActive: boolean; children: React.ReactNode }) => (
  <div
    className={cn(
      "group flex items-center gap-1 rounded-lg pr-1 transition-all",
      isActive ? "bg-primary/10" : "hover:bg-accent"
    )}
  >
    {children}
  </div>
)

interface CustomFolderActions {
  onToggleFavorite: (name: string) => void
  onRename: (name: string) => void
  onShare: (name: string) => void
  onEmpty: (name: string) => void
  onDelete: (name: string) => void
}

// CustomFolderItem renders one user folder with its favourite, rename, share,
// empty and delete actions.
const CustomFolderItem = ({
  name,
  isExpanded,
  isFavorite,
  actions,
}: {
  name: string
  isExpanded: boolean
  isFavorite: boolean
  actions: CustomFolderActions
}) => {
  const location = useLocation()
  const { t } = useI18n()
  const path = `/folder/${encodeURIComponent(name)}`
  if (!isExpanded) {
    return <NavItemComponent item={{ icon: FolderOpen, label: name, path }} isExpanded={isExpanded} />
  }
  const isActive = location.pathname === path
  const FavoriteIcon = isFavorite ? StarOff : Star
  return (
    <ActionRow isActive={isActive}>
      <RowLink to={path} isActive={isActive} icon={FolderOpen} label={name} />
      <RowActions label={t("sidebar.folderActions", { name })}>
        <DropdownMenuItem onClick={() => actions.onToggleFavorite(name)}>
          <FavoriteIcon className="mr-2 h-4 w-4" />
          {t(isFavorite ? "sidebar.removeFavorite" : "sidebar.addFavorite")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => actions.onRename(name)}>
          <Pencil className="mr-2 h-4 w-4" />
          {t("sidebar.rename")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => actions.onShare(name)}>
          <Share2 className="mr-2 h-4 w-4" />
          {t("share.dialogTitle")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => actions.onEmpty(name)}>
          <Eraser className="mr-2 h-4 w-4" />
          {t("sidebar.empty")}
        </DropdownMenuItem>
        <DropdownMenuItem
          className="text-destructive"
          onClick={() => actions.onDelete(name)}
        >
          <Trash2 className="mr-2 h-4 w-4" />
          {t("common.delete")}
        </DropdownMenuItem>
      </RowActions>
    </ActionRow>
  )
}

// SavedSearchItem renders one saved search with its edit and delete actions.
const SavedSearchItem = ({
  sf,
  onEdit,
  onDelete,
}: {
  sf: SearchFolder
  onEdit: (sf: SearchFolder) => void
  onDelete: (sf: SearchFolder) => void
}) => {
  const location = useLocation()
  const { t } = useI18n()
  const path = `/saved-search/${sf.id}`
  const isActive = location.pathname === path
  return (
    <ActionRow isActive={isActive}>
      <RowLink to={path} isActive={isActive} icon={Bookmark} label={sf.name} />
      <RowActions label={t("sidebar.savedSearchActions", { name: sf.name })}>
        <DropdownMenuItem onClick={() => onEdit(sf)}>
          <Pencil className="mr-2 h-4 w-4" />
          {t("common.edit")}
        </DropdownMenuItem>
        <DropdownMenuItem
          className="text-destructive"
          onClick={() => onDelete(sf)}
        >
          <Trash2 className="mr-2 h-4 w-4" />
          {t("common.delete")}
        </DropdownMenuItem>
      </RowActions>
    </ActionRow>
  )
}

// FolderDialog creates a folder or renames an existing one.
const FolderDialog = ({
  open,
  onOpenChange,
  mode,
  current,
  value,
  onValueChange,
  busy,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: "create" | "rename"
  current: string
  value: string
  onValueChange: (value: string) => void
  busy: boolean
  onSubmit: () => void
}) => {
  const { t } = useI18n()
  const creating = mode === "create"
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{creating ? t("sidebar.newFolderTitle") : t("sidebar.renameFolderTitle")}</DialogTitle>
          <DialogDescription>
            {creating
              ? t("sidebar.createFolderDescription")
              : t("sidebar.renameFolderDescription", { name: current })}
          </DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={value}
          onChange={(e) => onValueChange(e.target.value)}
          placeholder={t("sidebar.folderNamePlaceholder")}
          onKeyDown={(e) => {
            if (e.key === "Enter") onSubmit()
          }}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={onSubmit} disabled={busy}>
            {creating ? t("common.create") : t("sidebar.rename")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ConfirmDeleteDialog asks before a destructive delete.
const ConfirmDeleteDialog = ({
  open,
  title,
  description,
  busy,
  onCancel,
  onConfirm,
}: {
  open: boolean
  title: string
  description: string
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) => {
  const { t } = useI18n()
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={busy}>
            <Trash2 className="mr-2 h-4 w-4" />
            {t("common.delete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// SavedSearchDialog edits the structured criteria of a saved search.
const SavedSearchDialog = ({
  open,
  onOpenChange,
  mode,
  form,
  setForm,
  busy,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: "create" | "edit"
  form: SavedSearchForm
  setForm: (form: SavedSearchForm) => void
  busy: boolean
  onSubmit: () => void
}) => {
  const { t } = useI18n()
  const creating = mode === "create"
  const field = (key: keyof SavedSearchForm) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [key]: e.target.value })
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {creating ? t("sidebar.newSavedSearchTitle") : t("sidebar.editSavedSearchTitle")}
          </DialogTitle>
          <DialogDescription>{t("sidebar.savedSearchDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Input
            autoFocus
            value={form.name}
            onChange={field("name")}
            placeholder={t("sidebar.savedSearchNamePlaceholder")}
          />
          <Input
            value={form.from}
            onChange={field("from")}
            placeholder={t("sidebar.savedSearchFromPlaceholder")}
          />
          <Input
            value={form.subject}
            onChange={field("subject")}
            placeholder={t("sidebar.savedSearchSubjectPlaceholder")}
          />
          <Input
            value={form.body}
            onChange={field("body")}
            placeholder={t("sidebar.savedSearchBodyPlaceholder")}
          />
          <div className="flex gap-2">
            <Input
              type="date"
              value={form.dateFrom}
              onChange={field("dateFrom")}
              aria-label={t("sidebar.savedSearchDateFrom")}
            />
            <Input
              type="date"
              value={form.dateTo}
              onChange={field("dateTo")}
              aria-label={t("sidebar.savedSearchDateTo")}
            />
          </div>
          <Input
            value={form.baseFolders}
            onChange={field("baseFolders")}
            placeholder={t("sidebar.savedSearchFoldersPlaceholder")}
          />
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={form.hasAttachment}
              onCheckedChange={(v) => setForm({ ...form, hasAttachment: v === true })}
            />
            {t("sidebar.savedSearchHasAttachment")}
          </label>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={onSubmit} disabled={busy}>
            {creating ? t("common.create") : t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// SettingsLink is the settings entry at the bottom of the sidebar.
const SettingsLink = ({ isExpanded }: { isExpanded: boolean }) => {
  const { t } = useI18n()
  return (
    <div className="shrink-0 border-t p-2">
      <NavLink
        to="/settings"
        className={({ isActive }) =>
          cn(
            "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group",
            isActive
              ? "bg-primary/10 text-primary"
              : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          )
        }
      >
        <Settings className="h-5 w-5 shrink-0" />
        {isExpanded && <span>{t("nav.settings")}</span>}
      </NavLink>
    </div>
  )
}

// asideClass returns the sidebar's width and off-canvas classes.
const asideClass = (isExpanded: boolean, mobileOpen: boolean): string =>
  cn(
    "fixed left-0 top-0 z-40 flex h-screen flex-col border-r bg-card transition-all duration-300 ease-in-out",
    isExpanded ? "w-64" : "w-16",
    // Hidden off-canvas on small screens unless toggled open; always shown on lg+.
    mobileOpen ? "translate-x-0" : "-translate-x-full lg:translate-x-0"
  )

// FoldersHeader renders the favourites and the folders title in the expanded sidebar.
const FoldersHeader = ({
  isExpanded,
  favorites,
  onCreate,
}: {
  isExpanded: boolean
  favorites: string[]
  onCreate: () => void
}) => {
  const { t } = useI18n()
  if (!isExpanded) return null
  return (
    <>
      <FavoritesSection favorites={favorites} />
      <SectionHeader
        label={t("nav.folders")}
        actionLabel={t("nav.newFolder")}
        icon={FolderPlus}
        onAction={onCreate}
      />
    </>
  )
}

// SavedSearchesSection lists the saved searches in the expanded sidebar.
const SavedSearchesSection = ({
  isExpanded,
  savedSearches,
  onCreate,
  onEdit,
  onDelete,
}: {
  isExpanded: boolean
  savedSearches: SearchFolder[]
  onCreate: () => void
  onEdit: (sf: SearchFolder) => void
  onDelete: (sf: SearchFolder) => void
}) => {
  const { t } = useI18n()
  if (!isExpanded) return null
  return (
    <>
      <SectionHeader
        label={t("nav.savedSearches")}
        actionLabel={t("sidebar.newSavedSearchTitle")}
        icon={BookmarkPlus}
        onAction={onCreate}
        className="pt-3"
      />
      {savedSearches.map((sf) => (
        <SavedSearchItem key={sf.id} sf={sf} onEdit={onEdit} onDelete={onDelete} />
      ))}
    </>
  )
}

// FolderShareDialog shares the caller's own folder, when one is chosen.
const FolderShareDialog = ({
  folder,
  open,
  onOpenChange,
}: {
  folder: { name: string; label: string } | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) => {
  const { user } = useAuth()
  if (!folder) return null
  return (
    <ShareFolderDialog
      open={open}
      onOpenChange={onOpenChange}
      folderName={folder.name}
      folderLabel={folder.label}
      owner={user?.email ?? ""}
      isOwner={true}
    />
  )
}

// CollapseToggle expands the collapsed sidebar.
const CollapseToggle = ({ isExpanded, onToggle }: { isExpanded: boolean; onToggle: () => void }) => {
  if (isExpanded) return null
  return (
    <Button
      variant="ghost"
      size="icon"
      className="absolute -right-3 top-20 h-6 w-6 rounded-full border bg-background shadow-md hover:bg-accent"
      onClick={onToggle}
    >
      <ChevronRight className="h-3 w-3" />
    </Button>
  )
}

export function Sidebar({ collapsed, onToggle, mobileOpen = false, onMobileClose }: SidebarProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useI18n()
  const [hovered, setHovered] = useState(false)
  const { loadSharedMailboxes, inboxUnread } = useMailbox()

  // Spam total (inbox unread comes from the shared MailboxContext).
  const [spamCount, setSpamCount] = useState(0)
  // Real custom mailboxes (beyond the standard ones shown in the main nav).
  const [customFolders, setCustomFolders] = useState<string[]>([])
  // Pinned favourite folders, shown in a section at the top of the folder list.
  const [favorites, setFavorites] = useState<string[]>([])

  // Load shared mailboxes on mount
  useEffect(() => {
    loadSharedMailboxes()
  }, [loadSharedMailboxes])

  // Folder management dialog state.
  const [folderDialogOpen, setFolderDialogOpen] = useState(false)
  const [folderDialogMode, setFolderDialogMode] = useState<"create" | "rename">("create")
  const [folderDialogCurrent, setFolderDialogCurrent] = useState("")
  const [folderDialogValue, setFolderDialogValue] = useState("")
  const [folderBusy, setFolderBusy] = useState(false)
  const [folderDeleteTarget, setFolderDeleteTarget] = useState<string | null>(null)

  // Folder sharing dialog
  const [shareDialogOpen, setShareDialogOpen] = useState(false)
  const [shareDialogFolder, setShareDialogFolder] = useState<{ name: string; label: string } | null>(null)

  // Saved searches (persistent search folders) and their structured criteria dialog.
  const [savedSearches, setSavedSearches] = useState<SearchFolder[]>([])
  const [sfDialogOpen, setSfDialogOpen] = useState(false)
  const [sfDialogMode, setSfDialogMode] = useState<"create" | "edit">("create")
  const [sfEditId, setSfEditId] = useState<string | null>(null)
  const [sfBusy, setSfBusy] = useState(false)
  const [sfDeleteTarget, setSfDeleteTarget] = useState<SearchFolder | null>(null)
  const [sfForm, setSfForm] = useState({ ...EMPTY_SF_FORM })

  // loadCustomFolders refreshes the dynamic folder list (also re-run after a
  // create/rename/delete so the sidebar reflects the change immediately).
  const loadCustomFolders = useCallback(async () => {
    try {
      const result = await api.getMailboxes()
      const extra = (result.mailboxes ?? []).filter(
        (m) => !standardMailboxes.has(m.toLowerCase())
      )
      setCustomFolders(extra)
    } catch {
      setCustomFolders([])
    }
  }, [])

  // loadSavedSearches refreshes the persistent saved-search list (re-run after a
  // create/update/delete so the sidebar reflects the change immediately).
  const loadSavedSearches = useCallback(async () => {
    try {
      const res = await api.listSearchFolders()
      setSavedSearches(res.search_folders ?? [])
    } catch {
      setSavedSearches([])
    }
  }, [])

  // loadFavorites refreshes the pinned favourite folders (re-run after a toggle).
  const loadFavorites = useCallback(async () => {
    try {
      const res = await api.getFavorites()
      setFavorites(res.favorites ?? [])
    } catch {
      setFavorites([])
    }
  }, [])

  // Load the spam count and custom folders on mount (inbox unread is provided
  // by the shared MailboxContext).
  useEffect(() => {
    let cancelled = false
    const loadCounts = async () => {
      try {
        const spam = await api.getMail("spam")
        if (!cancelled) setSpamCount((spam.emails ?? []).length)
      } catch {
        if (!cancelled) setSpamCount(0)
      }
      await loadCustomFolders()
      await loadSavedSearches()
      await loadFavorites()
    }
    loadCounts()
    return () => {
      cancelled = true
    }
  }, [loadCustomFolders, loadSavedSearches, loadFavorites])

  const openCreateFolder = () => {
    setFolderDialogMode("create")
    setFolderDialogCurrent("")
    setFolderDialogValue("")
    setFolderDialogOpen(true)
  }

  const openRenameFolder = (name: string) => {
    setFolderDialogMode("rename")
    setFolderDialogCurrent(name)
    setFolderDialogValue(name)
    setFolderDialogOpen(true)
  }

  const submitFolderDialog = async () => {
    const value = folderDialogValue.trim()
    if (!value) {
      toast.error(t("sidebar.folderNameRequired"))
      return
    }
    setFolderBusy(true)
    try {
      if (folderDialogMode === "create") {
        await api.createFolder(value)
        toast.success(t("sidebar.folderCreated"))
      } else {
        await api.renameFolder(folderDialogCurrent, value)
        toast.success(t("sidebar.folderRenamed"))
      }
      setFolderDialogOpen(false)
      await loadCustomFolders()
    } catch (err) {
      toast.error(errorText(err, t("sidebar.folderSaveFailed")))
    } finally {
      setFolderBusy(false)
    }
  }

  const confirmDeleteFolder = async () => {
    if (!folderDeleteTarget || folderBusy) return
    setFolderBusy(true)
    try {
      await api.deleteFolder(folderDeleteTarget)
      toast.success(t("sidebar.folderDeleted"))
      if (location.pathname === `/folder/${encodeURIComponent(folderDeleteTarget)}`) {
        navigate("/inbox")
      }
      setFolderDeleteTarget(null)
      await loadCustomFolders()
    } catch (err) {
      toast.error(errorText(err, t("sidebar.folderDeleteFailed")))
    } finally {
      setFolderBusy(false)
    }
  }

  // handleEmptyFolder discards a custom folder's contents to Trash (server-side, in
  // one call), then refreshes the folder list so its unread badge updates.
  const handleEmptyFolder = async (name: string) => {
    if (folderBusy) return
    setFolderBusy(true)
    try {
      await api.emptyFolder(name)
      toast.success(t("sidebar.folderEmptied"))
      await loadCustomFolders()
    } catch (err) {
      toast.error(errorText(err, t("sidebar.folderEmptyFailed")))
    } finally {
      setFolderBusy(false)
    }
  }

  // handleToggleFavorite pins or unpins a folder; the server returns the new list.
  const handleToggleFavorite = async (name: string) => {
    try {
      const res = await api.toggleFavorite(name)
      setFavorites(res.favorites ?? [])
    } catch (err) {
      toast.error(errorText(err, t("sidebar.favoriteFailed")))
    }
  }

  const openCreateSavedSearch = () => {
    setSfDialogMode("create")
    setSfEditId(null)
    setSfForm({ ...EMPTY_SF_FORM })
    setSfDialogOpen(true)
  }

  const openEditSavedSearch = (sf: SearchFolder) => {
    setSfDialogMode("edit")
    setSfEditId(sf.id)
    setSfForm(savedSearchFormOf(sf))
    setSfDialogOpen(true)
  }

  const submitSavedSearch = async () => {
    const name = sfForm.name.trim()
    if (!name) {
      toast.error(t("sidebar.savedSearchNameRequired"))
      return
    }
    setSfBusy(true)
    try {
      const input = savedSearchInput(sfForm, name)
      if (sfDialogMode === "create") {
        await api.createSearchFolder(input)
        toast.success(t("sidebar.savedSearchCreated"))
      } else if (sfEditId) {
        await api.updateSearchFolder(sfEditId, input)
        toast.success(t("sidebar.savedSearchUpdated"))
      }
      setSfDialogOpen(false)
      await loadSavedSearches()
    } catch (err) {
      toast.error(errorText(err, t("sidebar.savedSearchSaveFailed")))
    } finally {
      setSfBusy(false)
    }
  }

  const confirmDeleteSavedSearch = async () => {
    if (!sfDeleteTarget || sfBusy) return
    setSfBusy(true)
    try {
      await api.deleteSearchFolder(sfDeleteTarget.id)
      toast.success(t("sidebar.savedSearchDeleted"))
      if (location.pathname === `/saved-search/${sfDeleteTarget.id}`) {
        navigate("/inbox")
      }
      setSfDeleteTarget(null)
      await loadSavedSearches()
    } catch (err) {
      toast.error(errorText(err, t("sidebar.savedSearchDeleteFailed")))
    } finally {
      setSfBusy(false)
    }
  }

  const folderActions: CustomFolderActions = {
    onToggleFavorite: (name) => void handleToggleFavorite(name),
    onRename: openRenameFolder,
    onShare: (name) => {
      setShareDialogFolder({ name, label: name })
      setShareDialogOpen(true)
    },
    onEmpty: (name) => void handleEmptyFolder(name),
    onDelete: setFolderDeleteTarget,
  }

  // Inject real counts into the nav items (badges only render when > 0).
  const mainNav = mainNavItems.map((item) =>
    item.path === "/inbox" ? { ...item, count: inboxUnread } : item
  )
  const folders: NavItem[] = folderItems.map((item) =>
    item.path === "/spam" ? { ...item, count: spamCount } : item
  )

  const isExpanded = !collapsed || hovered


  return (
    <TooltipProvider>
    <aside
      className={asideClass(isExpanded, mobileOpen)}
      onMouseEnter={() => collapsed && setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <SidebarLogo isExpanded={isExpanded} onToggle={onToggle} />
      <ComposeButton isExpanded={isExpanded} />

      {/* Main Navigation */}
      <nav className="flex-1 min-h-0 space-y-1 px-2 py-2 overflow-y-auto" onClick={() => onMobileClose?.()}>
        {mainNav.map((item) => (
          <NavItemComponent key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        <SharedMailboxesSection isExpanded={isExpanded} />

        <Separator className="my-3" />

        <FoldersHeader isExpanded={isExpanded} favorites={favorites} onCreate={openCreateFolder} />

        {folders.map((item) => (
          <FolderDropTarget key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        {customFolders.map((name) => (
          <CustomFolderItem
            key={name}
            name={name}
            isExpanded={isExpanded}
            isFavorite={favorites.includes(name)}
            actions={folderActions}
          />
        ))}

        <SavedSearchesSection
          isExpanded={isExpanded}
          savedSearches={savedSearches}
          onCreate={openCreateSavedSearch}
          onEdit={openEditSavedSearch}
          onDelete={setSfDeleteTarget}
        />
      </nav>

      <FolderDialog
        open={folderDialogOpen}
        onOpenChange={setFolderDialogOpen}
        mode={folderDialogMode}
        current={folderDialogCurrent}
        value={folderDialogValue}
        onValueChange={setFolderDialogValue}
        busy={folderBusy}
        onSubmit={() => void submitFolderDialog()}
      />

      <ConfirmDeleteDialog
        open={folderDeleteTarget !== null}
        title={t("sidebar.deleteFolderTitle")}
        description={t("sidebar.deleteFolderConfirm", { name: folderDeleteTarget ?? "" })}
        busy={folderBusy}
        onCancel={() => setFolderDeleteTarget(null)}
        onConfirm={() => void confirmDeleteFolder()}
      />

      {/* Folder sharing dialog */}
      <FolderShareDialog
        folder={shareDialogFolder}
        open={shareDialogOpen}
        onOpenChange={(open) => {
          setShareDialogOpen(open)
          if (!open) setShareDialogFolder(null)
        }}
      />

      <SavedSearchDialog
        open={sfDialogOpen}
        onOpenChange={setSfDialogOpen}
        mode={sfDialogMode}
        form={sfForm}
        setForm={setSfForm}
        busy={sfBusy}
        onSubmit={() => void submitSavedSearch()}
      />

      <ConfirmDeleteDialog
        open={sfDeleteTarget !== null}
        title={t("sidebar.deleteSavedSearchTitle")}
        description={t("sidebar.deleteSavedSearchConfirm", { name: sfDeleteTarget?.name ?? "" })}
        busy={sfBusy}
        onCancel={() => setSfDeleteTarget(null)}
        onConfirm={() => void confirmDeleteSavedSearch()}
      />

      <SettingsLink isExpanded={isExpanded} />

      <CollapseToggle isExpanded={isExpanded} onToggle={onToggle} />
    </aside>
    </TooltipProvider>
  )
}
