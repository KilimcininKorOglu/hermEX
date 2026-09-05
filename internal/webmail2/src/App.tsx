import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom"
import { useState, useEffect } from "react"
import api from "@/utils/api"
import { ThemeProvider } from "@/components/theme-provider"
import { AuthProvider, useAuth } from "@/contexts/AuthContext"
import { MailboxProvider } from "@/contexts/MailboxContext"
import { Layout } from "@/components/layout/layout"
import { InboxPage } from "@/pages/inbox"
import { EmailDetailPage } from "@/pages/email-detail"
import { ComposePage } from "@/pages/compose"
import { SentPage } from "@/pages/sent"
import { DraftsPage } from "@/pages/drafts"
import { ScheduledPage } from "@/pages/scheduled"
import { SharedPage } from "@/pages/shared"
import { TrashPage } from "@/pages/trash"
import { ContactsPage } from "@/pages/contacts"
import { CalendarPage } from "@/pages/calendar"
import { TodayPage } from "@/pages/today"
import { TasksPage } from "@/pages/tasks"
import { NotesPage } from "@/pages/notes"
import { PublicFoldersPage } from "@/pages/public-folders"
import { SettingsPage } from "@/pages/settings"
import { SearchPage } from "@/pages/search"
import { SavedSearchPage } from "@/pages/saved-search"
import { SpamPage } from "@/pages/spam"
import { FolderPage } from "@/pages/folder"
import { FiltersPage } from "@/pages/filters"
import { GroupsPage } from "@/pages/groups"
import { ThreadsPage } from "@/pages/threads"
import { OnboardingPage } from "@/pages/onboarding"
import { ForcePasswordChangePage } from "@/pages/force-password"
import { SecondFactorPage } from "@/pages/second-factor"
import { firstGate, type Gate } from "@/utils/authGate"
import { ShortcutsDialog } from "@/components/shortcuts-dialog"
import { Toaster } from "@/components/ui/sonner"
import { useKeyboardShortcuts } from "@/hooks/useKeyboardShortcuts"
import { LoginPage } from "@/pages/login"

// StartupRedirect sends the user to their configured startup folder on login,
// falling back to /inbox. Loads the appearance settings (DB-backed) once; until
// they resolve, stay on a blank route so the redirect never flashes /inbox first.
function StartupRedirect() {
  const [target, setTarget] = useState<string | null>(null)
  useEffect(() => {
    api.getAppearanceSettings()
      .then((s) => setTarget(s.startupFolder && s.startupFolder !== "inbox" ? `/${s.startupFolder}` : "/inbox"))
      .catch(() => setTarget("/inbox"))
  }, [])
  if (!target) return null
  return <Navigate to={target} replace />
}

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth()

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-indigo-600"></div>
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  return children
}

// RequireGates sends a signed-in user to whichever screen still stands between
// them and their mail. The order lives in firstGate, which is where it is
// tested; a route that renders one of those screens excludes itself with
// `except`, so it cannot bounce to itself.
function RequireGates({ except, children }: { except?: Gate; children: React.ReactNode }) {
  const { user } = useAuth()
  const gate = firstGate(user)
  if (gate && gate !== except) {
    return <Navigate to={gate} replace />
  }
  return children
}

// OnboardingGate renders the onboarding screen, but bounces an already-onboarded
// user back to the inbox so the route cannot be revisited to redo first-run.
function OnboardingGate() {
  const { user } = useAuth()
  if (user && user.onboarded) {
    return <Navigate to="/inbox" replace />
  }
  return <OnboardingPage />
}

// SecondFactorGate renders the code prompt, bouncing a session that has already
// cleared it back to the inbox so the route cannot be revisited.
function SecondFactorGate() {
  const { user } = useAuth()
  if (user && !user.secondFactorRequired) {
    return <Navigate to="/inbox" replace />
  }
  return <SecondFactorPage />
}

// ForcePasswordGate renders the forced password-change screen, bouncing a user
// who does not need to change their password back to the inbox.
function ForcePasswordGate() {
  const { user } = useAuth()
  if (user && !user.mustChangePassword) {
    return <Navigate to="/inbox" replace />
  }
  return <ForcePasswordChangePage />
}

function AppContent() {
  const { user } = useAuth()
  useKeyboardShortcuts()

  return (
    <>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/second-factor"
          element={
            <ProtectedRoute>
              <SecondFactorGate />
            </ProtectedRoute>
          }
        />
        <Route
          path="/force-password"
          element={
            <ProtectedRoute>
              <RequireGates except="/force-password">
                <ForcePasswordGate />
              </RequireGates>
            </ProtectedRoute>
          }
        />
        <Route
          path="/onboarding"
          element={
            <ProtectedRoute>
              <RequireGates except="/onboarding">
                <OnboardingGate />
              </RequireGates>
            </ProtectedRoute>
          }
        />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <RequireGates>
                <MailboxProvider personalEmail={user?.email || ""}>
                  <Layout />
                </MailboxProvider>
              </RequireGates>
            </ProtectedRoute>
          }
        >
          <Route index element={<StartupRedirect />} />
          <Route path="today" element={<TodayPage />} />
          <Route path="compose" element={<ComposePage />} />
          <Route path="inbox" element={<InboxPage folder="inbox" />} />
          <Route path="starred" element={<InboxPage folder="starred" />} />
          <Route path="sent" element={<SentPage />} />
          <Route path="drafts" element={<DraftsPage />} />
          <Route path="scheduled" element={<ScheduledPage />} />
          <Route path="trash" element={<TrashPage />} />
          <Route path="shared" element={<SharedPage />} />
          <Route path="public-folders" element={<PublicFoldersPage />} />
          <Route path="contacts" element={<ContactsPage />} />
          <Route path="calendar" element={<CalendarPage />} />
          <Route path="tasks" element={<TasksPage />} />
          <Route path="notes" element={<NotesPage />} />
          <Route path="filters" element={<FiltersPage />} />
          <Route path="groups" element={<GroupsPage />} />
          <Route path="threads" element={<ThreadsPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="search" element={<SearchPage />} />
          <Route path="saved-search/:id" element={<SavedSearchPage />} />
          <Route path="spam" element={<SpamPage />} />
          <Route path="folder/:type" element={<FolderPage />} />
          <Route path="email/:id" element={<EmailDetailPage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      <ShortcutsDialog />
    </>
  )
}

function App() {
  return (
    <ThemeProvider defaultTheme="system" storageKey="webmail-theme">
      <AuthProvider>
        <BrowserRouter>
          <AppContent />
        </BrowserRouter>
        <Toaster />
      </AuthProvider>
    </ThemeProvider>
  )
}

export default App
