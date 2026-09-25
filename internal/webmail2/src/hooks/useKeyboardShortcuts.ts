import { useEffect, useCallback, useState } from "react"
import { useNavigate } from "react-router-dom"
import { getShortcutMode, type ShortcutMode } from "@/utils/shortcutMode"
import { shortcutAction } from "@/utils/globalShortcuts"

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
    // Ignore if typing in an input
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
    const action = shortcutAction(mode, { key: e.key, ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey })
    if (!action) return
    if (action.preventDefault) e.preventDefault()
    if ("navigate" in action) navigate(action.navigate)
    else document.dispatchEvent(new CustomEvent(action.event))
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
