import { useEffect, useCallback } from "react"
import { useNavigate } from "react-router-dom"

export function useKeyboardShortcuts() {
  const navigate = useNavigate()

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
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

    // Navigation shortcuts (g + letter)
    if (key === "g" && !ctrl) {
      // Wait for next key
      return
    }

    // Global shortcuts
    if (ctrl && key === "n") {
      e.preventDefault()
      navigate("/compose")
      return
    }

    if (ctrl && shift && key === "i") {
      e.preventDefault()
      navigate("/inbox")
      return
    }

    if (key === "/" && !ctrl) {
      e.preventDefault()
      navigate("/search")
      return
    }

    if (ctrl && key === "1") {
      e.preventDefault()
      navigate("/inbox")
      return
    }

    if (ctrl && key === "2") {
      e.preventDefault()
      navigate("/sent")
      return
    }

    if (ctrl && key === "3") {
      e.preventDefault()
      navigate("/drafts")
      return
    }

    if (ctrl && key === "4") {
      e.preventDefault()
      navigate("/trash")
      return
    }

    if (ctrl && key === "k") {
      e.preventDefault()
      navigate("/search")
      return
    }

    if (key === "?" && shift) {
      e.preventDefault()
      // Toggle shortcuts dialog
      document.dispatchEvent(new CustomEvent("toggle-shortcuts"))
      return
    }

    if (key === "escape") {
      document.dispatchEvent(new CustomEvent("close-dialogs"))
      return
    }
  }, [navigate])

  useEffect(() => {
    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [handleKeyDown])
}

// category/description hold i18n keys (shortcuts.*) resolved at render time in
// ShortcutsDialog via t(); keys are literal keyboard glyphs and stay as-is.
export const shortcuts = [
  { category: "shortcuts.cat.navigation", items: [
    { keys: ["⌘", "1"], description: "shortcuts.desc.goToInbox" },
    { keys: ["⌘", "2"], description: "shortcuts.desc.goToSent" },
    { keys: ["⌘", "3"], description: "shortcuts.desc.goToDrafts" },
    { keys: ["⌘", "4"], description: "shortcuts.desc.goToTrash" },
    { keys: ["⌘", "K"], description: "shortcuts.desc.search" },
    { keys: ["/"], description: "shortcuts.desc.searchNotInput" },
    { keys: ["?"], description: "shortcuts.desc.showShortcuts" },
    { keys: ["Esc"], description: "shortcuts.desc.closeDialog" },
  ]},
  { category: "shortcuts.cat.actions", items: [
    { keys: ["⌘", "N"], description: "shortcuts.desc.composeNew" },
    { keys: ["⌘", "Shift", "I"], description: "shortcuts.desc.goToInbox" },
    { keys: ["R"], description: "shortcuts.desc.replyEmail" },
    { keys: ["A"], description: "shortcuts.desc.replyAll" },
    { keys: ["F"], description: "shortcuts.desc.forwardEmail" },
    { keys: ["E"], description: "shortcuts.desc.archiveEmail" },
    { keys: ["#"], description: "shortcuts.desc.deleteEmail" },
    { keys: ["S"], description: "shortcuts.desc.toggleStar" },
    { keys: ["U"], description: "shortcuts.desc.markUnread" },
  ]},
  { category: "shortcuts.cat.selection", items: [
    { keys: ["X"], description: "shortcuts.desc.selectEmail" },
    { keys: ["Shift", "↓"], description: "shortcuts.desc.selectNext" },
    { keys: ["Shift", "↑"], description: "shortcuts.desc.selectPrev" },
    { keys: ["*", "A"], description: "shortcuts.desc.selectAll" },
    { keys: ["*", "N"], description: "shortcuts.desc.deselectAll" },
  ]},
  { category: "shortcuts.cat.navigationInList", items: [
    { keys: ["J"], description: "shortcuts.desc.nextEmail" },
    { keys: ["K"], description: "shortcuts.desc.prevEmail" },
    { keys: ["Enter"], description: "shortcuts.desc.openEmail" },
    { keys: ["←"], description: "shortcuts.desc.goBack" },
  ]},
]
