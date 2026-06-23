import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom"
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
import { TasksPage } from "@/pages/tasks"
import { NotesPage } from "@/pages/notes"
import { SettingsPage } from "@/pages/settings"
import { SearchPage } from "@/pages/search"
import { SavedSearchPage } from "@/pages/saved-search"
import { SpamPage } from "@/pages/spam"
import { FolderPage } from "@/pages/folder"
import { FiltersPage } from "@/pages/filters"
import { ThreadsPage } from "@/pages/threads"
import { OnboardingPage } from "@/pages/onboarding"
import { ShortcutsDialog } from "@/components/shortcuts-dialog"
import { Toaster } from "@/components/ui/sonner"
import { useKeyboardShortcuts } from "@/hooks/useKeyboardShortcuts"
import { LoginPage } from "@/pages/login"

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

// RequireOnboarded sends a signed-in user who has not finished first-run
// onboarding to /onboarding. It fires ONLY when the flag is explicitly false
// (a fresh/unmigrated account); when it is undefined — e.g. a login fallback
// that could not read /auth/me — the user is not trapped.
function RequireOnboarded({ children }: { children: React.ReactNode }) {
  const { user } = useAuth()
  if (user && user.onboarded === false) {
    return <Navigate to="/onboarding" replace />
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

function AppContent() {
  const { user } = useAuth()
  useKeyboardShortcuts()

  return (
    <>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/onboarding"
          element={
            <ProtectedRoute>
              <OnboardingGate />
            </ProtectedRoute>
          }
        />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <RequireOnboarded>
                <MailboxProvider personalEmail={user?.email || ""}>
                  <Layout />
                </MailboxProvider>
              </RequireOnboarded>
            </ProtectedRoute>
          }
        >
          <Route index element={<Navigate to="/inbox" replace />} />
          <Route path="compose" element={<ComposePage />} />
          <Route path="inbox" element={<InboxPage folder="inbox" />} />
          <Route path="starred" element={<InboxPage folder="starred" />} />
          <Route path="sent" element={<SentPage />} />
          <Route path="drafts" element={<DraftsPage />} />
          <Route path="scheduled" element={<ScheduledPage />} />
          <Route path="trash" element={<TrashPage />} />
          <Route path="shared" element={<SharedPage />} />
          <Route path="contacts" element={<ContactsPage />} />
          <Route path="calendar" element={<CalendarPage />} />
          <Route path="tasks" element={<TasksPage />} />
          <Route path="notes" element={<NotesPage />} />
          <Route path="filters" element={<FiltersPage />} />
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
