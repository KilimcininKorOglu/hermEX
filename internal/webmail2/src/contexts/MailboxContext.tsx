import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import api, { SharedMailbox, Mail } from '../utils/api'
import { useMailEvents } from '../utils/mailEvents'

interface MailboxContextType {
  // Current active mailbox context
  currentMailbox: {
    type: 'personal' | 'shared'
    email: string // The email address of the current mailbox
    owner?: string // For shared mailboxes, the owner's email
  }
  
  // List of shared mailboxes the user has access to
  sharedMailboxes: SharedMailbox[]
  
  // Loading state
  loading: boolean
  
  // Switch to a different mailbox context
  switchMailbox: (email: string, owner?: string) => void
  
  // Switch back to personal mailbox
  switchToPersonal: () => void
  
  // Load shared mailboxes
  loadSharedMailboxes: () => Promise<void>
  
  // Check if currently in a shared mailbox
  isInSharedMailbox: () => boolean

  // Shared inbox state: fetched once here and consumed by the inbox page, the
  // sidebar unread badge, and the header notifications, so they stay in sync
  // (a read/delete in one place updates the others) and the inbox is not
  // fetched three times on load.
  // inboxEmails is the CURRENT PAGE of the inbox (server-paged/sorted/filtered);
  // inboxTotal and inboxUnread are whole-folder counts for the pager and the badge.
  inboxEmails: Mail[]
  inboxUnread: number
  inboxTotal: number
  inboxPageSize: number
  inboxLoading: boolean
  inboxQuery: InboxQuery
  // setInboxQuery merges fields (page/sort/dir/filter) and refetches the page.
  setInboxQuery: (q: Partial<InboxQuery>) => void
  refreshInbox: () => Promise<void>
  // Optimistically apply changes (e.g. read/starred) to inbox messages.
  patchInbox: (ids: string[], changes: Partial<Mail>) => void
  // Optimistically drop messages from the inbox (archive/delete).
  removeFromInbox: (ids: string[]) => void
}

// InboxQuery is the server-side list query for the inbox page.
export interface InboxQuery {
  page: number
  sort: string // "date" | "from" | "subject" | "size"
  dir: string // "asc" | "desc"
  filter: string // "all" | "unread" | "starred"
}

const INBOX_PAGE_SIZE = 50

const MailboxContext = createContext<MailboxContextType | null>(null)

export function MailboxProvider({ children, personalEmail }: { children: React.ReactNode; personalEmail: string }) {
  const [currentMailbox, setCurrentMailbox] = useState<{
    type: 'personal' | 'shared'
    email: string
    owner?: string
  }>({
    type: 'personal',
    email: personalEmail
  })
  const [sharedMailboxes, setSharedMailboxes] = useState<SharedMailbox[]>([])
  const [loading, setLoading] = useState(false)
  const [inboxEmails, setInboxEmails] = useState<Mail[]>([])
  const [inboxTotal, setInboxTotal] = useState(0)
  const [inboxUnread, setInboxUnread] = useState(0)
  const [inboxLoading, setInboxLoading] = useState(true)
  const [inboxQuery, setInboxQueryState] = useState<InboxQuery>({ page: 0, sort: 'date', dir: 'desc', filter: 'all' })
  const setInboxQuery = useCallback((q: Partial<InboxQuery>) => {
    // A filter/sort change (no explicit page) resets to the first page so the user
    // never lands on an out-of-range page; an explicit page just navigates.
    setInboxQueryState((prev) => ({ ...prev, ...q, page: q.page ?? 0 }))
  }, [])

  // fetchInbox pulls the inbox without toggling the loading flag, so background
  // polling does not flash the skeleton. When a shared mailbox is active it
  // fetches the owner's inbox instead, so switching mailboxes swaps the view.
  const sharedOwner = currentMailbox.type === 'shared' ? currentMailbox.owner : undefined
  const fetchInbox = useCallback(async () => {
    const res = await api.getMail('inbox', sharedOwner, {
      page: inboxQuery.page,
      pageSize: INBOX_PAGE_SIZE,
      sort: inboxQuery.sort,
      dir: inboxQuery.dir,
      filter: inboxQuery.filter,
    })
    setInboxEmails(res.emails ?? [])
    setInboxTotal(res.total ?? 0)
    setInboxUnread(res.unread ?? 0)
  }, [sharedOwner, inboxQuery])

  const refreshInbox = useCallback(async () => {
    setInboxLoading(true)
    try {
      await fetchInbox()
    } catch {
      setInboxEmails([])
    } finally {
      setInboxLoading(false)
    }
  }, [fetchInbox])

  useEffect(() => {
    refreshInbox()
  }, [refreshInbox])

  // Real-time inbox updates (push-to-pull): the shared SSE stream signals when
  // something changes and the UI fetches over HTTP in response, so the inbox
  // (and its sidebar unread badge) update instantly without aggressive polling.
  // Runs even while another folder is on screen, keeping the badge live.
  useMailEvents(() => {
    fetchInbox().catch(() => undefined)
  })

  // Fallback safety net for when the SSE stream is unavailable: a slow poll plus
  // a refresh when the tab regains focus. Kept long (push drives immediacy) so
  // background traffic stays minimal.
  useEffect(() => {
    const refresh = () => {
      fetchInbox().catch(() => undefined)
    }
    const interval = setInterval(refresh, 300000)
    const onVisible = () => {
      if (document.visibilityState === 'visible') refresh()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(interval)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [fetchInbox])

  const patchInbox = useCallback((ids: string[], changes: Partial<Mail>) => {
    const idset = new Set(ids)
    setInboxEmails((prev) => prev.map((m) => (idset.has(m.id) ? { ...m, ...changes } : m)))
  }, [])

  const removeFromInbox = useCallback((ids: string[]) => {
    const idset = new Set(ids)
    setInboxEmails((prev) => prev.filter((m) => !idset.has(m.id)))
  }, [])

  const loadSharedMailboxes = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.getSharedMailboxes()
      if (result.shared_mailboxes) {
        setSharedMailboxes(result.shared_mailboxes)
      }
    } catch (err) {
      console.error('Failed to load shared mailboxes:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  const switchMailbox = useCallback((email: string, owner?: string) => {
    // A mailbox is shared when its owner is not the signed-in user. A pure shared
    // mailbox has owner === email (both the shared address), so comparing the
    // owner to the personal email — not to email — is what distinguishes it.
    if (owner && owner !== personalEmail) {
      // This is a shared mailbox: route every subsequent mail call to the owner.
      api.setMailboxOwner(owner)
      setCurrentMailbox({
        type: 'shared',
        email,
        owner
      })
    } else {
      // Personal mailbox
      api.setMailboxOwner(undefined)
      setCurrentMailbox({
        type: 'personal',
        email
      })
    }
  }, [personalEmail])

  const switchToPersonal = useCallback(() => {
    api.setMailboxOwner(undefined)
    setCurrentMailbox({
      type: 'personal',
      email: personalEmail
    })
  }, [personalEmail])

  const isInSharedMailbox = useCallback(() => {
    return currentMailbox.type === 'shared'
  }, [currentMailbox.type])

  const value: MailboxContextType = {
    currentMailbox,
    sharedMailboxes,
    loading,
    switchMailbox,
    switchToPersonal,
    loadSharedMailboxes,
    isInSharedMailbox,
    inboxEmails,
    inboxUnread,
    inboxTotal,
    inboxPageSize: INBOX_PAGE_SIZE,
    inboxLoading,
    inboxQuery,
    setInboxQuery,
    refreshInbox,
    patchInbox,
    removeFromInbox
  }

  return (
    <MailboxContext.Provider value={value}>
      {children}
    </MailboxContext.Provider>
  )
}

export function useMailbox() {
  const context = useContext(MailboxContext)
  if (!context) {
    throw new Error('useMailbox must be used within a MailboxProvider')
  }
  return context
}
