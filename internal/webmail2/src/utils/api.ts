const API_URL = window.location.origin + '/api/v1'

// maxBulkExport mirrors the server cap on a single bulk EML export; the UI warns
// when a selection exceeds it rather than letting the overflow drop silently.
export const maxBulkExport = 200

// ownerQuery builds the optional `owner=` query fragment used to target a shared
// mailbox. `sep` is "?" when the URL has no query yet, otherwise "&". Returns ""
// when no owner is given (personal mailbox), so the same call serves both.
function ownerQuery(owner: string | undefined, sep: '?' | '&'): string {
  return owner ? `${sep}owner=${encodeURIComponent(owner)}` : ''
}

// ============================================================================
// Type Definitions
// ============================================================================

export interface Mail {
  id: string
  from: string // bare sender address
  fromName: string // sender display name ("" when unknown)
  to: string[] // bare recipient addresses
  toNames?: string[] // display name per recipient (same index, "" when unknown)
  subject: string
  body: string
  preview: string
  date: string
  read: boolean
  starred: boolean
  folder: string
  hasAttachments: boolean
  size: number
  senderTrusted?: boolean // sender is on the safe-sender allowlist → load remote images automatically
  labels?: string[]
  attachments?: AttachmentInfo[]
  importance?: string // "low" | "normal" | "high"
  sensitivity?: string // "personal" | "private" | "confidential"
  smimeSigned?: boolean // message carries an S/MIME signature
  smimeEncrypted?: boolean // message was S/MIME encrypted (decrypted for display)
  smimeVerified?: boolean // the signature verified successfully
  smimeSignedBy?: string // the signer's address
  followupStatus?: number // follow-up flag: 0 none, 1 complete, 2 flagged
  followupColor?: number // follow-up flag colour: 1..6 (purple..red)
  followupDue?: string // follow-up due date (RFC3339), empty when unset
}

/** RecoverableItem is one message in a folder's Recoverable Items dumpster. */
export interface RecoverableItem {
  id: string
  subject: string
  from: string
  date: string
  deletedOn: string
  size: number
}

/** PublicFolder is one organization public folder the caller may read. */
export interface PublicFolder {
  id: number
  name: string
  total: number
  unread: number
  can_post: boolean
}

export interface MailAttachment {
  filename: string
  contentType: string
  content: string // base64-encoded file bytes
}

// RecallResult summarizes a recall attempt: how many recipient copies were
// pulled, the total recipients tried, and the per-recipient outcome
// ("recalled" | "read" | "unavailable").
export interface RecallResult {
  id: string
  recalled: number
  total: number
  results: { recipient: string; status: 'recalled' | 'read' | 'unavailable' }[]
}

// AttachmentInfo describes a received message's attachment (metadata only).
// The bytes are fetched on demand by index.
export interface AttachmentInfo {
  filename: string
  contentType: string
  size: number
  index: number
}

export interface SendMailRequest {
  to: string[]
  cc?: string[]
  bcc?: string[]
  subject: string
  body: string
  from?: string // Sender identity for send-as or send-on-behalf
  attachments?: MailAttachment[]
  requestReadReceipt?: boolean // ask the recipient's client for a read receipt
  signMessage?: boolean // S/MIME sign the message
  encryptMessage?: boolean // S/MIME encrypt the message
  importance?: string // "low" | "normal" | "high"
  sensitivity?: string // "personal" | "private" | "confidential"
  // sendAt, when a future absolute RFC3339 instant, defers delivery: the server
  // releases the message at that time instead of sending now.
  sendAt?: string
  // is_html, when true, indicates the body is an HTML document to be sent
  // with Content-Type: text/html. When false or absent, body is sent as text/plain.
  is_html?: boolean
}

// ScheduledMailItem is one pending/failed "send later" message in the Scheduled view.
export interface ScheduledMailItem {
  id: string
  to: string[]
  subject: string
  sendAt: string // absolute RFC3339 (UTC)
  status: string // pending | sending | failed
  error?: string
}

export interface CalendarEvent {
  uid: string
  summary: string
  description?: string
  location?: string
  start: string // RFC3339, or YYYY-MM-DD when allDay
  end?: string
  allDay?: boolean
  organizer?: string
  attendees?: string[]
  recurrence?: string // raw RRULE value, e.g. "FREQ=WEEKLY"
  timezone?: string // IANA zone anchoring civil start/end (recurrence DST safety)
}

export type CalendarEventInput = Omit<CalendarEvent, "uid"> & { uid?: string }

export interface Calendar {
  id: string
  name: string
  description?: string
  color?: string
  isDefault?: boolean
}

export type CalendarInput = Pick<Calendar, "name" | "description" | "color">

export interface Room {
  email: string
  name: string
  capacity?: number
}

export interface BusyInterval {
  start: string // RFC3339 UTC
  end: string // RFC3339 UTC
}

export interface UserFreeBusy {
  user: string
  busy: BusyInterval[]
}

export interface Task {
  uid: string
  summary: string
  description?: string
  due?: string // RFC3339 or YYYY-MM-DD
  completed: boolean
}

export type TaskInput = Omit<Task, "uid"> & { uid?: string }

export interface Note {
  id: string
  title: string
  body: string
  created?: string
  updated?: string
}

export type NoteInput = { title: string; body: string }

export interface Delegation {
  id: string
  owner: string
  grantee: string
  mailbox: string
  rights: string
  canSendAs: boolean
  canSendOnBehalf: boolean
  createdAt: string
}

export interface DelegationInput {
  grantee: string
  rights: string[]
  canSendAs?: boolean
  canSendOnBehalf?: boolean
}

// ACL entry for folder sharing (RFC 4314)
export interface ACLEntry {
  Grantee: string
  Rights: string // human-readable string from server, e.g. "lrs"
}

// Folder permission levels mapped to RFC 4314 bitmasks (matching backend preset constants)
export const FOLDER_PERMISSION_LEVELS = [
  { label: "Reviewer (read)", value: "reviewer", rights: 3 },    // ACLLookup | ACLRead
  { label: "Author (read+write)", value: "author", rights: 27 }, // ACLLookup|ACLRead|ACLSeen|ACLWrite|ACLDelete
  { label: "Editor (full)", value: "editor", rights: 239 },       // ACLAll
] as const

export type FolderPermissionLevel = "reviewer" | "author" | "editor"

export function rightsToLevel(rights: number): FolderPermissionLevel {
  if (rights >= 239) return "editor"
  if (rights >= 27) return "author"
  return "reviewer"
}

export function levelToRights(level: FolderPermissionLevel): number {
  const found = FOLDER_PERMISSION_LEVELS.find(l => l.value === level)
  return found?.rights ?? 3
}

export interface SMIMECertInfo {
  subject: string
  issuer: string
  notBefore: string
  notAfter: string
  serialNumber: string
  fingerprint: string
  hasPrivateKey?: boolean
  mode?: string // "browser" | "server"
}

export interface DirectoryEntry {
  email: string
  name: string
  photo?: string // avatar endpoint URL when the user has a profile photo
}

// UserProfile is the caller's own editable directory profile.
export interface UserProfile {
  email?: string
  display_name?: string
  title?: string
  department?: string
  phone?: string
  timezone?: string
  locale?: string
  theme?: string
  onboarded?: boolean
  // Read-only storage usage and graduated quota thresholds (absolute bytes,
  // 0 = disabled/unlimited). Surfaced by GET /profile for the storage gauge;
  // never writable through updateProfile.
  quota_used?: number
  quota_limit?: number
  quota_warn?: number
  quota_prohibit_send?: number
}

export interface SignatureEntry {
  name: string
  body: string
  is_html: boolean
  ord: number
}

export interface TemplateEntry {
  name: string
  subject: string
  body: string
  is_html: boolean
}

export interface Category {
  name: string
  color: string
}

/** RecipientRule is one personal allow/block rule the MTA applies at delivery. */
export interface RecipientRule {
  pattern: string
  action: 'allow' | 'block'
}

export interface MeetingInvite {
  isInvite: boolean
  uid?: string
  summary?: string
  start?: string
  end?: string
  location?: string
  organizer?: string
}

export interface AuthLoginRequest {
  email: string
  password: string
}

export interface AuthLoginResponse {
  expiresIn?: number
}

// Filter mirrors the backend /api/v1/filters contract
// (internal/api/filters.go EmailFilter): camelCase JSON keys.
export interface Filter {
  id: string
  name: string
  enabled: boolean
  matchAll: boolean
  conditions: FilterCondition[]
  actions: FilterAction[]
  priority: number
}

// FilterField mirrors semcore.RuleConditionKind.
export type FilterField = 'from' | 'to' | 'subject' | 'body' | 'header' | 'size' | 'flag' | 'address'

// FilterOperator mirrors semcore.RuleMatchType.
export type FilterOperator = 'contains' | 'equals' | 'startsWith' | 'endsWith' | 'matches'

export interface FilterCondition {
  field: FilterField
  operator: FilterOperator
  value: string
  headerName?: string
}

// FilterActionType mirrors semcore.RuleActionKind.String(). The full vocabulary
// is represented so rules created in Outlook/admin round-trip without loss.
export type FilterActionType =
  | 'moveToFolder'
  | 'copyToFolder'
  | 'delete'
  | 'markRead'
  | 'markImportant'
  | 'forward'
  | 'forwardAsAttachment'
  | 'redirect'
  | 'reject'
  | 'addHeader'
  | 'deleteHeader'
  | 'flag'
  | 'stop'
  | 'vacation'

export interface FilterAction {
  type: FilterActionType
  target?: string
  forwardTo?: string
  message?: string
  headerName?: string
  headerValue?: string
  flagName?: string
  clearFlag?: boolean
}

// FilterInput is the create/update payload the backend accepts.
export interface FilterInput {
  name: string
  enabled?: boolean
  matchAll: boolean
  conditions: FilterCondition[]
  actions: FilterAction[]
}

// RwzImportResult is the JSON returned by POST /api/v1/filters/import: how many
// rules were created and what could not be represented (Outlook .rwz import).
export interface RwzImportResult {
  imported: number
  skippedRules: number
  skippedElements: number
  notes?: string[]
}

// VacationAutoReply mirrors the backend /api/v1/vacation contract
// (internal/api/vacation.go VacationConfig): snake_case JSON keys, with
// `message` as the reply body and RFC3339 date strings.
export interface VacationAutoReply {
  enabled: boolean
  subject: string
  message: string
  html_message?: string
  /** Reply sent to senders outside the organization; falls back to `message`. */
  external_message?: string
  /** Who receives the auto-reply: "internal" | "external" | "all" (default "all"). */
  audience?: string
  start_date?: string
  end_date?: string
  send_interval?: number
  exclude_addresses?: string[]
  ignore_lists?: boolean
  ignore_bulk?: boolean
}

export interface ClientSession {
  id: string
  device_type: string
  client_ip: string
  created_at: string
  last_active: string
  user_agent: string
}

export interface PushSubscription {
  endpoint: string
  keys: {
    p256dh: string
    auth: string
  }
}

export interface SearchResponse {
  emails: Mail[]
  total: number
  query: string
}

/**
 * A persistent saved search (MAPI-style search folder). The criteria fields are
 * optional; an empty field imposes no constraint. base_folders names the
 * mailboxes to search, or all mailboxes when omitted.
 */
export interface SearchFolder {
  id: string
  name: string
  from?: string
  subject?: string
  body?: string
  date_from?: string
  date_to?: string
  has_attachment?: boolean
  base_folders?: string[]
}

/** SearchFolderInput is the create/update payload (no server-assigned id). */
export type SearchFolderInput = Omit<SearchFolder, 'id'>

export interface ThreadsResponse {
  threads: Thread[]
}

export interface Thread {
  key: string
  subject: string
  messages: Mail[]
  participants: string[]
  lastDate: string
  unread: number
}

// Shared mailbox and sender identity types
export interface SharedMailbox {
  owner: string
  mailbox: string
  displayName?: string
  rights?: string
}

export interface SenderIdentity {
  email: string
  displayName?: string
  type: 'personal' | 'send-as' | 'send-on-behalf'
  mailboxOwner?: string // for shared mailbox identities
  canSend: boolean
}

export interface DiagnosticEntry {
  id: string
  severity: 'error' | 'warning' | 'info'
  category: 'policy' | 'sync' | 'delivery' | 'auth' | 'access'
  message: string
  mailbox?: string
  timestamp: string
  retryable: boolean
  nextStep?: string
}

// Contact type for address book
export interface Contact {
  id: string
  name: string
  email: string
  phone?: string
  company?: string
  labels?: string[]
  display_as?: string
  is_group?: boolean
  members?: string[]
}

// ============================================================================
// API Client
// ============================================================================

interface RequestOptions extends RequestInit {
  headers?: Record<string, string>
}

interface ApiResponse<T = unknown> {
  data?: T
  [key: string]: unknown
}

class API {
  private token: string | null
  // mailboxOwner, when set, makes every mail call target a shared mailbox the
  // user has access to (the owner's address). MailboxContext sets it on switch
  // and clears it when returning to the personal mailbox, so page components
  // need no per-call wiring — they automatically follow the active mailbox.
  mailboxOwner: string | undefined

  constructor() {
    // Token is now stored in HttpOnly cookie by the server
    // No need to read from localStorage (more secure against XSS)
    this.token = null
    this.mailboxOwner = undefined
  }

  setToken(token: string | null): void {
    this.token = token
  }

  // setMailboxOwner switches the active mailbox for subsequent mail calls.
  // Pass undefined (or the user's own address) to return to the personal mailbox.
  setMailboxOwner(owner: string | undefined): void {
    this.mailboxOwner = owner
  }

  async request<T = unknown>(endpoint: string, options: RequestOptions = {}): Promise<T> {
    const url = API_URL + endpoint

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...options.headers
    }

    // Token is sent automatically via HttpOnly cookie
    // No need to set Authorization header for web clients
    // For API clients that still use Bearer token, we keep the header support
    if (this.token) {
      headers['Authorization'] = `Bearer ${this.token}`
    }

    try {
      const response = await fetch(url, {
        ...options,
        headers,
        credentials: 'include' // Send HttpOnly cookies with requests
      })

      if (!response.ok) {
        // A 401 on an auth endpoint (login/refresh/logout) is a real result the
        // caller must handle (e.g. show "invalid credentials"); only a 401 on a
        // normal API call means the session expired, so bounce to /login then.
        if (response.status === 401 && !endpoint.startsWith('/auth/')) {
          if (window.location.pathname !== '/login') {
            window.location.href = '/login'
          }
          return null as T
        }
        throw new Error(`HTTP ${response.status}`)
      }

      const contentType = response.headers.get('content-type')
      if (contentType && contentType.includes('application/json')) {
        return await response.json() as T
      }
      return await response.text() as unknown as T
    } catch (error) {
      console.error('API error:', error)
      throw error
    }
  }

  // Auth
  async login(credentials: AuthLoginRequest): Promise<AuthLoginResponse> {
    return this.post<AuthLoginResponse>('/auth/login', credentials)
  }

  // logout invalidates the session cookie on the server.
  async logout(): Promise<void> {
    await this.post('/auth/logout')
  }

  // me returns the caller's session status for rehydration. /auth/me is a soft
  // check that answers 200 either way: authenticated=false when no valid session
  // exists (so the login screen never logs a 401), otherwise the identity.
  async me(): Promise<{ authenticated: boolean; email?: string; isAdmin?: boolean; has_avatar?: boolean; onboarded?: boolean; timezone?: string; locale?: string; theme?: string; must_change_password?: boolean }> {
    return this.get<{ authenticated: boolean; email?: string; isAdmin?: boolean; has_avatar?: boolean; onboarded?: boolean; timezone?: string; locale?: string; theme?: string; must_change_password?: boolean }>('/auth/me')
  }

  // changePassword updates the authenticated user's own password.
  async changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await this.post('/account/password', { currentPassword, newPassword })
  }

  // avatarUrl returns the endpoint that serves a user's profile photo. Auth
  // rides the same-origin session cookie, so it can be used directly as an
  // <img> src. cacheBust forces a reload after the photo changes.
  avatarUrl(email: string, cacheBust?: number): string {
    const base = `${API_URL}/avatar?email=${encodeURIComponent(email)}`
    return cacheBust ? `${base}&v=${cacheBust}` : base
  }

  // updateAvatar uploads the authenticated user's own profile photo as a data URL.
  async updateAvatar(dataURL: string): Promise<void> {
    await this.put('/profile/avatar', { avatar: dataURL })
  }

  // removeAvatar deletes the authenticated user's own profile photo.
  async removeAvatar(): Promise<void> {
    await this.delete('/profile/avatar')
  }

  // getProfile returns the authenticated user's own directory profile fields.
  async getProfile(): Promise<UserProfile> {
    return this.get<UserProfile>('/profile')
  }

  // updateProfile updates the authenticated user's own directory profile fields.
  async updateProfile(profile: UserProfile): Promise<UserProfile> {
    return this.put<UserProfile>('/profile', profile)
  }

  // Self-service delegation (the authenticated user is always the owner)
  async getDelegations(): Promise<{ delegations?: Delegation[] }> {
    return this.get<{ delegations?: Delegation[] }>('/delegations')
  }

  async createDelegation(input: DelegationInput): Promise<Delegation> {
    return this.post<Delegation>('/delegations', input)
  }

  async deleteDelegation(id: string): Promise<void> {
    await this.delete(`/delegations/${encodeURIComponent(id)}`)
  }

  // Per-user UI preferences (settings toggles)
  async getPreferences(): Promise<{ preferences?: Record<string, boolean> }> {
    return this.get<{ preferences?: Record<string, boolean> }>('/preferences')
  }

  async setPreferences(prefs: Record<string, boolean>): Promise<void> {
    await this.put('/preferences', prefs)
  }

  // Per-user outgoing-mail signature
  async getSignature(): Promise<{ signature?: string }> {
    return this.get<{ signature?: string }>('/signature')
  }

  async setSignature(signature: string): Promise<void> {
    await this.put('/signature', { signature })
  }

  // Multi-signature management
  async getSignatures(): Promise<{ signatures?: SignatureEntry[] }> {
    return this.get<{ signatures?: SignatureEntry[] }>('/signatures')
  }

  async saveSignature(entry: SignatureEntry): Promise<{ signature?: SignatureEntry }> {
    return this.post<{ signature?: SignatureEntry }>('/signatures', entry)
  }

  async deleteSignature(name: string): Promise<void> {
    await this.delete('/signatures?name=' + encodeURIComponent(name))
  }

  // Message templates / snippets
  async getTemplates(): Promise<{ templates?: TemplateEntry[] }> {
    return this.get<{ templates?: TemplateEntry[] }>('/templates')
  }

  async saveTemplate(entry: TemplateEntry): Promise<{ template?: TemplateEntry }> {
    return this.post<{ template?: TemplateEntry }>('/templates', entry)
  }

  async deleteTemplate(name: string): Promise<void> {
    await this.delete('/templates?name=' + encodeURIComponent(name))
  }

  // Active client sessions
  async getSessions(): Promise<{ sessions?: ClientSession[] }> {
    return this.get<{ sessions?: ClientSession[] }>('/sessions')
  }

  async revokeSession(id: string): Promise<void> {
    await this.delete(`/sessions/${encodeURIComponent(id)}`)
  }

  // Mail. An optional `owner` targets a shared mailbox the caller has access to,
  // so the same endpoints serve both the personal and shared mailbox views.
  async getMail(
    folder: string,
    owner?: string,
    opts?: { page?: number; pageSize?: number; sort?: string; dir?: string; filter?: string }
  ): Promise<{ emails?: Mail[]; total?: number; unread?: number }> {
    const params = new URLSearchParams()
    const o = owner ?? this.mailboxOwner
    if (o) params.set('owner', o)
    if (opts?.page != null) params.set('page', String(opts.page))
    if (opts?.pageSize != null) params.set('pageSize', String(opts.pageSize))
    if (opts?.sort) params.set('sort', opts.sort)
    if (opts?.dir) params.set('dir', opts.dir)
    if (opts?.filter) params.set('filter', opts.filter)
    const qs = params.toString()
    return this.get<{ emails?: Mail[]; total?: number; unread?: number }>(`/mail/${folder}${qs ? '?' + qs : ''}`)
  }

  // getMessage fetches a single message by id (resolved across all folders).
  async getMessage(id: string, owner?: string): Promise<Mail> {
    return this.get<Mail>(`/mail/message?id=${encodeURIComponent(id)}${ownerQuery(owner ?? this.mailboxOwner, '&')}`)
  }

  // downloadAttachment fetches one attachment of a received message by index and
  // triggers a browser save. Auth rides the HttpOnly cookie (and Bearer if set).
  async downloadAttachment(id: string, index: number, filename: string): Promise<void> {
    const headers: Record<string, string> = {}
    if (this.token) headers['Authorization'] = `Bearer ${this.token}`
    const res = await fetch(
      `${API_URL}/mail/attachment?id=${encodeURIComponent(id)}&index=${index}`,
      { headers, credentials: 'include' }
    )
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename || 'attachment'
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  // setMailLabels replaces the category labels on a message.
  async setMailLabels(id: string, labels: string[]): Promise<{ id: string; labels: string[] }> {
    return this.post<{ id: string; labels: string[] }>('/mail/labels', { id, labels })
  }

  // getInvite reports whether a message is a meeting invite and returns its details.
  async getInvite(id: string): Promise<MeetingInvite> {
    return this.get<MeetingInvite>(`/mail/invite?id=${encodeURIComponent(id)}`)
  }

  // rsvp responds to a meeting invite; accept/tentative add it to the calendar.
  async rsvp(id: string, response: 'accept' | 'tentative' | 'decline'): Promise<{ status: string }> {
    return this.post<{ status: string }>('/mail/rsvp', { id, response })
  }

  // getCategories returns the user's master category list (name + color).
  async getCategories(): Promise<{ categories?: Category[] }> {
    return this.get<{ categories?: Category[] }>('/categories')
  }

  // setCategories replaces the user's master category list.
  async setCategories(categories: Category[]): Promise<{ categories?: Category[] }> {
    return this.put<{ categories?: Category[] }>('/categories', { categories })
  }

  // getSafeSenders returns the remote-content allowlist (addresses/domains whose
  // messages load remote images automatically).
  async getSafeSenders(): Promise<{ safeSenders?: string[] }> {
    return this.get<{ safeSenders?: string[] }>('/safe-senders')
  }

  // setSafeSenders replaces the safe-sender allowlist (the server normalizes it).
  async setSafeSenders(safeSenders: string[]): Promise<{ safeSenders?: string[] }> {
    return this.put<{ safeSenders?: string[] }>('/safe-senders', { safeSenders })
  }

  // getRecipientRules returns the user's personal allow/block rules; the MTA reads
  // them at delivery (allow rescues to the inbox, block always files to Junk).
  async getRecipientRules(): Promise<{ rules?: RecipientRule[] }> {
    return this.get<{ rules?: RecipientRule[] }>('/recipient-rules')
  }

  // setRecipientRule upserts one allow/block rule for a pattern.
  async setRecipientRule(pattern: string, action: 'allow' | 'block'): Promise<{ ok?: boolean }> {
    return this.post<{ ok?: boolean }>('/recipient-rules', { pattern, action })
  }

  // deleteRecipientRule removes a rule by pattern.
  async deleteRecipientRule(pattern: string): Promise<void> {
    await this.delete('/recipient-rules?pattern=' + encodeURIComponent(pattern))
  }

  // getMailboxes returns the user's mailbox names.
  async getMailboxes(): Promise<{ mailboxes?: string[] }> {
    return this.get<{ mailboxes?: string[] }>('/mailboxes')
  }

  // getPublicFolders returns the organization public folders in the caller's
  // domain that the caller may read, plus the owner key to pass to getMail.
  // Empty when the feature is disabled.
  async getPublicFolders(): Promise<{ owner?: string; folders?: PublicFolder[] }> {
    return this.get<{ owner?: string; folders?: PublicFolder[] }>('/public-folders')
  }

  // getPublicFolderMessages lists one public folder's messages by folder id.
  async getPublicFolderMessages(fid: number): Promise<{ emails?: Mail[]; total?: number }> {
    return this.get<{ emails?: Mail[]; total?: number }>(`/public-folders/${fid}/messages`)
  }

  // getPublicMessage reads one public-folder message (read-only; never marks seen).
  async getPublicMessage(fid: number, uid: string): Promise<Mail> {
    return this.get<Mail>(`/public-message?fid=${fid}&uid=${encodeURIComponent(uid)}`)
  }

  // searchDirectory resolves names/addresses from the organization directory
  // (GAL) for recipient autocomplete. Optional offset/limit for paginated browsing.
  async searchDirectory(query: string, offset?: number, limit?: number): Promise<{
    entries?: DirectoryEntry[]
    total?: number
    offset?: number
    limit?: number
  }> {
    const params = new URLSearchParams()
    if (query) params.set("q", query)
    if (offset !== undefined) params.set("offset", String(offset))
    if (limit !== undefined) params.set("limit", String(limit))
    const qs = params.toString()
    return this.get<{ entries?: DirectoryEntry[]; total?: number; offset?: number; limit?: number }>(
      `/directory${qs ? "?" + qs : ""}`
    )
  }

  // Custom folder management (built-in folders cannot be renamed/deleted).
  async createFolder(name: string): Promise<{ name: string }> {
    return this.post<{ name: string }>('/folders', { name })
  }

  async renameFolder(current: string, name: string): Promise<{ name: string }> {
    return this.put<{ name: string }>(`/folders/${encodeURIComponent(current)}`, { name })
  }

  async deleteFolder(name: string): Promise<void> {
    await this.delete(`/folders/${encodeURIComponent(name)}`)
  }

  // emptyFolder removes every message from a folder: permanently from Trash/Spam,
  // otherwise the messages move to Trash. name is a built-in slug (trash/spam) or
  // a custom folder's display name.
  async emptyFolder(name: string): Promise<{ emptied?: number }> {
    return this.post<{ emptied?: number }>(`/folders/${encodeURIComponent(name)}/empty`, {})
  }

  // Favourite (pinned) folders, persisted in webmail settings.
  async getFavorites(): Promise<{ favorites?: string[] }> {
    return this.get<{ favorites?: string[] }>('/favorites')
  }

  async toggleFavorite(name: string): Promise<{ favorites?: string[] }> {
    return this.post<{ favorites?: string[] }>('/favorites/toggle', { name })
  }

  // Saved searches (persistent MAPI-style search folders).
  async listSearchFolders(): Promise<{ search_folders?: SearchFolder[] }> {
    return this.get<{ search_folders?: SearchFolder[] }>('/search-folders')
  }

  async createSearchFolder(input: SearchFolderInput): Promise<SearchFolder> {
    return this.post<SearchFolder>('/search-folders', input)
  }

  async updateSearchFolder(id: string, input: SearchFolderInput): Promise<SearchFolder> {
    return this.put<SearchFolder>(`/search-folders/${encodeURIComponent(id)}`, input)
  }

  async deleteSearchFolder(id: string): Promise<void> {
    await this.delete(`/search-folders/${encodeURIComponent(id)}`)
  }

  async getSearchFolderResults(id: string): Promise<{ emails?: Mail[]; total?: number }> {
    return this.get<{ emails?: Mail[]; total?: number }>(`/search-folders/${encodeURIComponent(id)}/results`)
  }

  async sendMail(mail: SendMailRequest): Promise<void> {
    await this.post('/mail/send', mail)
  }

  // listScheduled returns the caller's pending/failed "send later" messages.
  async listScheduled(): Promise<ScheduledMailItem[]> {
    const res = await this.get<{ scheduled: ScheduledMailItem[] }>('/scheduled')
    return res.scheduled ?? []
  }

  // cancelScheduled cancels one scheduled message by id (removing it from the
  // Scheduled folder and the send queue).
  async cancelScheduled(id: string): Promise<void> {
    await this.post('/scheduled/cancel', { id })
  }

  // saveDraft stores a draft in the Drafts folder, replacing the existing draft
  // when an id is supplied. Returns the (possibly new) draft id.
  async saveDraft(draft: { id?: string; to: string[]; cc?: string[]; bcc?: string[]; subject: string; body: string; from?: string }): Promise<{ id: string }> {
    return this.post<{ id: string }>('/mail/draft', draft)
  }

  async deleteMail(id: string, owner?: string): Promise<void> {
    await this.delete(`/mail/delete?id=${encodeURIComponent(id)}${ownerQuery(owner ?? this.mailboxOwner, '&')}`)
  }

  // recallMail attempts to unsend a message the caller authored: each local
  // recipient whose copy is still unread has it removed; read copies and
  // external recipients are reported back as not recalled.
  async recallMail(id: string): Promise<RecallResult> {
    return this.post<RecallResult>(`/mail/recall?id=${encodeURIComponent(id)}`, {})
  }

  // recoverMail restores a soft-deleted message from the Recoverable Items
  // dumpster back to the folder it was deleted from; the response carries the
  // destination folder it was restored to.
  async recoverMail(id: string, owner?: string): Promise<{ folder: string }> {
    return this.post<{ folder: string }>(
      `/mail/recover?id=${encodeURIComponent(id)}${ownerQuery(owner ?? this.mailboxOwner, '&')}`,
      {},
    )
  }

  // setFlag sets or clears an IMAP flag (\\Seen for read, \\Flagged for star)
  // on a message so the state persists server-side. owner targets a shared
  // mailbox (it rides the query string, which the handler reads for access).
  async setFlag(id: string, flag: '\\Seen' | '\\Flagged', value: boolean, owner?: string): Promise<void> {
    await this.post(`/mail/flag${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { id, flag, value })
  }

  // setFollowup sets a message's follow-up flag: a coloured flag with an optional
  // due date ('flag'), marking it complete ('complete'), or clearing it ('clear').
  // The richer follow-up the old webmail exposed beyond the plain \Flagged star.
  async setFollowup(
    id: string,
    action: 'flag' | 'complete' | 'clear',
    color?: number,
    due?: string,
    owner?: string,
  ): Promise<void> {
    await this.post(`/mail/followup${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { id, action, color, due })
  }

  // moveMail moves a message to another folder (e.g. "inbox" to restore from
  // Trash, or "archive" to archive).
  async moveMail(id: string, to: string, owner?: string): Promise<void> {
    await this.post(`/mail/move${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { id, to })
  }

  // copyMail copies a message into another folder, leaving the original in place.
  // The target may be a built-in slug or a custom folder's display name.
  async copyMail(id: string, to: string, owner?: string): Promise<void> {
    await this.post(`/mail/copy${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { id, to })
  }

  // listRecoverable returns a folder's Recoverable Items dumpster: messages
  // soft-deleted from it that are still recoverable.
  async listRecoverable(folder: string, owner?: string): Promise<{ folder: string; items: RecoverableItem[] }> {
    return this.get<{ folder: string; items: RecoverableItem[] }>(
      `/mail/recoverable?folder=${encodeURIComponent(folder)}${ownerQuery(owner ?? this.mailboxOwner, '&')}`,
    )
  }

  // recoverFromDumpster restores a soft-deleted message back into its folder.
  async recoverFromDumpster(folder: string, id: string, owner?: string): Promise<void> {
    await this.post(`/mail/recoverable/recover${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { folder, id })
  }

  // purgeFromDumpster permanently removes a soft-deleted message from the dumpster.
  async purgeFromDumpster(folder: string, id: string, owner?: string): Promise<void> {
    await this.post(`/mail/recoverable/purge${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { folder, id })
  }

  // exportSelected downloads the given messages as one messages.zip of .eml files
  // via a same-origin anchor (the session cookie authenticates; the server names
  // and streams the file). ids are "<folder>:<uid>" message ids; the server caps
  // the count, so the caller should warn when the selection exceeds the cap.
  exportSelected(ids: string[], owner?: string): void {
    if (ids.length === 0) return
    const params = ids.map((id) => `id=${encodeURIComponent(id)}`).join('&')
    const a = document.createElement('a')
    a.href = `${API_URL}/mail/export-zip?${params}${ownerQuery(owner ?? this.mailboxOwner, '&')}`
    a.download = 'messages.zip'
    document.body.appendChild(a)
    a.click()
    a.remove()
  }

  // markAllRead marks every unread message in a folder as read in one call.
  async markAllRead(folder: string, owner?: string): Promise<{ marked?: number }> {
    return this.post<{ marked?: number }>(`/mail/mark-all-read${ownerQuery(owner ?? this.mailboxOwner, '?')}`, { folder })
  }

  // importEml stores a base64-encoded .eml into a folder of the caller's own
  // mailbox (default Inbox).
  async importEml(file: string, folder = 'inbox'): Promise<{ uid?: number; folder?: string }> {
    return this.post<{ uid?: number; folder?: string }>('/mail/import', { file, folder })
  }

  // Filters
  async getFilters(): Promise<{ filters?: Filter[] }> {
    return this.get<{ filters?: Filter[] }>('/filters')
  }

  async createFilter(filter: FilterInput): Promise<Filter> {
    return this.post<Filter>('/filters', filter)
  }

  async updateFilter(id: string, filter: Partial<FilterInput>): Promise<Filter> {
    return this.put<Filter>(`/filters/${id}`, filter)
  }

  async deleteFilter(id: string): Promise<void> {
    await this.delete(`/filters/${id}`)
  }

  // reorderFilters sets the filter priority order (first id = highest priority).
  async reorderFilters(filterIds: string[]): Promise<void> {
    await this.post('/filters/reorder', { filterIds })
  }

  // runFilters applies the inbox filters to the mail already in the inbox now
  // (the "run now" sweep), returning how many messages were examined and acted on.
  async runFilters(): Promise<{ affected: number; evaluated: number }> {
    return this.post<{ affected: number; evaluated: number }>('/filters/run', {})
  }

  // exportRules downloads the user's filters as an Outlook .rwz file and triggers
  // a browser save. Returns the X-Rwz-Skipped summary header (or null) so the
  // caller can warn about rules/elements that could not be represented.
  async exportRules(): Promise<string | null> {
    const headers: Record<string, string> = {}
    if (this.token) headers['Authorization'] = `Bearer ${this.token}`
    const res = await fetch(`${API_URL}/filters/export`, { headers, credentials: 'include' })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const skipped = res.headers.get('X-Rwz-Skipped')
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'rules.rwz'
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
    return skipped
  }

  // importRules uploads an Outlook .rwz file and creates filters from it. The
  // file is sent base64-encoded in a JSON body (matching the avatar upload
  // contract) so it passes the API CSRF guard, which requires application/json.
  async importRules(file: File): Promise<RwzImportResult> {
    const dataUrl = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = () => reject(new Error('failed to read file'))
      reader.readAsDataURL(file)
    })
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (this.token) headers['Authorization'] = `Bearer ${this.token}`
    const res = await fetch(`${API_URL}/filters/import`, {
      method: 'POST',
      headers,
      credentials: 'include',
      body: JSON.stringify({ data: dataUrl }),
    })
    if (!res.ok) {
      let msg = `HTTP ${res.status}`
      try {
        const body = await res.json()
        if (body?.error) msg = body.error
      } catch {
        // non-JSON error body; keep the status message
      }
      throw new Error(msg)
    }
    return res.json()
  }

  // Vacation/Auto-reply
  async getVacation(): Promise<VacationAutoReply> {
    return this.get<VacationAutoReply>('/vacation')
  }

  async setVacation(vacation: VacationAutoReply): Promise<void> {
    await this.put('/vacation', vacation)
  }

  async deleteVacation(): Promise<void> {
    await this.delete('/vacation')
  }

  // Search
  async search(query: string): Promise<SearchResponse> {
    return this.get<SearchResponse>(`/search?q=${encodeURIComponent(query)}`)
  }

  // Threads. The inbox is grouped into conversations server-side; an optional
  // owner targets a shared mailbox the caller has access to.
  async getThreads(owner?: string): Promise<ThreadsResponse> {
    return this.get<ThreadsResponse>(`/threads${ownerQuery(owner ?? this.mailboxOwner, '?')}`)
  }

  async getThread(id: string): Promise<{ thread?: Thread }> {
    return this.get<{ thread?: Thread }>(`/threads/${id}`)
  }

  // Push notifications
  async getVapidPublicKey(): Promise<{ key?: string }> {
    return this.get<{ key?: string }>('/push/vapid-public-key')
  }

  async subscribePush(subscription: PushSubscription): Promise<void> {
    await this.post('/push/subscribe', subscription)
  }

  async unsubscribePush(endpoint: string): Promise<void> {
    await this.delete(`/push/unsubscribe?endpoint=${encodeURIComponent(endpoint)}`)
  }

  async getPushSubscriptions(): Promise<{ subscriptions?: PushSubscription[] }> {
    return this.get<{ subscriptions?: PushSubscription[] }>('/push/subscriptions')
  }

  // Shared mailboxes
  async getSharedMailboxes(): Promise<{ shared_mailboxes?: SharedMailbox[] }> {
    return this.get<{ shared_mailboxes?: SharedMailbox[] }>('/mailboxes/shared')
  }

  async getSharedAsOwner(): Promise<{ shared_as_owner?: string[] }> {
    return this.get<{ shared_as_owner?: string[] }>('/mailboxes/shared-as-owner')
  }

  // Folder ACL (RFC 4314) — owner/mailbox are canonical names (INBOX, custom-folder, etc.)
  async getACL(owner: string, mailbox: string): Promise<{ owner: string; mailbox: string; acl: ACLEntry[] }> {
    return this.get(`/mailboxes/${encodeURIComponent(owner)}/${encodeURIComponent(mailbox)}/acl`)
  }

  async setACL(owner: string, mailbox: string, grantee: string, rights: number): Promise<{ success: boolean }> {
    return this.post(`/mailboxes/${encodeURIComponent(owner)}/${encodeURIComponent(mailbox)}/acl`, { grantee, rights })
  }

  async deleteACL(owner: string, mailbox: string, grantee: string): Promise<{ success: boolean }> {
    return this.delete(`/mailboxes/${encodeURIComponent(owner)}/${encodeURIComponent(mailbox)}/acl/${encodeURIComponent(grantee)}`)
  }

  // Sender identities for compose
  async getSenderIdentities(personalEmail: string): Promise<SenderIdentity[]> {
    const [sharedResult] = await Promise.all([
      this.getSharedMailboxes()
    ])

    const identities: SenderIdentity[] = []

    // Add personal identity (user's own mailbox)
    identities.push({
      email: personalEmail,
      displayName: personalEmail,
      type: 'personal',
      canSend: true
    })

    // Add identities from shared mailboxes
    if (sharedResult.shared_mailboxes) {
      for (const mb of sharedResult.shared_mailboxes) {
        // User has access to this shared mailbox
        // They can send on behalf of the owner if they have write rights
        identities.push({
          email: mb.owner,
          displayName: `${mb.mailbox} (${mb.owner})`,
          type: 'send-on-behalf',
          mailboxOwner: mb.owner,
          canSend: true // Permission will be validated server-side on send
        })
      }
    }

    return identities
  }

  // Diagnostics
  async getDiagnostics(): Promise<{ errors?: DiagnosticEntry[] }> {
    return this.get<{ errors?: DiagnosticEntry[] }>('/mail/diagnostics')
  }

  async getMailboxDiagnostics(mailbox: string): Promise<{ errors?: DiagnosticEntry[] }> {
    return this.get<{ errors?: DiagnosticEntry[] }>(`/mail/diagnostics?mailbox=${encodeURIComponent(mailbox)}`)
  }

  // Contacts (CardDAV-backed)
  async getContacts(): Promise<{ contacts?: Contact[]; total?: number }> {
    return this.get<{ contacts?: Contact[]; total?: number }>('/contacts')
  }

  async createContact(contact: { name: string; email: string; phone?: string; company?: string; is_group?: boolean; members?: string[] }): Promise<{ contact?: Contact; status?: string }> {
    return this.post<{ contact?: Contact; status?: string }>('/contacts', contact)
  }

  async updateContact(id: string, contact: { name: string; email: string; phone?: string; company?: string; is_group?: boolean; members?: string[] }): Promise<{ contact?: Contact; status?: string }> {
    return this.put<{ contact?: Contact; status?: string }>(`/contacts/${id}`, contact)
  }

  async deleteContact(id: string): Promise<void> {
    await this.delete(`/contacts/${id}`)
  }

  // Calendar (CalDAV-backed)
  async getCalendarEvents(): Promise<{ events?: CalendarEvent[] }> {
    return this.get<{ events?: CalendarEvent[] }>('/calendar/events')
  }

  async createCalendarEvent(event: CalendarEventInput): Promise<CalendarEvent> {
    return this.post<CalendarEvent>('/calendar/events', event)
  }

  async updateCalendarEvent(uid: string, event: CalendarEventInput): Promise<CalendarEvent> {
    return this.put<CalendarEvent>(`/calendar/events/${encodeURIComponent(uid)}`, event)
  }

  async deleteCalendarEvent(uid: string): Promise<void> {
    await this.delete(`/calendar/events/${encodeURIComponent(uid)}`)
  }

  // Calendar management (multi-calendar)
  async getCalendars(): Promise<{ calendars?: Calendar[] }> {
    return this.get<{ calendars?: Calendar[] }>('/calendar/calendars')
  }

  async createCalendar(cal: CalendarInput): Promise<Calendar> {
    return this.post<Calendar>('/calendar/calendars', cal)
  }

  async updateCalendar(id: string, cal: Partial<CalendarInput>): Promise<Calendar> {
    return this.patch<Calendar>(`/calendar/calendars/${encodeURIComponent(id)}`, cal)
  }

  async deleteCalendar(id: string): Promise<void> {
    await this.delete(`/calendar/calendars/${encodeURIComponent(id)}`)
  }

  // getRooms lists the organization's bookable rooms for the room picker.
  async getRooms(): Promise<{ rooms?: Room[] }> {
    return this.get<{ rooms?: Room[] }>('/rooms')
  }

  // getFreeBusy returns busy intervals for the given users within a window,
  // computed from their real calendar events (no event details are exposed).
  async getFreeBusy(
    users: string[],
    start: string,
    end: string
  ): Promise<{ freeBusy?: UserFreeBusy[] }> {
    const params = new URLSearchParams({ users: users.join(','), start, end })
    return this.get<{ freeBusy?: UserFreeBusy[] }>(`/calendar/freebusy?${params.toString()}`)
  }

  // Tasks (CalDAV VTODO-backed)
  async getTasks(): Promise<{ tasks?: Task[] }> {
    return this.get<{ tasks?: Task[] }>('/tasks')
  }

  async createTask(task: TaskInput): Promise<Task> {
    return this.post<Task>('/tasks', task)
  }

  async updateTask(uid: string, task: TaskInput): Promise<Task> {
    return this.put<Task>(`/tasks/${encodeURIComponent(uid)}`, task)
  }

  async deleteTask(uid: string): Promise<void> {
    await this.delete(`/tasks/${encodeURIComponent(uid)}`)
  }

  // Notes (Outlook IPM.StickyNote, shared with EWS/IMAP/JMAP via the Notes folder)
  async getNotes(): Promise<{ notes?: Note[] }> {
    return this.get<{ notes?: Note[] }>('/notes')
  }

  async createNote(note: NoteInput): Promise<Note> {
    return this.post<Note>('/notes', note)
  }

  async updateNote(id: string, note: NoteInput): Promise<Note> {
    return this.put<Note>(`/notes/${encodeURIComponent(id)}`, note)
  }

  async deleteNote(id: string): Promise<void> {
    await this.delete(`/notes/${encodeURIComponent(id)}`)
  }

  // S/MIME certificate management (backed by /api/v1/smime/certificate)
  async getSMIMECertificate(): Promise<{ hasKeys: false } | SMIMECertInfo> {
    return this.get<{ hasKeys: false } | SMIMECertInfo>('/smime/certificate')
  }

  /** Browser mode: publishes the user's PUBLIC certificate (PEM). The key stays in the browser. */
  async uploadSMIMECertificate(cert: string): Promise<SMIMECertInfo> {
    return this.post<SMIMECertInfo>('/smime/certificate', { mode: 'browser', cert })
  }

  /** Server mode: uploads the .p12 (base64) and its password; the server stores the key at rest. */
  async uploadServerSMIME(p12: string, passphrase: string): Promise<SMIMECertInfo> {
    return this.post<SMIMECertInfo>('/smime/certificate', { mode: 'server', p12, passphrase })
  }

  /** Builds the outgoing MIME server-side and returns it unsent (base64) for client-side S/MIME. */
  async buildMail(mail: SendMailRequest): Promise<{ raw: string }> {
    return this.post<{ raw: string }>('/mail/build', mail)
  }

  /** Relays a client-built (signed/encrypted) raw message; recipients are supplied separately. */
  async sendRawMail(raw: string, to: string[], cc: string[], bcc: string[]): Promise<void> {
    await this.post('/mail/send-raw', { raw, to, cc, bcc })
  }

  /** Fetches a recipient's published S/MIME public certificate (PEM) for encryption, or null. */
  async getRecipientCertificate(address: string): Promise<{ cert: string } | null> {
    return this.get<{ cert: string } | null>(`/smime/recipient?address=${encodeURIComponent(address)}`)
  }

  /** Fetches a message's raw RFC822 bytes (used to decrypt S/MIME client-side). */
  async getMessageRaw(id: string): Promise<string> {
    const headers: Record<string, string> = {}
    if (this.token) headers['Authorization'] = `Bearer ${this.token}`
    const res = await fetch(`${API_URL}/mail/source?id=${encodeURIComponent(id)}`, { headers, credentials: 'include' })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.text()
  }

  async deleteSMIMECertificate(): Promise<{ status: string }> {
    return this.delete<{ status: string }>('/smime/certificate')
  }

  // Generic methods
  get<T = ApiResponse>(endpoint: string): Promise<T> {
    return this.request<T>(endpoint, { method: 'GET' })
  }

  post<T = unknown>(endpoint: string, data?: unknown): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'POST',
      body: data ? JSON.stringify(data) : undefined
    })
  }

  put<T = unknown>(endpoint: string, data?: unknown): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'PUT',
      body: data ? JSON.stringify(data) : undefined
    })
  }

  patch<T = unknown>(endpoint: string, data?: unknown): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'PATCH',
      body: data ? JSON.stringify(data) : undefined
    })
  }

  delete<T = ApiResponse>(endpoint: string): Promise<T> {
    return this.request<T>(endpoint, { method: 'DELETE' })
  }
}

export default new API()
