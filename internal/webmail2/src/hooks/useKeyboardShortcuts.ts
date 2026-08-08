import { useEffect, useCallback, useState } from "react"
import { useNavigate } from "react-router-dom"
import { getShortcutMode, basicEnabled, extendedEnabled, type ShortcutMode } from "@/utils/shortcutMode"

export function useKeyboardShortcuts() {
  const navigate = useNavigate()
  // mode gates every global shortcut: "off" binds nothing, "basic" binds the
  // essential set, "extended" adds folder navigation. It is read from the cookie
  // cache and refreshed when Settings dispatches "shortcut-mode-changed".
  const [mode, setMode] = useState<ShortcutMode>(() => getShortcutMode())
  useEffect(() => {
    const onChange = () => setMode(getShortcutMode())
    document.addEventListener("shortcut-mode-changed", onChange)
    return () => document.removeEventListener("shortcut-mode-changed", onChange)
  }, [])

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (mode === "off") return
    // Ignore if typing in an input
    if (
      e.target instanceof HTMLInputElement ||
      e.target instanceof HTMLTextAreaElement
    ) {
      return
    }

    const key = e.key.toLowerCase()
    const ctrl = e.ctrlKey || e.metaKey
    const shift = e.shiftKey
    const basic = basicEnabled(mode)
    const extended = extendedEnabled(mode)

    // Navigation shortcuts (g + letter)
    if (key === "g" && !ctrl) {
      // Wait for next key
      return
    }

    // Basic shortcuts: compose, inbox, search, help, close.
    if (basic && ctrl && key === "n") {
      e.preventDefault()
      navigate("/compose")
      return
    }

    if (basic && ctrl && shift && key === "i") {
      e.preventDefault()
      navigate("/inbox")
      return
    }

    if (basic && key === "/" && !ctrl) {
      e.preventDefault()
      navigate("/search")
      return
    }

    // Extended shortcuts: direct folder navigation (Ctrl+1..4).
    if (extended && ctrl && key === "1") {
      e.preventDefault()
      navigate("/inbox")
      return
    }

    if (extended && ctrl && key === "2") {
      e.preventDefault()
      navigate("/sent")
      return
    }

    if (extended && ctrl && key === "3") {
      e.preventDefault()
      navigate("/drafts")
      return
    }

    if (extended && ctrl && key === "4") {
      e.preventDefault()
      navigate("/trash")
      return
    }

    if (basic && ctrl && key === "k") {
      e.preventDefault()
      navigate("/search")
      return
    }

    if (basic && key === "?" && shift) {
      e.preventDefault()
      // Toggle shortcuts dialog
      document.dispatchEvent(new CustomEvent("toggle-shortcuts"))
      return
    }

    if (basic && key === "escape") {
      document.dispatchEvent(new CustomEvent("close-dialogs"))
      return
    }
  }, [navigate, mode])

  useEffect(() => {
    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [handleKeyDown])
}

// category/description hold i18n keys (shortcuts.*) resolved at render time in
// ShortcutsDialog via t(); keys are literal keyboard glyphs and stay as-is.
// level marks whether a shortcut fires in "basic" (and thus also "extended") or
// only in "extended", mirroring what the hooks actually bind, the dialog badges
// each row and dims the ones the current mode does not enable. Every entry here
// is bound somewhere (no phantom rows).
export const shortcuts = [
  { category: "shortcuts.cat.navigation", items: [
    { keys: ["⌘", "1"], description: "shortcuts.desc.goToInbox", level: "extended" },
    { keys: ["⌘", "2"], description: "shortcuts.desc.goToSent", level: "extended" },
    { keys: ["⌘", "3"], description: "shortcuts.desc.goToDrafts", level: "extended" },
    { keys: ["⌘", "4"], description: "shortcuts.desc.goToTrash", level: "extended" },
    { keys: ["⌘", "K"], description: "shortcuts.desc.search", level: "basic" },
    { keys: ["/"], description: "shortcuts.desc.searchNotInput", level: "basic" },
    { keys: ["?"], description: "shortcuts.desc.showShortcuts", level: "basic" },
    { keys: ["Esc"], description: "shortcuts.desc.closeDialog", level: "basic" },
  ]},
  { category: "shortcuts.cat.actions", items: [
    { keys: ["⌘", "N"], description: "shortcuts.desc.composeNew", level: "basic" },
    { keys: ["⌘", "Shift", "I"], description: "shortcuts.desc.goToInbox", level: "basic" },
    { keys: ["R"], description: "shortcuts.desc.replyEmail", level: "basic" },
    { keys: ["A"], description: "shortcuts.desc.replyAll", level: "basic" },
    { keys: ["F"], description: "shortcuts.desc.forwardEmail", level: "extended" },
    { keys: ["#"], description: "shortcuts.desc.deleteEmail", level: "extended" },
    { keys: ["U"], description: "shortcuts.desc.markUnread", level: "extended" },
  ]},
  { category: "shortcuts.cat.navigationInList", items: [
    { keys: ["J"], description: "shortcuts.desc.nextEmail", level: "basic" },
    { keys: ["K"], description: "shortcuts.desc.prevEmail", level: "basic" },
    { keys: ["Enter"], description: "shortcuts.desc.openEmail", level: "basic" },
  ]},
]
