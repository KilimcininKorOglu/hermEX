import { cn } from "@/lib/utils"
import { getShortcutMode } from "@/utils/shortcutMode"

export type ViewMode = "list" | "compact"

// formatSize renders a message size as a compact human-readable string
// (reference the mail-list Size column). Bytes below 1 KB show as "< 1 KB".
export function formatSize(bytes: number): string {
  if (!bytes || bytes < 1024) return "< 1 KB"
  const kb = bytes / 1024
  if (kb < 1024) return `${Math.round(kb)} KB`
  return `${(kb / 1024).toFixed(1)} MB`
}

// rowTone returns a message row's background classes. A follow-up flag tints the
// whole row, not just the small glyph in the flag column: that column can be
// switched off, and even with it on a flagged mail is easy to miss in a full
// list. The tint has to survive hover and selection, so the flagged row carries
// its own hover and selected shades rather than falling through to the accent
// ones, which would replace it on the first mouseover.
export function rowTone(opts: {
  read: boolean
  flagged: boolean
  viewMode: ViewMode
  selected: boolean
  previewed: boolean
}): string {
  const { read, flagged, viewMode, selected, previewed } = opts
  if (flagged) {
    return selected || previewed
      ? "bg-amber-200/70 hover:bg-amber-200 dark:bg-amber-900/50 dark:hover:bg-amber-900/70"
      : "bg-amber-100/70 hover:bg-amber-100 dark:bg-amber-950/40 dark:hover:bg-amber-950/60"
  }
  return cn(
    "hover:bg-accent/50",
    !read && viewMode === "list" && "bg-accent/5",
    selected && "bg-primary/5",
    previewed && "bg-primary/10"
  )
}

// listKeysActive reports whether a keydown may drive the message list: list
// shortcuts are on, the key is not typed into a field, and no modifier is held.
export function listKeysActive(e: KeyboardEvent): boolean {
  if (getShortcutMode() === "off") return false
  const el = e.target as HTMLElement | null
  if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) return false
  return !(e.metaKey || e.ctrlKey || e.altKey)
}

// nextListIndex returns the row a j/k (or arrow) key moves the selection to,
// or null for any other key. With no selection both keys pick the first row.
export function nextListIndex(key: string, idx: number, count: number): number | null {
  if (key === "j" || key === "ArrowDown") return idx < 0 ? 0 : Math.min(idx + 1, count - 1)
  if (key === "k" || key === "ArrowUp") return idx < 0 ? 0 : Math.max(idx - 1, 0)
  return null
}

// emptyListKey is the i18n key of the notice an empty message list shows.
export function emptyListKey(folder: string, activeFilter: string): string {
  if (folder === "starred" || activeFilter === "starred") return "inbox.noStarredMessages"
  if (activeFilter === "unread") return "inbox.noUnreadMessages"
  return "inbox.inboxEmpty"
}
