import { useState, useRef, useEffect, useCallback, type ReactNode } from "react"
import { useNavigate, useSearchParams } from "react-router-dom"
import {
  ArrowLeft,
  Send,
  Save,
  Paperclip,
  CalendarClock,
  ListTodo,
  StickyNote,
  X,
  Plus,
  Bold,
  Italic,
  Underline,
  Link,
  List,
  Image,
  Minimize2,
  Maximize2,
  Clock,
  Check,
  AlertTriangle,
  Mail,
  ChevronDown,
  Shield,
  Key,
  SlidersHorizontal,
  Contact as ContactIcon,
  type LucideIcon,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Textarea } from "@/components/ui/textarea"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import api, { SenderIdentity, DiagnosticEntry, Contact as ContactType, MailAttachment, SignatureEntry, TemplateEntry, Mail as MailMessage, CalendarEvent, Task, Note } from "@/utils/api"
import { singleFlight } from "@/utils/singleFlight"
import { draftSession } from "@/utils/draftSession"
import { DRAG_TYPE, fileFromDrag } from "@/utils/attachmentDrag"
import { taskToVTodo, noteToText, safeItemName } from "@/utils/attachItem"
import * as smimeStore from "@/utils/smime"
import { hasIdentity as hasBrowserSmime } from "@/utils/smimeIdentity"
import { mailOptionsActive } from "@/utils/mailOptions"
import { draftRecipients, prefillFromDraft, prefillFromParams } from "@/utils/composePrefill"
import {
  addressList,
  dedupeRecipients,
  defaultSender,
  FORMAT_PLACEHOLDER_KEYS,
  formatSize,
  formatText,
  isAddress,
  mentionsAttachment,
  namesCheckedOutcome,
  safeFileBase,
  scheduledInstant,
  sendBlock,
  smimeBlock,
  typedAddress,
  typedRecipient,
  uniqueAddresses,
  withAutoCc,
  withRecipient,
  withSignature,
  withTemplate,
  type FormatKind,
  type Recipient,
  type RecipientField,
} from "@/utils/composeForm"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"
import { withTz, zonedInputToISO } from "@/utils/date"
import { RichTextEditor } from "@/components/RichTextEditor"

type TFunc = ReturnType<typeof useI18n>["t"]

interface Attachment {
  id: string
  name: string
  size: number
  file?: File
}

type RichTextHandle = { getHTML: () => string; setHTML: (html: string) => void }

// fileToBase64 reads a File into a base64 string (without the data URL prefix)
// for transport in the JSON send/draft payloads.
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = typeof reader.result === "string" ? reader.result : ""
      resolve(result.includes(",") ? result.split(",")[1] : result)
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

// encodeAttachments turns the attached files into the send payload's entries.
async function encodeAttachments(list: Attachment[]): Promise<MailAttachment[]> {
  const encoded = await Promise.all(
    list.map(async (a): Promise<MailAttachment | null> =>
      a.file ? { filename: a.name, contentType: a.file.type || "application/octet-stream", content: await fileToBase64(a.file) } : null
    )
  )
  return encoded.filter((x): x is MailAttachment => x !== null)
}

// newAttachment wraps a file for the attachment list.
function newAttachment(file: File): Attachment {
  return { id: crypto.randomUUID(), name: file.name, size: file.size, file }
}

// dragCarries reports whether a drag holds the given data type.
function dragCarries(e: DragEvent, type: string): boolean {
  return Array.from(e.dataTransfer?.types ?? []).includes(type)
}

// resolveRecipient looks up a recipient that still shows a bare address in the
// directory and attaches the resolved name. It reports whether it resolved.
async function resolveRecipient(r: Recipient): Promise<{ recipient: Recipient; resolved: boolean }> {
  if (r.name && r.name !== r.email) return { recipient: r, resolved: true }
  try {
    const res = await api.searchDirectory(r.email)
    const match = (res.entries ?? []).find((e) => e.email.toLowerCase() === r.email.toLowerCase())
    if (match?.name && match.name !== match.email) return { recipient: { ...r, name: match.name }, resolved: true }
  } catch {
    // A lookup failure leaves the address as typed.
  }
  return { recipient: r, resolved: false }
}

// resolveRecipients resolves a whole field and counts the addresses that did not
// resolve.
async function resolveRecipients(list: Recipient[]): Promise<{ resolved: Recipient[]; unresolved: number }> {
  const results = await Promise.all(list.map(resolveRecipient))
  return { resolved: results.map((x) => x.recipient), unresolved: results.filter((x) => !x.resolved).length }
}

// recipientCerts fetches every recipient's S/MIME certificate for encryption;
// a recipient without one fails the send and is named.
async function recipientCerts(t: TFunc, addresses: string[]): Promise<string[]> {
  const certs: string[] = []
  for (const addr of uniqueAddresses(addresses)) {
    const rc = await api.getRecipientCertificate(addr)
    if (!rc?.cert) throw new Error(t("compose.smimeNoRecipientCert", { addr }))
    certs.push(rc.cert)
  }
  return certs
}

// useSenderIdentities loads the identities the user may send as and picks the
// one a new message starts from.
function useSenderIdentities() {
  const { user } = useAuth()
  const { currentMailbox, isInSharedMailbox } = useMailbox()
  const [identities, setIdentities] = useState<SenderIdentity[]>([])
  const [selected, setSelected] = useState<SenderIdentity | null>(null)

  useEffect(() => {
    const sharedOwner = isInSharedMailbox() && currentMailbox.owner ? currentMailbox.owner : null
    api.getSenderIdentities(user?.email || "")
      .then((list) => {
        setIdentities(list)
        const pick = defaultSender(list, sharedOwner)
        if (pick) setSelected(pick)
      })
      .catch((err) => {
        console.error("Failed to load sender identities:", err)
        // Fall back to the personal identity.
        if (user?.email) setSelected({ email: user.email, displayName: user.email, type: "personal", canSend: true })
      })
  }, [user, currentMailbox, isInSharedMailbox])

  return { identities, selected, setSelected }
}

type Senders = ReturnType<typeof useSenderIdentities>

// useDiagnostics loads the mailbox diagnostics shown beside the sender.
function useDiagnostics() {
  const [diagnostics, setDiagnostics] = useState<DiagnosticEntry[]>([])
  const [show, setShow] = useState(false)
  useEffect(() => {
    api.getDiagnostics()
      .then((result) => { if (result.errors) setDiagnostics(result.errors) })
      .catch((err) => console.error("Failed to load diagnostics:", err))
  }, [])
  return { diagnostics, show, setShow }
}

type Diagnostics = ReturnType<typeof useDiagnostics>

// useDirectorySuggestions merges the personal contacts matching the query with
// the organization directory (GAL), fetched as the user types and deduped by
// address below the contacts.
function useDirectorySuggestions(contacts: Recipient[], query: string) {
  const [directory, setDirectory] = useState<Recipient[]>([])
  useEffect(() => {
    const q = query.trim()
    if (q.length < 2) {
      setDirectory([])
      return
    }
    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const res = await api.searchDirectory(q)
        if (!cancelled) setDirectory((res.entries ?? []).map((e) => ({ id: `gal-${e.email}`, name: e.name || e.email, email: e.email })))
      } catch {
        if (!cancelled) setDirectory([])
      }
    }, 200)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [query])

  const low = query.toLowerCase()
  const matching = contacts.filter((c) => c.name.toLowerCase().includes(low) || c.email.toLowerCase().includes(low))
  const known = new Set(contacts.map((c) => c.email.toLowerCase()))
  return [...matching, ...directory.filter((d) => !known.has(d.email.toLowerCase()))]
}

// useContacts loads the personal contacts offered as recipients.
function useContacts() {
  const [contacts, setContacts] = useState<Recipient[]>([])
  useEffect(() => {
    api.getContacts()
      .then((result) => {
        if (result.contacts) setContacts(result.contacts.map((c: ContactType) => ({ id: c.id, name: c.name, email: c.email })))
      })
      .catch((err) => console.error("Failed to load contacts:", err))
  }, [])
  return contacts
}

// useRecipients holds the To, Cc and Bcc fields: the chips, the typed text, the
// suggestions and the check-names pass.
function useRecipients(searchParams: URLSearchParams) {
  const { t } = useI18n()
  const contacts = useContacts()
  const [lists, setLists] = useState<Record<RecipientField, Recipient[]>>({ to: [], cc: [], bcc: [] })
  const [input, setInput] = useState<Record<RecipientField, string>>({ to: "", cc: "", bcc: "" })
  const [activeField, setActiveField] = useState<RecipientField | null>(null)
  const [showCc, setShowCc] = useState(false)
  const [showBcc, setShowBcc] = useState(false)
  const [searchQuery, setSearchQuery] = useState("")
  const [checking, setChecking] = useState(false)
  const suggestions = useDirectorySuggestions(contacts, searchQuery)

  const setField = useCallback((field: RecipientField, list: Recipient[]) => setLists((prev) => ({ ...prev, [field]: list })), [])

  // Handle replyTo/cc params after contacts are loaded. Both accept a
  // comma-separated list so Reply All can prefill multiple recipients.
  useEffect(() => {
    const toRecipient = (email: string, idx: number): Recipient =>
      contacts.find((c) => c.email === email) ?? { id: `param-${idx}-${email}`, name: email, email }
    const replyTo = addressList(searchParams.get("replyTo"))
    if (replyTo.length > 0) setField("to", replyTo.map(toRecipient))
    const ccList = addressList(searchParams.get("cc"))
    if (ccList.length > 0) {
      setField("cc", ccList.map(toRecipient))
      setShowCc(true)
    }
  }, [searchParams, contacts, setField])

  // Auto-Cc: append the configured addresses (settings.appearance.autoCc) to the
  // Cc list once on mount, so every outgoing mail copies them without the user
  // re-adding them. A reply-all's explicit Cc is not dropped.
  useEffect(() => {
    api.getAppearanceSettings()
      .then((s) => {
        if (!(s.autoCc ?? "").trim()) return
        setLists((prev) => ({ ...prev, cc: withAutoCc(prev.cc, s.autoCc) }))
        setShowCc(true)
      })
      .catch(() => undefined)
  }, [])

  const add = (contact: Recipient, field: RecipientField) => {
    setLists((prev) => ({ ...prev, [field]: withRecipient(prev[field], contact) }))
    setSearchQuery("")
  }

  const remove = (id: string, field: RecipientField) =>
    setLists((prev) => ({ ...prev, [field]: prev[field].filter((r) => r.id !== id) }))

  // pick adds a suggestion picked from the inline list under a field, then clears
  // that field's typed text.
  const pick = (contact: Recipient, field: RecipientField) => {
    add(contact, field)
    setInput((p) => ({ ...p, [field]: "" }))
    setActiveField(null)
  }

  const type = (field: RecipientField, value: string) => {
    setInput((p) => ({ ...p, [field]: value }))
    setSearchQuery(value)
    setActiveField(field)
  }

  // commitTyped adds the free-text address typed into a field, validating its
  // basic shape so users can email anyone, not just contacts.
  const commitTyped = (field: RecipientField) => {
    const email = typedAddress(input[field])
    setInput((prev) => ({ ...prev, [field]: "" }))
    if (!email) return
    if (!isAddress(email)) {
      toast.error(t("compose.invalidEmail"))
      return
    }
    add(typedRecipient(field, email), field)
  }

  // pendingTyped is the address still typed into a field, when it is one.
  const pendingTyped = (field: RecipientField): Recipient[] => {
    const email = typedAddress(input[field])
    return email && isAddress(email) ? [typedRecipient(field, email)] : []
  }

  // checkNames is the explicit "resolve all" action: it commits any half-typed
  // address, resolves every recipient against the directory in one pass and
  // reports how many addresses could not be matched (external or mistyped).
  const checkNames = async () => {
    setChecking(true)
    try {
      const fields: RecipientField[] = ["to", "cc", "bcc"]
      const pending = fields.map((f) => dedupeRecipients([...lists[f], ...pendingTyped(f)]))
      setInput({ to: "", cc: "", bcc: "" })
      const results = await Promise.all(pending.map(resolveRecipients))
      setLists({ to: results[0].resolved, cc: results[1].resolved, bcc: results[2].resolved })
      const total = pending.reduce((n, l) => n + l.length, 0)
      const outcome = namesCheckedOutcome(total, results.reduce((n, r) => n + r.unresolved, 0))
      toast[outcome.level](t(outcome.key, outcome.params))
    } finally {
      setChecking(false)
    }
  }

  return {
    lists, setField, input, activeField, setActiveField, showCc, setShowCc, showBcc, setShowBcc,
    searchQuery, setSearchQuery, suggestions, checking, add, remove, pick, type, commitTyped, checkNames,
  }
}

type Recipients = ReturnType<typeof useRecipients>

// useSignaturesAndTemplates loads the outgoing signatures, the message templates
// and the rich-text preference, and applies the picked signature or template to
// a new message once. Editing an existing draft applies neither.
function useSignaturesAndTemplates(searchParams: URLSearchParams, setSubject: (s: string) => void, setBody: (f: (prev: string) => string) => void) {
  const [signatures, setSignatures] = useState<SignatureEntry[]>([])
  const [signature, setSignature] = useState<SignatureEntry | null>(null)
  const [templates, setTemplates] = useState<TemplateEntry[]>([])
  const [template, setTemplate] = useState<TemplateEntry | null>(null)
  const [richTextMode, setRichTextMode] = useState(false)
  const signatureApplied = useRef(false)
  const templateApplied = useRef(false)

  useEffect(() => {
    let cancelled = false
    api.getSignatures()
      .then((res) => {
        if (cancelled) return
        const list = res.signatures ?? []
        setSignatures(list)
        if (list.length > 0) setSignature(list.find((s) => s.name === "default") ?? list[0])
      })
      .catch(() => undefined) // no signatures configured
    api.getTemplates()
      .then((res) => { if (!cancelled) setTemplates(res.templates ?? []) })
      .catch(() => undefined)
    api.getPreferences()
      .then((res) => { if (!cancelled && res.preferences) setRichTextMode(res.preferences.richTextMode ?? false) })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (signatureApplied.current || !signature || searchParams.get("draft")) return
    signatureApplied.current = true
    setBody((prev) => withSignature(prev, signature))
  }, [signature, searchParams, setBody])

  useEffect(() => {
    if (templateApplied.current || !template || searchParams.get("draft")) return
    templateApplied.current = true
    if (template.subject) setSubject(template.subject)
    setBody((prev) => withTemplate(prev, template))
  }, [template, searchParams, setSubject, setBody])

  return { signatures, signature, setSignature, templates, template, setTemplate, richTextMode }
}

type SignaturesAndTemplates = ReturnType<typeof useSignaturesAndTemplates>

// useMessageContent holds the subject and body, prefilled from the reply or
// forward parameters, and reopens a stored draft named by ?draft=<id>.
function useMessageContent(searchParams: URLSearchParams, recipients: Recipients) {
  const [subject, setSubject] = useState("")
  const [body, setBody] = useState("")
  const [draftId, setDraftId] = useState<string | null>(null)
  const { setField, setShowCc, setShowBcc } = recipients

  // Prefill subject/body from query params (used by reply and forward). The
  // sanitization the body needs lives in prefillFromParams, where the tests hold
  // it.
  useEffect(() => {
    const prefill = prefillFromParams(searchParams)
    if (prefill.subject) setSubject(prefill.subject)
    if (prefill.body) setBody(prefill.body)
  }, [searchParams])

  useEffect(() => {
    const draftParam = searchParams.get("draft")
    if (!draftParam) return
    let active = true
    api.getMessage(draftParam)
      .then((msg) => {
        if (!active || !msg) return
        setDraftId(msg.id || draftParam)
        setSubject(msg.subject || "")
        setBody(prefillFromDraft(msg.body))
        const toList = draftRecipients(msg.to, "to")
        const ccList = draftRecipients(msg.cc, "cc")
        const bccList = draftRecipients(msg.bcc, "bcc")
        if (toList.length > 0) setField("to", toList)
        if (ccList.length > 0) { setField("cc", ccList); setShowCc(true) }
        if (bccList.length > 0) { setField("bcc", bccList); setShowBcc(true) }
      })
      .catch((err) => console.error("Failed to load draft:", err))
    return () => {
      active = false
    }
  }, [searchParams, setField, setShowCc, setShowBcc])

  return { subject, setSubject, body, setBody, draftId, setDraftId }
}

type MessageContent = ReturnType<typeof useMessageContent>

// useAttachments holds the attached files. A file dropped anywhere on the
// composer is attached: the listeners sit on the WINDOW because a drop that lands
// on a plain element, the subject field or the page background reaches no React
// handler, and the browser then navigates to the dropped file, which discards the
// message being written.
function useAttachments() {
  const { t } = useI18n()
  const [attachments, setAttachments] = useState<Attachment[]>([])

  const add = useCallback((file: File) => setAttachments((prev) => [...prev, newAttachment(file)]), [])

  // attachFiles is the one way a picked or dropped file becomes an attachment, so
  // the two cannot drift apart.
  const attachFiles = useCallback((files: File[]) => {
    if (files.length === 0) return
    setAttachments((prev) => [...prev, ...files.map(newAttachment)])
    const key = files.length > 1 ? "compose.filesAttached" : "compose.fileAttached"
    toast.success(t(key, { count: String(files.length) }))
  }, [t])

  useEffect(() => {
    const onDragOver = (e: DragEvent) => {
      if (dragCarries(e, "Files") || dragCarries(e, DRAG_TYPE)) e.preventDefault()
    }
    const onDrop = (e: DragEvent) => {
      // The editor handles its own drop, and calling preventDefault is how it
      // says so; attaching here as well would add the file twice.
      if (e.defaultPrevented) return
      const dragged = e.dataTransfer ? fileFromDrag(e.dataTransfer) : null
      if (!dragCarries(e, "Files") && !dragged) return
      e.preventDefault()
      // An attachment dragged out of a message carries its own payload: a drag
      // started inside the browser cannot populate dataTransfer.files.
      attachFiles(dragged ? [dragged] : Array.from(e.dataTransfer?.files ?? []))
    }
    window.addEventListener("dragover", onDragOver)
    window.addEventListener("drop", onDrop)
    return () => {
      window.removeEventListener("dragover", onDragOver)
      window.removeEventListener("drop", onDrop)
    }
  }, [attachFiles])

  const remove = (id: string) => setAttachments((prev) => prev.filter((a) => a.id !== id))

  return { attachments, add, attachFiles, remove }
}

type Attachments = ReturnType<typeof useAttachments>

type ItemKind = "event" | "task" | "note"

// ITEM_KIND_TITLE_KEYS is the i18n key each attach-item picker is titled with.
const ITEM_KIND_TITLE_KEYS: Record<ItemKind, string> = {
  event: "compose.attachAppointment",
  task: "compose.attachTask",
  note: "compose.attachNote",
}

// usePickers drives the three attach pickers: an existing message (embedded as
// message/rfc822), a contact (.vcf), and an appointment, task or note.
function usePickers(add: (file: File) => void) {
  const { t } = useI18n()
  const [msgOpen, setMsgOpen] = useState(false)
  const [msgFolder, setMsgFolder] = useState("inbox")
  const [msgList, setMsgList] = useState<MailMessage[]>([])
  const [msgLoading, setMsgLoading] = useState(false)
  const [contactOpen, setContactOpen] = useState(false)
  const [contactList, setContactList] = useState<ContactType[]>([])
  const [itemOpen, setItemOpen] = useState(false)
  const [itemKind, setItemKind] = useState<ItemKind>("event")
  const [itemLoading, setItemLoading] = useState(false)
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [notes, setNotes] = useState<Note[]>([])

  // attach adds a picked item and confirms, or reports the failure.
  const attach = async (make: () => Promise<File> | File, close: () => void, okKey: string, failKey = "compose.attachFailed") => {
    try {
      add(await make())
      close()
      toast.success(t(okKey))
    } catch {
      toast.error(t(failKey))
    }
  }

  const openMessages = async (folder: string) => {
    setMsgFolder(folder)
    setMsgOpen(true)
    setMsgLoading(true)
    try {
      setMsgList((await api.getMail(folder)).emails ?? [])
    } catch {
      setMsgList([])
    } finally {
      setMsgLoading(false)
    }
  }

  // attachMessage fetches the picked message's raw .eml and embeds it.
  const attachMessage = (msg: MailMessage) =>
    attach(async () => new File([await api.getMessageRaw(msg.id)], `${safeFileBase(msg.subject, "message")}.eml`, { type: "message/rfc822" }),
      () => setMsgOpen(false), "compose.messageAttached", "compose.messageAttachFailed")

  const openContacts = () => {
    setContactOpen(true)
    api.getContacts().then((res) => setContactList(res.contacts ?? [])).catch(() => undefined)
  }

  // attachContact fetches the picked contact's vCard and embeds it.
  const attachContact = (c: ContactType) =>
    attach(async () => {
      const res = await fetch(api.contactVCardUrl(c.id), { credentials: "include" })
      return new File([await res.text()], `${safeFileBase(c.name, "contact")}.vcf`, { type: "text/vcard" })
    }, () => setContactOpen(false), "compose.contactAttached")

  const loadItems = async (kind: ItemKind) => {
    if (kind === "event") setEvents((await api.getCalendarEvents()).events ?? [])
    else if (kind === "task") setTasks((await api.getTasks()).tasks ?? [])
    else setNotes((await api.getNotes()).notes ?? [])
  }

  const openItems = async (kind: ItemKind) => {
    setItemKind(kind)
    setItemOpen(true)
    setItemLoading(true)
    try {
      await loadItems(kind)
    } catch {
      setEvents([])
      setTasks([])
      setNotes([])
    } finally {
      setItemLoading(false)
    }
  }

  const closeItems = () => setItemOpen(false)

  // attachEvent embeds an appointment as the backend's .ics export (faithful to
  // VTIMEZONE and recurrence).
  const attachEvent = (ev: CalendarEvent) =>
    attach(async () => {
      const res = await fetch(`/api/v1/calendar/events/${encodeURIComponent(ev.uid)}/ics`, { credentials: "include" })
      if (!res.ok) throw new Error()
      return new File([await res.text()], `${safeItemName(ev.summary, "event")}.ics`, { type: "text/calendar" })
    }, closeItems, "compose.itemAttached")

  // attachTask embeds a task as a VTODO .ics document, serialized client-side.
  const attachTask = (tk: Task) =>
    attach(() => new File([taskToVTodo(tk)], `${safeItemName(tk.summary, "task")}.ics`, { type: "text/calendar" }), closeItems, "compose.itemAttached")

  // attachNote embeds a note as a plain-text .txt document.
  const attachNote = (nt: Note) =>
    attach(() => new File([noteToText(nt)], `${safeItemName(nt.title, "note")}.txt`, { type: "text/plain" }), closeItems, "compose.itemAttached")

  return {
    msg: { open: msgOpen, setOpen: setMsgOpen, folder: msgFolder, list: msgList, loading: msgLoading, openFolder: openMessages, attach: attachMessage },
    contact: { open: contactOpen, setOpen: setContactOpen, list: contactList, show: openContacts, attach: attachContact },
    item: { open: itemOpen, setOpen: setItemOpen, kind: itemKind, loading: itemLoading, events, tasks, notes, show: openItems, attachEvent, attachTask, attachNote },
  }
}

type Pickers = ReturnType<typeof usePickers>

// useMailOptions holds the per-message options: importance, sensitivity, the two
// receipts, and the S/MIME sign and encrypt toggles.
function useMailOptions() {
  const [open, setOpen] = useState(false)
  const [importance, setImportance] = useState<"low" | "normal" | "high">("normal")
  const [sensitivity, setSensitivity] = useState<"normal" | "personal" | "private" | "confidential">("normal")
  const [readReceipt, setReadReceipt] = useState(false)
  const [deliveryReceipt, setDeliveryReceipt] = useState(false)
  const [sign, setSign] = useState(false)
  const [encrypt, setEncrypt] = useState(false)
  return {
    open, setOpen, importance, setImportance, sensitivity, setSensitivity,
    readReceipt, setReadReceipt, deliveryReceipt, setDeliveryReceipt, sign, setSign, encrypt, setEncrypt,
  }
}

type MailOptions = ReturnType<typeof useMailOptions>

// hasDraftContent reports whether the composer holds anything worth a draft.
function hasDraftContent(content: MessageContent, lists: Recipients["lists"]): boolean {
  return !!(content.subject || content.body || lists.to.length > 0 || lists.cc.length > 0 || lists.bcc.length > 0)
}

// useDraftSaving saves the message as a draft, on request and silently every
// minute. Both paths share ONE gate: the draft id only exists after the first
// save answers, so two overlapping saves both post without one and the mailbox
// ends up with two drafts.
function useDraftSaving(content: MessageContent, recipients: Recipients, sender: SenderIdentity | null) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { user } = useAuth()
  const [saving, setSaving] = useState(false)
  // lastSaved is the time the draft was last stored, and only a draft save that
  // the server answered sets it.
  const [lastSaved, setLastSaved] = useState<Date | null>(null)
  // The session holds the draft id a save answered with, so the next save and a
  // send read it before React has re-rendered, and it stops saving once a send
  // begins.
  const session = useRef(draftSession(singleFlight(setSaving)))

  // store saves the draft and returns the id the server answered with. A draft
  // reopened from the Drafts folder is known only to the content state.
  const store = async (id: string | undefined): Promise<string | undefined> => {
    const { to, cc, bcc } = recipients.lists
    const res = await api.saveDraft({
      id: id ?? content.draftId ?? undefined,
      to: to.map((r) => r.email),
      cc: cc.map((r) => r.email),
      bcc: bcc.map((r) => r.email),
      subject: content.subject,
      body: content.body,
      from: sender?.email || user?.email || "",
    })
    if (res?.id) content.setDraftId(res.id)
    return res?.id
  }

  const save = async () => {
    if (!hasDraftContent(content, recipients.lists)) {
      toast.error(t("compose.nothingToSave"))
      return
    }
    await session.current.save(async (id) => {
      try {
        const saved = await store(id)
        toast.success(t("compose.draftSaved"))
        navigate("/drafts")
        return saved
      } catch (err) {
        console.error("Failed to save draft:", err)
        toast.error(t("compose.draftSaveFailed"))
        return undefined
      }
    })
  }

  // autoSave is the silent version (no navigate, no toast), fired every minute so
  // a browser crash never loses work.
  const autoSave = async () => {
    if (!hasDraftContent(content, recipients.lists)) return
    await session.current.save(async (id) => {
      try {
        const saved = await store(id)
        setLastSaved(new Date())
        return saved
      } catch {
        /* best-effort: a failed autosave must not interrupt composing */
        return undefined
      }
    })
  }

  // settle stops draft saving for a send, waits for a running save, and returns
  // the draft id the send must consume.
  const settle = async (): Promise<string | undefined> => (await session.current.settle()) ?? content.draftId ?? undefined
  // reopen lets saving resume after a send that failed, so the message is still
  // autosaved while the user fixes it.
  const reopen = () => session.current.reopen()

  // The interval is armed once and calls the autosave of the latest render. An
  // interval re-armed on every render restarts its minute on each keystroke, so
  // a user who kept typing was never autosaved at all.
  const autoSaveRef = useRef<() => void>(() => undefined)
  useEffect(() => {
    autoSaveRef.current = () => void autoSave()
  })
  useEffect(() => {
    const id = window.setInterval(() => autoSaveRef.current(), 60000)
    return () => window.clearInterval(id)
  }, [])

  return { saving, lastSaved, save, settle, reopen }
}

type DraftSaving = ReturnType<typeof useDraftSaving>

// ComposeState is everything a send reads.
interface ComposeState {
  content: MessageContent
  recipients: Recipients
  senders: Senders
  diagnostics: Diagnostics
  attachments: Attachment[]
  options: MailOptions
  richTextMode: boolean
  richTextRef: React.RefObject<RichTextHandle | null>
  scheduledAt: string
  drafts: Pick<DraftSaving, "settle" | "reopen">
}

// currentBody is the body as it will be sent: the editor's HTML in rich-text
// mode, otherwise the plain text.
function currentBody(s: ComposeState): string {
  return s.richTextMode && s.richTextRef.current ? s.richTextRef.current.getHTML() : s.content.body
}

// sendPayload builds the send request from the composer. draftId is the stored
// draft the send consumes.
async function sendPayload(s: ComposeState, senderEmail: string, sendAt: string | undefined, draftId: string | undefined) {
  const { lists } = s.recipients
  const encoded = await encodeAttachments(s.attachments)
  const o = s.options
  return {
    to: lists.to.map((r) => r.email),
    cc: lists.cc.map((r) => r.email),
    bcc: lists.bcc.map((r) => r.email),
    subject: s.content.subject,
    body: currentBody(s),
    from: senderEmail,
    attachments: encoded.length > 0 ? encoded : undefined,
    requestReadReceipt: o.readReceipt || undefined,
    requestDeliveryReceipt: o.deliveryReceipt || undefined,
    importance: o.importance !== "normal" ? o.importance : undefined,
    sensitivity: o.sensitivity !== "normal" ? o.sensitivity : undefined,
    sendAt,
    is_html: s.richTextMode,
    draftId,
  }
}

type SendPayload = Awaited<ReturnType<typeof sendPayload>>

// sendBrowserSmime builds the MIME server-side, signs and/or encrypts it in the
// browser (sign-then-encrypt) and relays the raw result. The key never leaves
// the browser.
async function sendBrowserSmime(t: TFunc, payload: SendPayload, o: MailOptions) {
  const { raw } = await api.buildMail(payload)
  let mime = atob(raw)
  if (o.sign) mime = smimeStore.signMime(mime)
  if (o.encrypt) {
    const certs = await recipientCerts(t, [...payload.to, ...payload.cc, ...payload.bcc, payload.from])
    mime = smimeStore.encryptMime(mime, certs)
  }
  await api.sendRawMail(btoa(mime), payload.to, payload.cc, payload.bcc, payload.draftId)
}

// transmit sends the payload by the path S/MIME needs: signed or encrypted in the
// browser, by the server that holds the key, or plain.
async function transmit(t: TFunc, payload: SendPayload, o: MailOptions, browserSmime: boolean) {
  if (browserSmime) return sendBrowserSmime(t, payload, o)
  if (o.sign || o.encrypt) return api.sendMail({ ...payload, signMessage: o.sign, encryptMessage: o.encrypt })
  return api.sendMail(payload)
}

// SEND_KEYS is what an immediate and a scheduled send report and where each
// lands afterwards.
const SEND_KEYS = {
  now: { start: "compose.sendingEmail", done: "compose.emailSent", failed: "compose.sendFailed", path: "/sent" },
  scheduled: { start: "compose.schedulingEmail", done: "compose.emailScheduled", failed: "compose.scheduleFailed", path: "/scheduled" },
}

// useSend sends the message. The in-flight gate lives outside React state: a
// second Ctrl+Enter or click can arrive before a re-render, and the Send button's
// disabled attribute does not cover the keyboard path at all.
function useSend(state: ComposeState) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { user } = useAuth()
  const [sending, setSending] = useState(false)
  const [reminderOpen, setReminderOpen] = useState(false)
  const gate = useRef(singleFlight(setSending))

  // draftBlocked reports, and explains, a message that cannot be sent as it is.
  const draftBlocked = (): boolean => {
    const sender = state.senders.selected
    const block = sendBlock({
      to: state.recipients.lists.to, subject: state.content.subject, canSend: sender?.canSend ?? true,
      senderEmail: sender?.email, diagnostics: state.diagnostics.diagnostics,
    })
    if (!block) return false
    if (block.showDiagnostics) state.diagnostics.setShow(true)
    toast.error(t(block.key, block.params))
    return true
  }

  // smimeReady reports whether the S/MIME options can run, and whether the key
  // is in this browser; it explains a refusal itself.
  const smimeReady = async (scheduled: boolean): Promise<{ ok: boolean; browserSmime: boolean }> => {
    const o = state.options
    const browserSmime = (o.sign || o.encrypt) && (await hasBrowserSmime())
    const block = smimeBlock({ sign: o.sign, encrypt: o.encrypt, scheduled, browserKey: browserSmime, unlocked: smimeStore.isUnlocked() })
    if (block) toast.error(t(block))
    return { ok: !block, browserSmime }
  }

  // wantsReminder opens the missing-attachment reminder when the text hints at an
  // attachment but none is attached.
  const wantsReminder = (skipReminder: boolean): boolean => {
    if (skipReminder || state.attachments.length > 0) return false
    if (!mentionsAttachment(state.content.subject, currentBody(state))) return false
    setReminderOpen(true)
    return true
  }

  // precheck returns the send-later instant, or false when the send must not
  // start; it reports the reason itself.
  const precheck = async (skipReminder: boolean): Promise<{ sendAt?: string; browserSmime: boolean } | false> => {
    if (draftBlocked()) return false
    const when = scheduledInstant(state.scheduledAt, zonedInputToISO, Date.now())
    if (when.error) {
      toast.error(t("compose.scheduleInPast"))
      return false
    }
    const smime = await smimeReady(!!when.iso)
    if (!smime.ok || wantsReminder(skipReminder)) return false
    return { sendAt: when.iso, browserSmime: smime.browserSmime }
  }

  const doSend = async (skipReminder: boolean) => {
    const ready = await precheck(skipReminder)
    if (!ready) return
    const keys = SEND_KEYS[ready.sendAt ? "scheduled" : "now"]
    toast.success(t(keys.start))
    const draftId = await state.drafts.settle()
    try {
      const payload = await sendPayload(state, state.senders.selected?.email || user?.email || "", ready.sendAt, draftId)
      await transmit(t, payload, state.options, ready.browserSmime)
      toast.success(t(keys.done))
      navigate(keys.path)
    } catch (err) {
      state.drafts.reopen()
      console.error("Failed to send email:", err)
      toast.error(err instanceof Error && err.message ? err.message : t(keys.failed))
    }
  }

  // send runs through the gate, so the Ctrl+Enter shortcut and the button cannot
  // both start a send.
  const send = (skipReminder = false) => {
    void gate.current.run(() => doSend(skipReminder))
  }

  // The shortcut calls the send of the LATEST render through a ref. A listener
  // bound to one render's send sends that render's recipients, attachments and
  // options, so a Cc added after the last edit of To, Subject or Body was dropped
  // from a Ctrl+Enter send.
  const shortcutRef = useRef<() => void>(() => undefined)
  useEffect(() => {
    shortcutRef.current = () => send()
  })
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault()
        shortcutRef.current()
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [])

  return { sending, send, reminderOpen, setReminderOpen }
}

type Sender = ReturnType<typeof useSend>

// useFormatting wraps the plain-text selection with markdown-style markers and
// keeps focus in the textarea.
function useFormatting(content: MessageContent) {
  const { t } = useI18n()
  const bodyRef = useRef<HTMLTextAreaElement>(null)
  const apply = (kind: FormatKind) => {
    const ta = bodyRef.current
    const { body } = content
    const start = ta ? ta.selectionStart : body.length
    const end = ta ? ta.selectionEnd : body.length
    const insert = formatText(kind, body.slice(start, end), t(FORMAT_PLACEHOLDER_KEYS[kind]))
    content.setBody(body.slice(0, start) + insert + body.slice(end))
    requestAnimationFrame(() => {
      if (!ta) return
      ta.focus()
      const pos = start + insert.length
      ta.setSelectionRange(pos, pos)
    })
  }
  return { bodyRef, apply }
}

export function ComposePage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { t } = useI18n()
  const [isFullscreen, setIsFullscreen] = useState(false)
  // Scheduled ("send later"): scheduleOpen toggles the picker; scheduledAt holds
  // the chosen wall clock (datetime-local value, interpreted in the display tz).
  const [scheduleOpen, setScheduleOpen] = useState(false)
  const [scheduledAt, setScheduledAt] = useState("")
  const richTextRef = useRef<RichTextHandle | null>(null)

  const senders = useSenderIdentities()
  const diagnostics = useDiagnostics()
  const recipients = useRecipients(searchParams)
  const content = useMessageContent(searchParams, recipients)
  const extras = useSignaturesAndTemplates(searchParams, content.setSubject, content.setBody)
  const attachments = useAttachments()
  const pickers = usePickers(attachments.add)
  const options = useMailOptions()
  const drafts = useDraftSaving(content, recipients, senders.selected)
  const formatting = useFormatting(content)
  const sender = useSend({
    content, recipients, senders, diagnostics, attachments: attachments.attachments, options,
    richTextMode: extras.richTextMode, richTextRef, scheduledAt, drafts,
  })

  const handleDiscard = () => {
    const { to } = recipients.lists
    if (!(content.subject || content.body || to.length > 0)) {
      navigate("/inbox")
      return
    }
    if (confirm(t("compose.discardConfirm"))) void drafts.save()
  }

  return (
    <div className={cn(
      "flex flex-col bg-background transition-all duration-200",
      isFullscreen ? "fixed inset-0 z-50" : "h-[calc(100vh-4rem)]"
    )}>
      <ComposeHeader
        onBack={handleDiscard}
        drafts={drafts}
        isFullscreen={isFullscreen}
        onToggleFullscreen={() => setIsFullscreen(!isFullscreen)}
        scheduledAt={scheduledAt}
        onToggleSchedule={() => setScheduleOpen((o) => !o)}
        extras={extras}
        sender={sender}
        canSend={recipients.lists.to.length > 0}
      />
      {scheduleOpen && <SchedulePicker value={scheduledAt} onChange={setScheduledAt} />}

      {/* Recipients. The sender comes first, as Outlook and OWA order it: the
          identity a message goes out under is the first decision, and a reader
          who scrolled past it would not notice they were sending as a shared
          mailbox. */}
      <div className="border-b px-4 py-2 space-y-2">
        <SenderRow senders={senders} diagnostics={diagnostics} />
        <RecipientRow field="to" label={t("common.to")} rec={recipients}>
          <Button variant="ghost" size="sm" className="text-xs h-7" onClick={() => recipients.setShowCc(!recipients.showCc)}>
            {t("common.cc")}
          </Button>
          <Button variant="ghost" size="sm" className="text-xs h-7" onClick={() => recipients.setShowBcc(!recipients.showBcc)}>
            {t("common.bcc")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-xs h-7"
            onClick={() => void recipients.checkNames()}
            disabled={recipients.checking}
            title={t("compose.checkNamesHint")}
          >
            {t("compose.checkNames")}
          </Button>
        </RecipientRow>
        {recipients.showCc && <RecipientRow field="cc" label={t("common.cc")} rec={recipients} />}
        {recipients.showBcc && <RecipientRow field="bcc" label={t("common.bcc")} rec={recipients} />}
        {diagnostics.show && diagnostics.diagnostics.length > 0 && (
          <DiagnosticsPanel entries={diagnostics.diagnostics} onClose={() => diagnostics.setShow(false)} />
        )}
        <div className="flex items-center gap-2">
          <span className="min-w-24 shrink-0 text-sm text-muted-foreground">{t("compose.subjectShort")}:</span>
          <Input
            className="flex-1 border-0 shadow-none focus-visible:ring-0 px-0 py-1 h-8"
            placeholder={t("common.subject")}
            value={content.subject}
            onChange={(e) => content.setSubject(e.target.value)}
          />
        </div>
      </div>

      <FormatBar richTextMode={extras.richTextMode} onFormat={formatting.apply} />

      <div className="flex-1 overflow-hidden">
        {extras.richTextMode ? (
          <RichTextEditor
            ref={richTextRef}
            value={content.body}
            onChange={content.setBody}
            placeholder={t("compose.writeMessage")}
            className="h-full"
          />
        ) : (
          <Textarea
            ref={formatting.bodyRef}
            className="h-full resize-none border-0 shadow-none focus-visible:ring-0 p-4"
            placeholder={t("compose.writeMessage")}
            value={content.body}
            onChange={(e) => content.setBody(e.target.value)}
          />
        )}
      </div>

      {attachments.attachments.length > 0 && <AttachmentBar attachments={attachments} />}

      <div className="flex items-center justify-between border-t px-4 py-2">
        <ComposeFooterActions attachments={attachments} pickers={pickers} options={options} sender={sender} />
        <MailOptionsDialog options={options} />
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <kbd className="rounded border px-1.5 py-0.5 text-xs bg-muted">⌘</kbd>
          <span>+</span>
          <kbd className="rounded border px-1.5 py-0.5 text-xs bg-muted">Enter</kbd>
          <span>{t("compose.toSend")}</span>
        </div>
      </div>
    </div>
  )
}

// formatLastSaved renders when the draft was last stored.
function formatLastSaved(t: TFunc, lastSaved: Date): string {
  const diff = Math.floor((Date.now() - lastSaved.getTime()) / 1000)
  if (diff < 60) return t("compose.justNow")
  if (diff < 3600) return t("compose.minutesAgo", { minutes: String(Math.floor(diff / 60)) })
  return lastSaved.toLocaleTimeString([], withTz({ hour: "2-digit", minute: "2-digit" }))
}

function SaveStatus({ drafts }: { drafts: DraftSaving }) {
  const { t } = useI18n()
  if (!drafts.lastSaved) return null
  return (
    <span className="flex items-center gap-1 text-xs text-muted-foreground ml-2">
      {drafts.saving ? (
        <>
          <Clock className="h-3 w-3 animate-pulse" />
          {t("common.saving")}
        </>
      ) : (
        <>
          <Check className="h-3 w-3" />
          {t("compose.saved", { time: formatLastSaved(t, drafts.lastSaved) })}
        </>
      )}
    </span>
  )
}

interface ComposeHeaderProps {
  onBack: () => void
  drafts: DraftSaving
  isFullscreen: boolean
  onToggleFullscreen: () => void
  scheduledAt: string
  onToggleSchedule: () => void
  extras: SignaturesAndTemplates
  sender: Sender
  canSend: boolean
}

function ComposeHeader(p: ComposeHeaderProps) {
  const { t } = useI18n()
  return (
    <div className="flex items-center justify-between border-b px-4 py-2">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="icon" onClick={p.onBack}>
          <ArrowLeft className="h-5 w-5" />
        </Button>
        <span className="font-medium">{t("compose.newMessage")}</span>
        <SaveStatus drafts={p.drafts} />
      </div>
      <div className="flex items-center gap-1">
        <Button
          variant="ghost"
          size="icon"
          onClick={p.onToggleFullscreen}
          title={p.isFullscreen ? t("compose.exitFullscreen") : t("compose.fullscreen")}
        >
          {p.isFullscreen ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
        </Button>
        <Button variant="ghost" size="icon" onClick={() => void p.drafts.save()} disabled={p.drafts.saving} title={t("compose.saveDraftTooltip")}>
          <Save className="h-4 w-4" />
        </Button>
        <Button variant={p.scheduledAt ? "secondary" : "ghost"} size="icon" onClick={p.onToggleSchedule} title={t("compose.scheduleSend")}>
          <Clock className="h-4 w-4" />
        </Button>
        <EntryPicker
          entries={p.extras.signatures}
          selected={p.extras.signature}
          onPick={p.extras.setSignature}
          title={t("compose.signature")}
          trigger={<span className="text-xs font-serif italic">Sig</span>}
        />
        <EntryPicker
          entries={p.extras.templates}
          selected={p.extras.template}
          onPick={p.extras.setTemplate}
          title={t("compose.template")}
          trigger={<span className="text-xs font-bold">Tpl</span>}
        />
        <SendButton sender={p.sender} scheduled={!!p.scheduledAt} disabled={!p.canSend} />
      </div>
    </div>
  )
}

function SendButton({ sender, scheduled, disabled }: { sender: Sender; scheduled: boolean; disabled: boolean }) {
  const { t } = useI18n()
  const idle = scheduled ? t("compose.scheduleSend") : t("common.send")
  const busy = scheduled ? t("compose.scheduling") : t("common.sending")
  return (
    <Button className="gap-2" onClick={() => sender.send()} disabled={sender.sending || disabled}>
      <Send className="h-4 w-4" />
      {sender.sending ? busy : idle}
    </Button>
  )
}

// EntryPicker is the header menu that picks a signature or a template.
function EntryPicker<E extends { name: string; is_html: boolean }>({ entries, selected, onPick, title, trigger }: {
  entries: E[]
  selected: E | null
  onPick: (entry: E) => void
  title: string
  trigger: ReactNode
}) {
  const [open, setOpen] = useState(false)
  if (entries.length === 0) return null
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" title={title}>{trigger}</Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {entries.map((entry) => (
          <DropdownMenuItem
            key={entry.name}
            onClick={() => {
              onPick(entry)
              setOpen(false)
            }}
            className={selected?.name === entry.name ? "bg-accent" : ""}
          >
            <span className="mr-2">{entry.is_html ? "HTML" : "TXT"}</span>
            {entry.name}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// SchedulePicker chooses an absolute send-later time (interpreted in the display
// timezone); clearing the time returns to an immediate send.
function SchedulePicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useI18n()
  return (
    <div className="flex flex-wrap items-center gap-2 border-b bg-muted/40 px-4 py-2">
      <Clock className="h-4 w-4 text-muted-foreground" />
      <span className="text-sm text-muted-foreground">{t("compose.scheduleSendAt")}:</span>
      <Input type="datetime-local" className="h-8 w-auto" value={value} onChange={(e) => onChange(e.target.value)} />
      {value && (
        <Button variant="ghost" size="sm" onClick={() => onChange("")}>
          {t("compose.scheduleClear")}
        </Button>
      )}
    </div>
  )
}

function SelectedSender({ identity }: { identity: SenderIdentity | null }) {
  const { t } = useI18n()
  if (!identity) return <span>{t("compose.selectSender")}</span>
  return (
    <>
      <span className="truncate max-w-[150px]">{identity.displayName || identity.email}</span>
      <SenderBadge identity={identity} />
    </>
  )
}

function SenderBadge({ identity }: { identity: SenderIdentity }) {
  const { t } = useI18n()
  if (identity.type === "personal") return null
  return (
    <Badge variant="secondary" className="text-[10px] h-4 ml-1">
      {identity.type === "send-on-behalf" ? t("compose.onBehalf") : t("compose.sendAs")}
    </Badge>
  )
}

function SenderRow({ senders, diagnostics }: { senders: Senders; diagnostics: Diagnostics }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const selected = senders.selected
  const canSend = selected?.canSend ?? true
  const sendError = !canSend && selected ? t("compose.noSendPermission", { email: selected.email }) : null
  return (
    <div className="flex items-center gap-2">
      <span className="min-w-24 shrink-0 text-sm text-muted-foreground flex items-center gap-1">
        <Mail className="h-3 w-3 shrink-0" />
        {t("common.from")}:
      </span>
      <div className="flex-1 flex items-center gap-2">
        <DropdownMenu open={open} onOpenChange={setOpen}>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className={cn("h-7 text-xs gap-1", !canSend && "border-red-500 text-red-500")}>
              <SelectedSender identity={selected} />
              <ChevronDown className="h-3 w-3 ml-1" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-80">
            <div className="p-2 text-xs text-muted-foreground">{t("compose.selectSenderIdentity")}</div>
            <Separator />
            <div className="max-h-60 overflow-auto">
              {senders.identities.map((identity) => (
                <IdentityItem
                  key={identity.email}
                  identity={identity}
                  onPick={() => {
                    senders.setSelected(identity)
                    setOpen(false)
                  }}
                />
              ))}
            </div>
          </DropdownMenuContent>
        </DropdownMenu>
        {sendError && (
          <div className="flex items-center gap-1 text-xs text-red-500">
            <AlertTriangle className="h-3 w-3" />
            <span className="truncate max-w-[200px]">{sendError}</span>
          </div>
        )}
        <DiagnosticsToggle diagnostics={diagnostics} />
      </div>
    </div>
  )
}

function DiagnosticsToggle({ diagnostics }: { diagnostics: Diagnostics }) {
  const { t } = useI18n()
  if (diagnostics.diagnostics.length === 0) return null
  const hasError = diagnostics.diagnostics.some((d) => d.severity === "error")
  return (
    <Button
      variant="ghost"
      size="icon"
      className="h-6 w-6"
      onClick={() => diagnostics.setShow(!diagnostics.show)}
      title={t("compose.viewDiagnostics")}
    >
      <AlertTriangle className={cn("h-4 w-4", hasError && "text-red-500")} />
    </Button>
  )
}

// IDENTITY_TYPE_BADGES is the badge each identity type carries in the picker.
const IDENTITY_TYPE_BADGES: Record<string, { variant: "default" | "secondary" | "outline"; key: string }> = {
  personal: { variant: "default", key: "nav.personal" },
  "send-on-behalf": { variant: "secondary", key: "compose.onBehalf" },
  "send-as": { variant: "outline", key: "compose.sendAs" },
}

function IdentityItem({ identity, onPick }: { identity: SenderIdentity; onPick: () => void }) {
  const { t } = useI18n()
  const badge = IDENTITY_TYPE_BADGES[identity.type]
  return (
    <DropdownMenuItem
      onClick={onPick}
      className={cn("flex flex-col items-start py-2 cursor-pointer", !identity.canSend && "opacity-50")}
      disabled={!identity.canSend}
    >
      <div className="flex items-center gap-2 w-full">
        <span className="font-medium text-sm">{identity.displayName || identity.email}</span>
        {badge && <Badge variant={badge.variant} className="text-[10px] h-4">{t(badge.key)}</Badge>}
      </div>
      {identity.mailboxOwner && (
        <span className="text-xs text-muted-foreground">{t("compose.sharedMailbox", { owner: identity.mailboxOwner })}</span>
      )}
      {!identity.canSend && (
        <span className="text-xs text-red-500 flex items-center gap-1 mt-1">
          <AlertTriangle className="h-3 w-3" />
          {t("compose.noSendPermissionShort")}
        </span>
      )}
    </DropdownMenuItem>
  )
}

// RecipientRow is one recipient field: its chips, the address-book picker, the
// free-text input and the inline suggestions. The label column is a MINIMUM
// width, not a fixed one, so a longer translation pushes the field instead of
// running under it. Every row carries the same class, or they stop lining up.
function RecipientRow({ field, label, rec, children }: { field: RecipientField; label: string; rec: Recipients; children?: ReactNode }) {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-2">
      <span className="min-w-24 shrink-0 text-sm text-muted-foreground">{label}:</span>
      <div className="relative flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
        {rec.lists[field].map((r) => (
          <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
            {r.name}
            <button onClick={() => rec.remove(r.id, field)} className="ml-0.5 rounded-full hover:bg-muted p-0.5">
              <X className="h-3 w-3" />
            </button>
          </Badge>
        ))}
        <AddressBookPicker field={field} rec={rec} />
        <input
          className="flex-1 min-w-[160px] bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          placeholder={t("compose.searchOrType")}
          value={rec.input[field]}
          onChange={(e) => rec.type(field, e.target.value)}
          onFocus={() => rec.setActiveField(field)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault()
              rec.commitTyped(field)
            }
          }}
          onBlur={() => rec.commitTyped(field)}
        />
        <SuggestionsPanel field={field} rec={rec} />
      </div>
      {children}
    </div>
  )
}

function AddressBookPicker({ field, rec }: { field: RecipientField; rec: Recipients }) {
  const { t } = useI18n()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="h-6 w-6" title={t("compose.searchAddressBook")} aria-label={t("compose.searchAddressBook")}>
          <Plus className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <div className="p-2">
          <Input placeholder={t("compose.searchPeople")} value={rec.searchQuery} onChange={(e) => rec.setSearchQuery(e.target.value)} />
        </div>
        <Separator />
        <div className="max-h-48 overflow-auto">
          {rec.suggestions.map((contact) => (
            <DropdownMenuItem key={contact.id} onClick={() => rec.add(contact, field)} className="flex flex-col items-start py-2">
              <span className="font-medium">{contact.name}</span>
              <span className="text-xs text-muted-foreground">{contact.email}</span>
            </DropdownMenuItem>
          ))}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// SuggestionsPanel renders the inline GAL/contact suggestions under the active
// field. onMouseDown preventDefault keeps the input's onBlur (which commits typed
// text) from firing before the click.
function SuggestionsPanel({ field, rec }: { field: RecipientField; rec: Recipients }) {
  if (rec.activeField !== field || rec.input[field].trim().length < 2 || rec.suggestions.length === 0) return null
  return (
    <div className="absolute left-0 top-full z-20 mt-1 w-72 max-h-48 overflow-auto rounded-md border bg-popover shadow-md">
      {rec.suggestions.map((contact) => (
        <button
          key={contact.id}
          type="button"
          onMouseDown={(e) => {
            e.preventDefault()
            rec.pick(contact, field)
          }}
          className="flex w-full flex-col items-start px-3 py-2 text-left hover:bg-accent"
        >
          <span className="text-sm font-medium">{contact.name}</span>
          <span className="text-xs text-muted-foreground">{contact.email}</span>
        </button>
      ))}
    </div>
  )
}

function DiagnosticsPanel({ entries, onClose }: { entries: DiagnosticEntry[]; onClose: () => void }) {
  const { t } = useI18n()
  return (
    <div className="border rounded-md bg-muted/30 p-3 space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">{t("compose.mailboxDiagnostics")}</span>
        <Button variant="ghost" size="icon" className="h-5 w-5" onClick={onClose}>
          <X className="h-3 w-3" />
        </Button>
      </div>
      {entries.map((entry) => <DiagnosticItem key={entry.id} entry={entry} />)}
    </div>
  )
}

function DiagnosticItem({ entry }: { entry: DiagnosticEntry }) {
  const { t } = useI18n()
  return (
    <div
      className={cn(
        "text-xs p-2 rounded border-l-2",
        entry.severity === "error" && "bg-red-50 border-red-500 text-red-700 dark:bg-red-950 dark:text-red-400",
        entry.severity === "warning" && "bg-yellow-50 border-yellow-500 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-400",
        entry.severity === "info" && "bg-blue-50 border-blue-500 text-blue-700 dark:bg-blue-950 dark:text-blue-400"
      )}
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
        <div className="flex-1">
          <div className="font-medium">{entry.message}</div>
          {entry.mailbox && <div className="text-muted-foreground mt-0.5">{t("compose.mailboxLabel", { mailbox: entry.mailbox })}</div>}
          {entry.nextStep && (
            <div className="text-muted-foreground mt-1 flex items-center gap-1">
              <span>{t("compose.nextStep")}</span>
              <span className="font-medium">{entry.nextStep}</span>
            </div>
          )}
          <div className="text-muted-foreground mt-1">{new Date(entry.timestamp).toLocaleString([], withTz())}</div>
        </div>
      </div>
    </div>
  )
}

// FORMAT_BUTTONS lists the plain-text formatting toolbar, with a separator before
// the link group.
const FORMAT_BUTTONS: { kind: FormatKind; icon: LucideIcon; titleKey: string; separatorBefore?: boolean }[] = [
  { kind: "bold", icon: Bold, titleKey: "compose.bold" },
  { kind: "italic", icon: Italic, titleKey: "compose.italic" },
  { kind: "underline", icon: Underline, titleKey: "compose.underline" },
  { kind: "link", icon: Link, titleKey: "compose.insertLink", separatorBefore: true },
  { kind: "list", icon: List, titleKey: "compose.bulletList" },
  { kind: "image", icon: Image, titleKey: "compose.insertImageLink" },
]

// FormatBar is the formatting toolbar, PLAIN-TEXT MODE ONLY. In rich-text mode
// the editor renders its own toolbar over the contentEditable, so showing this
// one gave two toolbars, and its buttons write markdown markers into the
// plain-text body through bodyRef, which does not exist in that mode: the marker
// landed as literal text at the end of the HTML body. The send hint belongs to
// the page, not to either toolbar, so rich-text mode keeps it.
function FormatBar({ richTextMode, onFormat }: { richTextMode: boolean; onFormat: (kind: FormatKind) => void }) {
  const { t } = useI18n()
  if (richTextMode) {
    return (
      <div className="border-b px-4 py-1 bg-muted/30">
        <span className="text-xs text-muted-foreground">{t("compose.sendTip")}</span>
      </div>
    )
  }
  return (
    <div className="flex items-center gap-1 border-b px-4 py-1 bg-muted/30">
      {/* onMouseDown preventDefault keeps focus (and the selection) in the body
          textarea so the format wraps the selected text instead of inserting a
          placeholder. */}
      {FORMAT_BUTTONS.map(({ kind, icon: Icon, titleKey, separatorBefore }) => (
        <span key={kind} className="contents">
          {separatorBefore && <Separator orientation="vertical" className="h-6" />}
          <Button variant="ghost" size="icon" className="h-8 w-8" title={t(titleKey)} onMouseDown={(e) => e.preventDefault()} onClick={() => onFormat(kind)}>
            <Icon className="h-4 w-4" />
          </Button>
        </span>
      ))}
      <span className="text-xs text-muted-foreground ml-2">{t("compose.sendTip")}</span>
    </div>
  )
}

function AttachmentBar({ attachments }: { attachments: Attachments }) {
  return (
    <div className="border-t px-4 py-2 bg-muted/30">
      <div className="flex flex-wrap gap-2">
        {attachments.attachments.map((att) => (
          <div key={att.id} className="flex items-center gap-2 rounded border bg-background px-3 py-1.5">
            <Paperclip className="h-4 w-4 text-muted-foreground" />
            <span className="text-sm">{att.name}</span>
            <span className="text-xs text-muted-foreground">({formatSize(att.size)})</span>
            <button onClick={() => attachments.remove(att.id)} className="ml-1 rounded-full hover:bg-muted p-0.5">
              <X className="h-3 w-3" />
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

// ToggleOption is a footer toggle for one S/MIME option.
function ToggleOption({ on, onToggle, icon: Icon, label }: { on: boolean; onToggle: () => void; icon: LucideIcon; label: string }) {
  return (
    <Button type="button" variant={on ? "secondary" : "outline"} size="sm" onClick={onToggle} title={label} aria-pressed={on}>
      <Icon className={on ? "mr-1.5 h-4 w-4" : "mr-1.5 h-4 w-4 opacity-40"} />
      {label}
    </Button>
  )
}

function ComposeFooterActions({ attachments, pickers, options, sender }: { attachments: Attachments; pickers: Pickers; options: MailOptions; sender: Sender }) {
  const { t } = useI18n()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const active = mailOptionsActive({
    importance: options.importance, sensitivity: options.sensitivity,
    requestReadReceipt: options.readReceipt, requestDeliveryReceipt: options.deliveryReceipt,
  })
  return (
    <div className="flex items-center gap-2">
      <Button variant="outline" size="icon" onClick={() => fileInputRef.current?.click()}>
        <Paperclip className="h-5 w-5" />
      </Button>
      <input
        type="file"
        multiple
        ref={fileInputRef}
        className="hidden"
        onChange={(e) => attachments.attachFiles(Array.from(e.target.files ?? []))}
      />
      <Button type="button" variant="outline" size="icon" onClick={() => void pickers.msg.openFolder(pickers.msg.folder)} title={t("compose.attachMessage")}>
        <Mail className="h-5 w-5" />
      </Button>
      <Button type="button" variant="outline" size="icon" onClick={pickers.contact.show} title={t("compose.attachContact")}>
        <ContactIcon className="h-5 w-5" />
      </Button>
      <AttachItemMenu onPick={(kind) => void pickers.item.show(kind)} />
      <MessagePickerDialog picker={pickers.msg} />
      <ContactPickerDialog picker={pickers.contact} />
      <ItemPickerDialog picker={pickers.item} />
      <AttachReminderDialog sender={sender} />
      <Button type="button" variant={active ? "secondary" : "outline"} size="sm" onClick={() => options.setOpen(true)} title={t("compose.mailOptions")}>
        <SlidersHorizontal className="mr-1.5 h-4 w-4" />
        {t("compose.mailOptions")}
      </Button>
      <ToggleOption on={options.sign} onToggle={() => options.setSign((v) => !v)} icon={Shield} label={t("compose.signMessage")} />
      <ToggleOption on={options.encrypt} onToggle={() => options.setEncrypt((v) => !v)} icon={Key} label={t("compose.encryptMessage")} />
    </div>
  )
}

function AttachItemMenu({ onPick }: { onPick: (kind: ItemKind) => void }) {
  const { t } = useI18n()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" size="icon" title={t("compose.attachItem")}>
          <CalendarClock className="h-5 w-5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuItem onClick={() => onPick("event")}>
          <CalendarClock className="mr-2 h-4 w-4" />
          {t("compose.attachAppointment")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onPick("task")}>
          <ListTodo className="mr-2 h-4 w-4" />
          {t("compose.attachTask")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onPick("note")}>
          <StickyNote className="mr-2 h-4 w-4" />
          {t("compose.attachNote")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// PickerMessage is the loading or empty line a picker list shows instead of rows.
function PickerMessage({ text }: { text: string }) {
  return <p className="py-6 text-center text-sm text-muted-foreground">{text}</p>
}

// PickerRow is one pickable entry: a title and a secondary line.
function PickerRow({ title, detail, onClick }: { title: string; detail?: ReactNode; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full flex-col items-start gap-0.5 rounded-md px-2 py-2 text-left hover:bg-accent"
    >
      <span className="w-full truncate text-sm font-medium">{title}</span>
      {detail && <span className="w-full truncate text-xs text-muted-foreground">{detail}</span>}
    </button>
  )
}

// PickerList renders a picker's rows, or the loading or empty line.
function PickerList<I>({ loading, items, render }: { loading: boolean; items: I[]; render: (item: I) => ReactNode }) {
  const { t } = useI18n()
  if (loading) return <PickerMessage text={t("common.loading")} />
  if (items.length === 0) return <PickerMessage text={t("common.noResults")} />
  return <>{items.map(render)}</>
}

function MessagePickerDialog({ picker }: { picker: Pickers["msg"] }) {
  const { t } = useI18n()
  return (
    <Dialog open={picker.open} onOpenChange={picker.setOpen}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("compose.attachMessageTitle")}</DialogTitle>
          <DialogDescription>{t("compose.attachMessageDesc")}</DialogDescription>
        </DialogHeader>
        <div className="flex gap-2">
          {(["inbox", "sent"] as const).map((folder) => (
            <Button
              key={folder}
              type="button"
              size="sm"
              variant={picker.folder === folder ? "secondary" : "outline"}
              onClick={() => void picker.openFolder(folder)}
            >
              {t(`nav.${folder}`)}
            </Button>
          ))}
        </div>
        <div className="max-h-80 overflow-y-auto">
          <PickerList
            loading={picker.loading}
            items={picker.list}
            render={(m) => (
              <PickerRow key={m.id} title={m.subject || t("compose.noSubject")} detail={m.fromName || m.from} onClick={() => void picker.attach(m)} />
            )}
          />
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ContactPickerDialog({ picker }: { picker: Pickers["contact"] }) {
  const { t } = useI18n()
  return (
    <Dialog open={picker.open} onOpenChange={picker.setOpen}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("compose.attachContactTitle")}</DialogTitle>
          <DialogDescription>{t("compose.attachContactDesc")}</DialogDescription>
        </DialogHeader>
        <div className="max-h-72 overflow-auto">
          {picker.list.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">{t("compose.noContacts")}</p>
          ) : (
            <ul className="divide-y">
              {picker.list.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    className="flex w-full items-center gap-2 px-2 py-2 text-left text-sm hover:bg-accent/50"
                    onClick={() => void picker.attach(c)}
                  >
                    <ContactIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate">{c.name}</span>
                    {c.email && <span className="shrink-0 text-xs text-muted-foreground truncate">{c.email}</span>}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ItemPickerDialog({ picker }: { picker: Pickers["item"] }) {
  const { t } = useI18n()
  return (
    <Dialog open={picker.open} onOpenChange={picker.setOpen}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t(ITEM_KIND_TITLE_KEYS[picker.kind])}</DialogTitle>
          <DialogDescription>{t("compose.attachItemDesc")}</DialogDescription>
        </DialogHeader>
        <div className="max-h-80 overflow-y-auto">
          <ItemPickerList picker={picker} />
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ItemPickerList({ picker }: { picker: Pickers["item"] }) {
  const { t } = useI18n()
  const noSubject = t("compose.noSubject")
  if (picker.kind === "event") {
    return (
      <PickerList
        loading={picker.loading}
        items={picker.events}
        render={(ev) => (
          <PickerRow key={ev.uid} title={ev.summary || noSubject} detail={ev.start ? new Date(ev.start).toLocaleString() : ""} onClick={() => void picker.attachEvent(ev)} />
        )}
      />
    )
  }
  if (picker.kind === "task") {
    return (
      <PickerList
        loading={picker.loading}
        items={picker.tasks}
        render={(tk) => (
          <PickerRow key={tk.uid} title={tk.summary || noSubject} detail={tk.due ? new Date(tk.due).toLocaleDateString() : undefined} onClick={() => void picker.attachTask(tk)} />
        )}
      />
    )
  }
  return (
    <PickerList
      loading={picker.loading}
      items={picker.notes}
      render={(nt) => <PickerRow key={nt.id} title={nt.title || noSubject} detail={nt.body} onClick={() => void picker.attachNote(nt)} />}
    />
  )
}

function AttachReminderDialog({ sender }: { sender: Sender }) {
  const { t } = useI18n()
  return (
    <Dialog open={sender.reminderOpen} onOpenChange={sender.setReminderOpen}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("compose.attachReminderTitle")}</DialogTitle>
          <DialogDescription>{t("compose.attachReminderDesc")}</DialogDescription>
        </DialogHeader>
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={() => sender.setReminderOpen(false)}>
            {t("compose.attachReminderCancel")}
          </Button>
          <Button onClick={() => { sender.setReminderOpen(false); sender.send(true) }}>
            {t("compose.attachReminderSend")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// MailOptionsDialog gathers importance, sensitivity and tracking in one place.
// S/MIME sign and encrypt stay as their own footer toggles.
function MailOptionsDialog({ options }: { options: MailOptions }) {
  const { t } = useI18n()
  const selectClass = "w-48 rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
  return (
    <Dialog open={options.open} onOpenChange={options.setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("compose.mailOptions")}</DialogTitle>
          <DialogDescription>{t("compose.mailOptionsDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="flex items-center justify-between gap-4">
            <label htmlFor="mo-importance" className="text-sm font-medium">{t("compose.messageImportance")}</label>
            <select
              id="mo-importance"
              value={options.importance}
              onChange={(e) => options.setImportance(e.target.value as "low" | "normal" | "high")}
              className={selectClass}
            >
              <option value="low">{t("compose.importanceLow")}</option>
              <option value="normal">{t("compose.importanceNormal")}</option>
              <option value="high">{t("compose.importanceHigh")}</option>
            </select>
          </div>
          <div className="flex items-center justify-between gap-4">
            <label htmlFor="mo-sensitivity" className="text-sm font-medium">{t("compose.messageSensitivity")}</label>
            <select
              id="mo-sensitivity"
              value={options.sensitivity}
              onChange={(e) => options.setSensitivity(e.target.value as "normal" | "personal" | "private" | "confidential")}
              className={selectClass}
            >
              <option value="normal">{t("compose.sensitivityNormal")}</option>
              <option value="personal">{t("compose.sensitivityPersonal")}</option>
              <option value="private">{t("compose.sensitivityPrivate")}</option>
              <option value="confidential">{t("compose.sensitivityConfidential")}</option>
            </select>
          </div>
          <div>
            <p className="mb-2 text-sm font-medium">{t("compose.trackingOptions")}</p>
            <label className="flex items-center gap-2 py-1 text-sm">
              <input type="checkbox" checked={options.readReceipt} onChange={(e) => options.setReadReceipt(e.target.checked)} />
              {t("compose.requestReadReceipt")}
            </label>
            <label className="flex items-center gap-2 py-1 text-sm">
              <input type="checkbox" checked={options.deliveryReceipt} onChange={(e) => options.setDeliveryReceipt(e.target.checked)} />
              {t("compose.requestDeliveryReceipt")}
            </label>
          </div>
        </div>
        <DialogFooter>
          <Button type="button" onClick={() => options.setOpen(false)}>{t("common.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
