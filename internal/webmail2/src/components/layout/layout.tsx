import { useState } from "react"
import { Outlet } from "react-router-dom"
import { Sidebar } from "./sidebar"
import { Header } from "./header"
import { ReminderOverlay } from "@/components/reminder-overlay"
import { cn } from "@/lib/utils"

export function Layout() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)

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
