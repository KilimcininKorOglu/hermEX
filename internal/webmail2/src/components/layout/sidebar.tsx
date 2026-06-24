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
      {isExpanded && (
        <>
          <span className="flex-1">{label}</span>
          {item.shortcut && (
            <kbd className="hidden group-hover:inline-flex items-center gap-0.5 rounded border px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground bg-muted">
              <span>⌘</span>{item.shortcut}
            </kbd>
          )}
          {item.count !== undefined && item.count > 0 && (
            <Badge
              variant={isActive ? "default" : "secondary"}
              className="h-5 min-w-[20px] px-1.5 text-xs"
            >
              {item.count}
            </Badge>
          )}
        </>
      )}
      {!isExpanded && item.count !== undefined && item.count > 0 && (
        <Badge
          variant="default"
          className="absolute -right-1 -top-1 h-4 w-4 p-0 flex items-center justify-center text-[10px]"
        >
          {item.count}
        </Badge>
      )}
    </NavLink>
  )

  if (!isExpanded) {
    return (
      <Tooltip delayDuration={0}>
        <TooltipTrigger asChild>
          {content}
        </TooltipTrigger>
        <TooltipContent side="right" className="flex items-center gap-3">
          {label}
          {item.shortcut && (
            <kbd className="rounded border px-1.5 py-0.5 text-xs font-mono bg-muted">
              ⌘{item.shortcut}
            </kbd>
          )}
        </TooltipContent>
      </Tooltip>
    )
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

export function Sidebar({ collapsed, onToggle, mobileOpen = false, onMobileClose }: SidebarProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useI18n()
  const [hovered, setHovered] = useState(false)
  const { user } = useAuth()
  const { currentMailbox, switchMailbox, loadSharedMailboxes, sharedMailboxes, inboxUnread } = useMailbox()

  // Track expanded state for shared mailboxes section
  const [sharedExpanded, setSharedExpanded] = useState(true)

  // Spam total (inbox unread comes from the shared MailboxContext).
  const [spamCount, setSpamCount] = useState(0)
  // Real custom mailboxes (beyond the standard ones shown in the main nav).
  const [customFolders, setCustomFolders] = useState<string[]>([])

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
    }
    loadCounts()
    return () => {
      cancelled = true
    }
  }, [loadCustomFolders, loadSavedSearches])

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
      toast.error(err instanceof Error ? err.message : t("sidebar.folderSaveFailed"))
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
      toast.error(err instanceof Error ? err.message : t("sidebar.folderDeleteFailed"))
    } finally {
      setFolderBusy(false)
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
    setSfForm({
      name: sf.name,
      from: sf.from ?? "",
      subject: sf.subject ?? "",
      body: sf.body ?? "",
      dateFrom: sf.date_from ?? "",
      dateTo: sf.date_to ?? "",
      hasAttachment: sf.has_attachment === true,
      baseFolders: (sf.base_folders ?? []).join(", "),
    })
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
      const input = {
        name,
        from: sfForm.from.trim() || undefined,
        subject: sfForm.subject.trim() || undefined,
        body: sfForm.body.trim() || undefined,
        date_from: sfForm.dateFrom || undefined,
        date_to: sfForm.dateTo || undefined,
        has_attachment: sfForm.hasAttachment ? true : undefined,
        base_folders: sfForm.baseFolders
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      }
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
      toast.error(err instanceof Error ? err.message : t("sidebar.savedSearchSaveFailed"))
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
      toast.error(err instanceof Error ? err.message : t("sidebar.savedSearchDeleteFailed"))
    } finally {
      setSfBusy(false)
    }
  }

  // Inject real counts into the nav items (badges only render when > 0).
  const mainNav = mainNavItems.map((item) =>
    item.path === "/inbox" ? { ...item, count: inboxUnread } : item
  )
  const folders: NavItem[] = folderItems.map((item) =>
    item.path === "/spam" ? { ...item, count: spamCount } : item
  )

  const isExpanded = !collapsed || hovered

  // Check if we're in a shared mailbox context
  const isInSharedContext = currentMailbox.type === 'shared'

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

  return (
    <TooltipProvider>
    <aside
      className={cn(
        "fixed left-0 top-0 z-40 h-screen border-r bg-card transition-all duration-300 ease-in-out",
        isExpanded ? "w-64" : "w-16",
        // Hidden off-canvas on small screens unless toggled open; always shown on lg+.
        mobileOpen ? "translate-x-0" : "-translate-x-full lg:translate-x-0"
      )}
      onMouseEnter={() => collapsed && setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {/* Logo Area */}
      <div className="flex h-16 items-center justify-between border-b px-4">
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

      {/* Compose Button */}
      <div className="p-3">
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

      {/* Main Navigation */}
      <nav className="flex-1 space-y-1 px-2 py-2 overflow-y-auto" onClick={() => onMobileClose?.()}>
        {mainNav.map((item) => (
          <NavItemComponent key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        {/* Shared Mailboxes Section - only show when user has shared mailboxes */}
        {sharedMailboxes.length > 0 && isExpanded && (
          <>
            <Separator className="my-3" />
            <button
              onClick={() => setSharedExpanded(!sharedExpanded)}
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
        )}

        {/* Shared mailboxes in collapsed mode */}
        {sharedMailboxes.length > 0 && !isExpanded && (
          <div className="space-y-1 px-1">
            <Tooltip delayDuration={0}>
              <TooltipTrigger asChild>
                <button
                  onClick={() => setSharedExpanded(!sharedExpanded)}
                  className="w-full flex items-center justify-center rounded-lg p-2 text-purple-500 hover:bg-purple-500/10 transition-colors"
                >
                  <Mail className="h-5 w-5" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">
                <span>
                  {sharedMailboxes.length > 1
                    ? t("sidebar.sharedCountPlural", { count: String(sharedMailboxes.length) })
                    : t("sidebar.sharedCount", { count: String(sharedMailboxes.length) })}
                </span>
              </TooltipContent>
            </Tooltip>
          </div>
        )}

        <Separator className="my-3" />

        {isExpanded && (
          <div className="flex items-center justify-between px-3 pb-2">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
              {t("nav.folders")}
            </p>
            <Tooltip delayDuration={0}>
              <TooltipTrigger asChild>
                <button
                  onClick={openCreateFolder}
                  className="text-muted-foreground hover:text-foreground"
                  aria-label={t("nav.newFolder")}
                >
                  <FolderPlus className="h-4 w-4" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">{t("nav.newFolder")}</TooltipContent>
            </Tooltip>
          </div>
        )}

        {folders.map((item) => (
          <NavItemComponent key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        {customFolders.map((name) => {
          const path = `/folder/${encodeURIComponent(name)}`
          const isActive = location.pathname === path
          if (!isExpanded) {
            return (
              <NavItemComponent
                key={path}
                item={{ icon: FolderOpen, label: name, path }}
                isExpanded={isExpanded}
              />
            )
          }
          return (
            <div
              key={path}
              className={cn(
                "group flex items-center gap-1 rounded-lg pr-1 transition-all",
                isActive ? "bg-primary/10" : "hover:bg-accent"
              )}
            >
              <NavLink
                to={path}
                className={cn(
                  "flex flex-1 items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium min-w-0",
                  isActive ? "text-primary" : "text-muted-foreground group-hover:text-accent-foreground"
                )}
              >
                <FolderOpen className="h-5 w-5 shrink-0" />
                <span className="flex-1 truncate">{name}</span>
              </NavLink>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-foreground px-1"
                    aria-label={t("sidebar.folderActions", { name })}
                  >
                    <MoreHorizontal className="h-4 w-4" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => openRenameFolder(name)}>
                    <Pencil className="mr-2 h-4 w-4" />
                    {t("sidebar.rename")}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    onClick={() => {
                      setShareDialogFolder({ name, label: name })
                      setShareDialogOpen(true)
                    }}
                  >
                    <Share2 className="mr-2 h-4 w-4" />
                    {t("share.dialogTitle")}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    className="text-destructive"
                    onClick={() => setFolderDeleteTarget(name)}
                  >
                    <Trash2 className="mr-2 h-4 w-4" />
                    {t("common.delete")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )
        })}

        {isExpanded && (
          <>
            <div className="flex items-center justify-between px-3 pb-2 pt-3">
              <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                {t("nav.savedSearches")}
              </p>
              <Tooltip delayDuration={0}>
                <TooltipTrigger asChild>
                  <button
                    onClick={openCreateSavedSearch}
                    className="text-muted-foreground hover:text-foreground"
                    aria-label={t("sidebar.newSavedSearchTitle")}
                  >
                    <BookmarkPlus className="h-4 w-4" />
                  </button>
                </TooltipTrigger>
                <TooltipContent side="right">{t("sidebar.newSavedSearchTitle")}</TooltipContent>
              </Tooltip>
            </div>

            {savedSearches.map((sf) => {
              const path = `/saved-search/${sf.id}`
              const isActive = location.pathname === path
              return (
                <div
                  key={sf.id}
                  className={cn(
                    "group flex items-center gap-1 rounded-lg pr-1 transition-all",
                    isActive ? "bg-primary/10" : "hover:bg-accent"
                  )}
                >
                  <NavLink
                    to={path}
                    className={cn(
                      "flex flex-1 items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium min-w-0",
                      isActive ? "text-primary" : "text-muted-foreground group-hover:text-accent-foreground"
                    )}
                  >
                    <Bookmark className="h-5 w-5 shrink-0" />
                    <span className="flex-1 truncate">{sf.name}</span>
                  </NavLink>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <button
                        className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-foreground px-1"
                        aria-label={t("sidebar.savedSearchActions", { name: sf.name })}
                      >
                        <MoreHorizontal className="h-4 w-4" />
                      </button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => openEditSavedSearch(sf)}>
                        <Pencil className="mr-2 h-4 w-4" />
                        {t("common.edit")}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        className="text-destructive"
                        onClick={() => setSfDeleteTarget(sf)}
                      >
                        <Trash2 className="mr-2 h-4 w-4" />
                        {t("common.delete")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              )
            })}
          </>
        )}
      </nav>

      {/* Create / rename folder dialog */}
      <Dialog open={folderDialogOpen} onOpenChange={setFolderDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{folderDialogMode === "create" ? t("sidebar.newFolderTitle") : t("sidebar.renameFolderTitle")}</DialogTitle>
            <DialogDescription>
              {folderDialogMode === "create"
                ? t("sidebar.createFolderDescription")
                : t("sidebar.renameFolderDescription", { name: folderDialogCurrent })}
            </DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={folderDialogValue}
            onChange={(e) => setFolderDialogValue(e.target.value)}
            placeholder={t("sidebar.folderNamePlaceholder")}
            onKeyDown={(e) => {
              if (e.key === "Enter") void submitFolderDialog()
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setFolderDialogOpen(false)} disabled={folderBusy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submitFolderDialog} disabled={folderBusy}>
              {folderDialogMode === "create" ? t("common.create") : t("sidebar.rename")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete folder confirmation */}
      <Dialog open={folderDeleteTarget !== null} onOpenChange={(open) => { if (!open) setFolderDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("sidebar.deleteFolderTitle")}</DialogTitle>
            <DialogDescription>
              {t("sidebar.deleteFolderConfirm", { name: folderDeleteTarget ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFolderDeleteTarget(null)} disabled={folderBusy}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={confirmDeleteFolder} disabled={folderBusy}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Folder sharing dialog */}
      {shareDialogFolder && (
        <ShareFolderDialog
          open={shareDialogOpen}
          onOpenChange={(open) => {
            setShareDialogOpen(open)
            if (!open) setShareDialogFolder(null)
          }}
          folderName={shareDialogFolder.name}
          folderLabel={shareDialogFolder.label}
          owner={user?.email ?? ""}
          isOwner={true}
        />
      )}

      {/* Create / edit saved search dialog (structured criteria) */}
      <Dialog open={sfDialogOpen} onOpenChange={setSfDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {sfDialogMode === "create" ? t("sidebar.newSavedSearchTitle") : t("sidebar.editSavedSearchTitle")}
            </DialogTitle>
            <DialogDescription>{t("sidebar.savedSearchDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Input
              autoFocus
              value={sfForm.name}
              onChange={(e) => setSfForm({ ...sfForm, name: e.target.value })}
              placeholder={t("sidebar.savedSearchNamePlaceholder")}
            />
            <Input
              value={sfForm.from}
              onChange={(e) => setSfForm({ ...sfForm, from: e.target.value })}
              placeholder={t("sidebar.savedSearchFromPlaceholder")}
            />
            <Input
              value={sfForm.subject}
              onChange={(e) => setSfForm({ ...sfForm, subject: e.target.value })}
              placeholder={t("sidebar.savedSearchSubjectPlaceholder")}
            />
            <Input
              value={sfForm.body}
              onChange={(e) => setSfForm({ ...sfForm, body: e.target.value })}
              placeholder={t("sidebar.savedSearchBodyPlaceholder")}
            />
            <div className="flex gap-2">
              <Input
                type="date"
                value={sfForm.dateFrom}
                onChange={(e) => setSfForm({ ...sfForm, dateFrom: e.target.value })}
                aria-label={t("sidebar.savedSearchDateFrom")}
              />
              <Input
                type="date"
                value={sfForm.dateTo}
                onChange={(e) => setSfForm({ ...sfForm, dateTo: e.target.value })}
                aria-label={t("sidebar.savedSearchDateTo")}
              />
            </div>
            <Input
              value={sfForm.baseFolders}
              onChange={(e) => setSfForm({ ...sfForm, baseFolders: e.target.value })}
              placeholder={t("sidebar.savedSearchFoldersPlaceholder")}
            />
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={sfForm.hasAttachment}
                onCheckedChange={(v) => setSfForm({ ...sfForm, hasAttachment: v === true })}
              />
              {t("sidebar.savedSearchHasAttachment")}
            </label>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSfDialogOpen(false)} disabled={sfBusy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submitSavedSearch} disabled={sfBusy}>
              {sfDialogMode === "create" ? t("common.create") : t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete saved search confirmation */}
      <Dialog open={sfDeleteTarget !== null} onOpenChange={(open) => { if (!open) setSfDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("sidebar.deleteSavedSearchTitle")}</DialogTitle>
            <DialogDescription>
              {t("sidebar.deleteSavedSearchConfirm", { name: sfDeleteTarget?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSfDeleteTarget(null)} disabled={sfBusy}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={confirmDeleteSavedSearch} disabled={sfBusy}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Bottom Actions */}
      <div className="border-t p-2">
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

      {/* Collapse Toggle (when collapsed) */}
      {!isExpanded && (
        <Button
          variant="ghost"
          size="icon"
          className="absolute -right-3 top-20 h-6 w-6 rounded-full border bg-background shadow-md hover:bg-accent"
          onClick={onToggle}
        >
          <ChevronRight className="h-3 w-3" />
        </Button>
      )}
    </aside>
    </TooltipProvider>
  )
}
