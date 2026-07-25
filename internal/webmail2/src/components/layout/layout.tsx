import { useState, useEffect, useRef } from "react"
import { Outlet, useNavigate } from "react-router-dom"
import { Sidebar } from "./sidebar"
import { Header } from "./header"
import { ReminderOverlay } from "@/components/reminder-overlay"
import { useMailbox } from "@/contexts/MailboxContext"
import api from "@/utils/api"
import { cn } from "@/lib/utils"

// baseTitle is the app title without any unread-count prefix; the title-counter
// effect restores it whenever the counter is off or the inbox is all read.
const baseTitle = "hermEX Webmail"

export function Layout() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)
  const [showUnreadCounter, setShowUnreadCounter] = useState(false)
  const navigate = useNavigate()
  const { inboxUnread } = useMailbox()

  // The unread-counter-in-title preference lives in the DB-backed appearance
  // settings; fetch it once and re-read when the settings page saves a change
  // (it dispatches "appearance-changed"), so the title reacts without a reload.
  useEffect(() => {
    const load = () => {
      api.getAppearanceSettings()
        .then((s) => setShowUnreadCounter(!!s.showUnreadCounter))
        .catch(() => undefined)
    }
    load()
    document.addEventListener("appearance-changed", load)
    return () => document.removeEventListener("appearance-changed", load)
  }, [])

  // Reflect the inbox unread count in the browser tab/title when the preference
  // is on (mirrors the reference "Show unread mail counter in application title").
  useEffect(() => {
    if (showUnreadCounter && inboxUnread > 0) {
      document.title = `(${inboxUnread}) ${baseTitle}`
    } else {
      document.title = baseTitle
    }
  }, [showUnreadCounter, inboxUnread])
  // Desktop notifications: when the inbox unread count rises, fire a browser
  // Notification (permission gated). The ref holds the previous count so a
  // refresh that lands on the same unread total does not spam a notice.
  const prevUnread = useRef(inboxUnread)
  useEffect(() => {
    if (inboxUnread > prevUnread.current) {
      if (typeof Notification !== "undefined" && Notification.permission === "granted") {
        try {
          new Notification("hermEX Webmail", { body: `${inboxUnread} unread message${inboxUnread === 1 ? "" : "s"}` })
        } catch {
          /* best-effort */
        }
      }
    }
    prevUnread.current = inboxUnread
  }, [inboxUnread])

  // Global keyboard shortcuts (ignored while typing in an input/textarea/contenteditable
  // so they never swallow ordinary typing):
  //   c → compose, / → focus the header search, g t → Today, g i → Inbox, g c → Calendar.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      const typing = el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)
      if (typing) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (e.key === "c") {
        e.preventDefault()
        navigate("/compose")
      } else if (e.key === "/") {
        e.preventDefault()
        const s = document.querySelector<HTMLInputElement>('[aria-label="search"]')
        s?.focus()
      } else if (e.key === "?") {
        e.preventDefault()
        alert("Keyboard shortcuts\n\nc — Compose\n/ — Focus search\nj / ↓ — Next message\nk / ↑ — Previous message\nEnter — Open message")
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [navigate])

  return (
    <div className="min-h-screen bg-background">
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggle={() => setSidebarCollapsed(!sidebarCollapsed)}
        mobileOpen={mobileMenuOpen}
        onMobileClose={() => setMobileMenuOpen(false)}
      />

      <Header
        onMenuToggle={() => setMobileMenuOpen(!mobileMenuOpen)}
        sidebarCollapsed={sidebarCollapsed}
      />

      {/* Backdrop for the mobile sidebar */}
      {mobileMenuOpen && (
        <div
          className="fixed inset-0 z-30 bg-black/40 lg:hidden"
          onClick={() => setMobileMenuOpen(false)}
        />
      )}

      <main
        className={cn(
          "pt-16 transition-all duration-300",
          sidebarCollapsed ? "lg:pl-16" : "lg:pl-64"
        )}
      >
        <div className="p-4 lg:p-6">
          <Outlet />
        </div>
      </main>

      {/* Calendar reminder engine: fires a snooze/dismiss popup app-wide when a
          reminder is due, regardless of the page the user is on. */}
      <ReminderOverlay />
    </div>
  )
}
