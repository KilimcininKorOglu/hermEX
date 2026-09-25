import { useState, useEffect, useRef, type DragEvent, type MouseEvent, type ReactNode } from "react"
import { useParams, useNavigate } from "react-router-dom"
import {
  ArrowLeft,
  Trash2,
  Reply,
  ReplyAll,
  Forward,
  ExternalLink,
  Mail,
  FolderInput,
  Flag,
  Tag,
  X,
  Plus,
  CalendarCheck,
  Check,
  HelpCircle,
  Paperclip,
  Download,
  CalendarArrowDown,
  Printer,
  Code,
  FileText,
  ShieldCheck,
  Undo2,
  RotateCcw,
  Copy,
  Braces,
  StickyNote,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Badge } from "@/components/ui/badge"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Separator } from "@/components/ui/separator"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import { escapeTextBody, sanitizeEmailBody } from "@/utils/sanitize"
import { forwardParams, replyAllParams, replyParams, type QuoteLabels } from "@/utils/replyParams"
import { MAX_DRAG_BYTES, blobToBase64, setAttachmentDrag } from "@/utils/attachmentDrag"
import { dragSet, emptySelection, selectOnClick } from "@/utils/attachmentSelection"
import {
  FOLLOWUP_TOAST_KEYS,
  emailDetailOf,
  followupPatch,
  proposalRange,
  proposeWindow,
  readerShortcut,
  recallOutcome,
  replySubject,
  senderInitials,
  toDatetimeLocal,
  type BodyView,
  type EmailDetail,
  type FollowupAction,
  type ReaderAction,
} from "@/utils/emailDetail"
import { cn } from "@/lib/utils"
import api from "@/utils/api"
import type { MeetingInvite, AttachmentInfo, Mail as MailMessage, Note } from "@/utils/api"
import * as smimeStore from "@/utils/smime"
import { formatAbsolute, withTz } from "@/utils/date"
import { getShortcutMode } from "@/utils/shortcutMode"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"

type TFunc = (key: string, params?: Record<string, string>) => string

// pdfZoomFragment maps the appearance PDF-zoom mode onto the PDF Open Parameters
// fragment the embedded viewer understands: "page-width"/"page-actual" become
// FitH/actual-size #view, and "auto" leaves the viewer's own default zoom.
function pdfZoomFragment(mode: string): string {
  switch (mode) {
    case "page-width": return "view=FitH"
    case "page-actual": return "zoom=100"
    default: return "" // auto: no override
  }
}

// formatFileSize renders a byte count as a human-readable size.
function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// FOLLOWUP_COLORS mirrors objectstore's six follow-up flag colours (purple..red,
// values 1..6), each with the i18n name key and the Tailwind fill for the flag.
const FOLLOWUP_COLORS = [
  { value: 1, key: "followUpColorPurple", fill: "fill-purple-500 text-purple-500" },
  { value: 2, key: "followUpColorOrange", fill: "fill-orange-500 text-orange-500" },
  { value: 3, key: "followUpColorGreen", fill: "fill-green-500 text-green-500" },
  { value: 4, key: "followUpColorYellow", fill: "fill-yellow-500 text-yellow-500" },
  { value: 5, key: "followUpColorBlue", fill: "fill-blue-500 text-blue-500" },
  { value: 6, key: "followUpColorRed", fill: "fill-red-500 text-red-500" },
] as const

// STANDARD_FOLDERS are the built-in mailbox names already offered as fixed copy
// targets below, so getMailboxes() entries matching these are dropped from the
// custom-folder list.
const STANDARD_FOLDERS = new Set(["inbox", "sent", "drafts", "trash", "junk", "scheduled"])

// FOLDER_TARGETS are the fixed move and copy targets: the folder slug and the
// i18n key of its label.
const FOLDER_TARGETS = [
  { folder: "inbox", key: "nav.inbox" },
  { folder: "archive", key: "common.archive" },
  { folder: "spam", key: "nav.spam" },
  { folder: "trash", key: "nav.trash" },
]

// Largest PDF that previews on its own when the message opens, used only until the
// appearance request answers with the operator's own cap. It matches the server's
// built-in default, so the two agree when no operator value has been saved.
const FALLBACK_PREVIEW_MAX_BYTES = 2 * 1024 * 1024

// signerTrusted asks the server whether a locally verified signature belongs to
// the sender. A valid signature only proves someone held the signing key;
// attributing it to the sender is the server's decision (it has the trust
// anchors and the sender's published certificate), so without that answer the
// signature stays unverified.
async function signerTrusted(verdict: NonNullable<ReturnType<typeof smimeStore.verifyMime>>, from: string): Promise<boolean> {
  if (!verdict.verified || !verdict.signerCert) return false
  try {
    return (await api.verifySMIMESigner(verdict.signerCert, from)).trusted
  } catch {
    return false
  }
}

// decryptedView decrypts a browser-mode S/MIME message and verifies its inner
// signature in the browser; posting the decrypted signed content to the server
// would leak the plaintext.
async function decryptedView(result: MailMessage, base: BodyView): Promise<BodyView> {
  const inner = smimeStore.decryptMime(await api.getMessageRaw(result.id))
  const extracted = smimeStore.extractMimeBody(inner)
  const content = extracted.html ? extracted.body : escapeTextBody(extracted.body)
  const verdict = smimeStore.verifyMime(inner)
  if (!verdict) return { ...base, content }
  return { content, smimeSigned: true, smimeSignedBy: verdict.signedBy, smimeVerified: await signerTrusted(verdict, result.from) }
}

// bodyView returns the body the reader renders. The body goes into an HTML
// sink, so a text/plain body is escaped first: the server reports which one it
// sent in bodyType. An S/MIME encrypted message is decrypted client-side by a
// browser-mode reader (key in this browser); a server-mode reader's message was
// already decrypted server-side.
async function bodyView(result: MailMessage, t: TFunc): Promise<BodyView> {
  const base: BodyView = {
    content: result.bodyType === "text" ? escapeTextBody(result.body) : result.body,
    smimeSigned: result.smimeSigned,
    smimeVerified: result.smimeVerified,
    smimeSignedBy: result.smimeSignedBy,
  }
  if (!result.smimeEncrypted || !(await smimeStore.hasIdentity())) return base
  if (!smimeStore.isUnlocked()) return { ...base, content: `<p>${t("emailDetail.smimeLockedBody")}</p>` }
  try {
    return await decryptedView(result, base)
  } catch {
    return { ...base, content: `<p>${t("emailDetail.smimeDecryptFailed")}</p>` }
  }
}

// inviteOf detects a meeting invite so the reader can offer RSVP actions. A
// failure here must not block reading the message.
async function inviteOf(id: string): Promise<MeetingInvite | null> {
  try {
    const inv = await api.getInvite(id)
    return inv.isInvite ? inv : null
  } catch {
    return null
  }
}

// notesOf reads the notes annotating a mail. Annotations are an aside: a
// failure to read them must not stop the mail from being read.
async function notesOf(id: string): Promise<Note[]> {
  try {
    return (await api.getMailNotes(id)).notes ?? []
  } catch {
    return []
  }
}

// downloadBlob saves a fetched export under the given file name.
async function downloadBlob(url: string, filename: string) {
  const res = await fetch(url, { credentials: "include" })
  if (!res.ok) throw new Error()
  const href = URL.createObjectURL(await res.blob())
  const a = document.createElement("a")
  a.href = href
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(href)
}

// useReaderPrefs loads what the reader shows around the message: category
// colours, custom copy targets, the reply quoting preference, and the item
// data button, inline preview and PDF zoom from the appearance settings.
function useReaderPrefs() {
  // Category name → color, so labels render with their configured color.
  const [categoryColors, setCategoryColors] = useState<Record<string, string>>({})
  // Custom folders offered as copy-to targets, alongside the fixed built-ins.
  const [copyFolders, setCopyFolders] = useState<string[]>([])
  // When set (a saved preference), a reply does not quote the original message.
  const [omitOriginal, setOmitOriginal] = useState(false)
  // Item data: a dev-only raw property dump (the JSON the API returned). The
  // button is hidden unless the "Show item data button" advanced setting is on
  // (reference default: off).
  const [showItemData, setShowItemData] = useState(false)
  // Inline attachment preview + PDF zoom, from the file-previewing appearance
  // settings (reference SettingsFilePreviewerWidget). Preview defaults on.
  const [filePreview, setFilePreview] = useState(true)
  const [pdfZoom, setPdfZoom] = useState("page-width")
  // The cap itself is the operator's, served with the appearance settings. The
  // fallback here only covers the moment before that request answers.
  const [previewMaxBytes, setPreviewMaxBytes] = useState(FALLBACK_PREVIEW_MAX_BYTES)

  useEffect(() => {
    let cancelled = false
    api.getCategories()
      .then((res) => {
        if (cancelled) return
        const map: Record<string, string> = {}
        for (const c of res.categories ?? []) map[c.name.toLowerCase()] = c.color
        setCategoryColors(map)
      })
      .catch(() => {})
    api.getMailboxes()
      .then((res) => {
        if (cancelled) return
        setCopyFolders((res.mailboxes ?? []).filter((m) => !STANDARD_FOLDERS.has(m.toLowerCase())))
      })
      .catch(() => {})
    api.getPreferences()
      .then((res) => { if (!cancelled) setOmitOriginal(res.preferences?.omitOriginalOnReply === true) })
      .catch(() => {})
    api.getAppearanceSettings()
      .then((a) => {
        if (cancelled) return
        setShowItemData(!!a.showItemData)
        setFilePreview(a.filePreview ?? true)
        setPdfZoom(a.pdfZoom ?? "page-width")
        if (a.previewMaxBytes && a.previewMaxBytes > 0) setPreviewMaxBytes(a.previewMaxBytes)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  return { categoryColors, copyFolders, omitOriginal, showItemData, filePreview, pdfZoom, previewMaxBytes }
}

type ReaderPrefs = ReturnType<typeof useReaderPrefs>

// useMessage loads the message by id (the backend resolves it across all
// folders), with its invite and notes, and marks it read.
function useMessage(id: string | undefined) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { patchInbox } = useMailbox()
  const [email, setEmail] = useState<EmailDetail | null>(null)
  const [loading, setLoading] = useState(true)
  // Remote images are blocked on open (tracking-pixel protection); the reader
  // offers a one-time "show images" once the user chooses to load them.
  const [showImages, setShowImages] = useState(false)
  const [invite, setInvite] = useState<MeetingInvite | null>(null)
  // Notes annotating this mail. They live in the Notes folder and reference the
  // mail by its Message-ID, so nothing is written to the mail itself.
  const [notes, setNotes] = useState<Note[]>([])

  useEffect(() => {
    setShowImages(false) // re-block remote images for each newly opened message
    const show = async (result: MailMessage) => {
      // A safe-listed sender's remote images are trusted, so load them without
      // the one-time prompt (the server computed the allowlist match).
      if (result.senderTrusted) setShowImages(true)
      setEmail(emailDetailOf(result, await bodyView(result, t)))
      // Mark the message read on open (server-side) if it was unread, so the
      // unread count reflects reading, standard mail-client behavior.
      // Fire-and-forget: a failure must not block reading the message.
      if (!result.read) {
        api.setFlag(result.id, "\\Seen", true).catch(() => undefined)
        patchInbox([result.id], { read: true })
      }
      setInvite(await inviteOf(result.id))
      setNotes(await notesOf(result.id))
    }
    const loadEmail = async () => {
      if (!id) {
        setLoading(false)
        return
      }
      try {
        setLoading(true)
        const result = await api.getMessage(id)
        if (result?.id) {
          await show(result)
        } else {
          toast.error(t("emailDetail.notFound"))
          navigate("/inbox")
        }
      } catch (err) {
        console.error("Failed to load email:", err)
        toast.error(t("emailDetail.failedToLoad"))
        navigate("/inbox")
      } finally {
        setLoading(false)
      }
    }
    loadEmail()
  }, [id, navigate, patchInbox, t])

  return { email, setEmail, loading, showImages, setShowImages, invite, notes, setNotes }
}

// useAttachments holds the attachment selection, the bytes read ahead for a
// drag, and the PDF previews the reader asked for by hand.
function useAttachments(email: EmailDetail | null) {
  const { t } = useI18n()
  // A PDF preview renders through <object>, which the browser does not defer the
  // way it defers a lazy <img>, so every previewed PDF downloads in full the
  // moment the message opens. Above the threshold the preview waits for a click,
  // and these are the attachments the reader asked for by hand.
  const [previewRequested, setPreviewRequested] = useState<number[]>([])
  // Bytes held for a drag, by attachment index. A drag cannot fetch: the payload
  // has to be on the DataTransfer synchronously, so it is read when the reader
  // points at the attachment. A drag begun before that read answers carries no
  // payload, and dragging again works.
  const dragPayloads = useRef<Record<number, string>>({})
  // Which attachments the reader has selected. A drag or a download started
  // inside the selection covers all of it.
  const [selection, setSelection] = useState(emptySelection)

  // A request to preview, and a selection, cover one message, not the next.
  const emailId = email?.id
  useEffect(() => {
    setPreviewRequested([])
    setSelection(emptySelection)
  }, [emailId])

  // prepareDrag reads one attachment so it can ride on a drag into a composer,
  // including one in another tab, which shares no state with this page.
  const prepareDrag = async (att: AttachmentInfo) => {
    if (!email || dragPayloads.current[att.index] !== undefined) return
    if (att.size > MAX_DRAG_BYTES) return
    try {
      const res = await fetch(
        `/api/v1/mail/attachment?id=${encodeURIComponent(email.id)}&index=${att.index}`,
        { credentials: "include" },
      )
      if (!res.ok) return
      dragPayloads.current[att.index] = await blobToBase64(await res.blob())
    } catch {
      // A drag that finds nothing prepared simply carries no payload.
    }
  }

  const prepareAll = (indexes: number[]) => {
    for (const a of email?.attachments ?? []) {
      if (indexes.includes(a.index)) void prepareDrag(a)
    }
  }

  const handleDragStart = (e: DragEvent, att: AttachmentInfo) => {
    if (!email) return
    const wanted = dragSet(selection, att.index)
    // The selection travels whole or not at all: handing over the part that
    // happens to be read would drop the rest without saying so. The missing
    // ones are being read now, so the next drag carries everything.
    if (wanted.some((i) => dragPayloads.current[i] === undefined)) {
      prepareAll(wanted)
      return
    }
    setAttachmentDrag(e.dataTransfer, wanted.map((i) => {
      const a = email.attachments.find((x) => x.index === i)
      return { name: a?.filename ?? "attachment", type: a?.contentType || "application/octet-stream", content: dragPayloads.current[i] }
    }))
  }

  const handleDownload = async (att: AttachmentInfo) => {
    if (!email) return
    try {
      await api.downloadAttachment(email.id, att.index, att.filename)
    } catch {
      toast.error(t("emailDetail.failedToDownload"))
    }
  }

  // handleClick selects when the click carries a modifier, and opens the
  // attachment otherwise. Selecting starts the reads, so a drag of the
  // selection can hand it over whole.
  const handleClick = (e: MouseEvent, att: AttachmentInfo) => {
    const next = selectOnClick(selection, att.index, e)
    if (!next.selected) {
      void handleDownload(att)
      return
    }
    e.preventDefault()
    setSelection(next.selection)
    prepareAll(next.selection.indexes)
  }

  // handleDownloadAll fetches an archive of the attachments. With a selection it
  // holds exactly that selection, because one archive is all the operating
  // system takes from a single gesture. A same-origin anchor carries the
  // session cookie, and the server names the file.
  const handleDownloadAll = () => {
    if (!email) return
    const indexes = selection.indexes.map((i) => `&index=${i}`).join("")
    const a = document.createElement("a")
    a.href = `/api/v1/mail/attachments-zip?id=${encodeURIComponent(email.id)}${indexes}`
    a.download = "attachments.zip"
    document.body.appendChild(a)
    a.click()
    a.remove()
  }

  const requestPreview = (index: number) => setPreviewRequested((prev) => [...prev, index])

  return { selected: selection.indexes, previewRequested, requestPreview, prepareDrag, handleDragStart, handleClick, handleDownloadAll }
}

type Attachments = ReturnType<typeof useAttachments>

// useNotes adds and removes the notes annotating the open mail. A note is
// stored in the Notes folder keyed by the mail's Message-ID, so the mail itself
// is untouched and the annotation follows it when it is filed elsewhere.
function useNotes(email: EmailDetail | null, setNotes: (notes: Note[]) => void) {
  const { t } = useI18n()
  const [draft, setDraft] = useState("")
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const reload = async (mailId: string) => setNotes((await api.getMailNotes(mailId)).notes ?? [])

  const add = async () => {
    const body = draft.trim()
    if (!body || !email) return
    setBusy(true)
    try {
      await api.addMailNote(email.id, { title: email.subject || "", body })
      await reload(email.id)
      setDraft("")
      setOpen(false)
      toast.success(t("emailDetail.noteAdded"))
    } catch {
      toast.error(t("emailDetail.noteFailed"))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (id: string) => {
    if (!email) return
    try {
      await api.deleteNote(id)
      await reload(email.id)
    } catch {
      toast.error(t("emailDetail.noteFailed"))
    }
  }

  return { draft, setDraft, open, setOpen, busy, add, remove }
}

type NotesEditor = ReturnType<typeof useNotes>

// useInviteActions answers a meeting invite: accept, tentative or decline, or
// propose a new time to the organizer.
function useInviteActions(email: EmailDetail | null, invite: MeetingInvite | null) {
  const { t } = useI18n()
  const [rsvpStatus, setRsvpStatus] = useState<string | null>(null)
  const [rsvpBusy, setRsvpBusy] = useState(false)
  // Propose-new-time: a dialog where the invitee picks a proposed start/end and
  // emails a METHOD:COUNTER iTIP to the organizer.
  const [proposeOpen, setProposeOpen] = useState(false)
  const [proposeStart, setProposeStart] = useState("")
  const [proposeEnd, setProposeEnd] = useState("")
  const [proposeBusy, setProposeBusy] = useState(false)

  // handleRsvp responds to a meeting invite. Accept/tentative add the event to
  // the user's calendar; decline removes it.
  const handleRsvp = async (response: "accept" | "tentative" | "decline") => {
    if (!email) return
    setRsvpBusy(true)
    try {
      await api.rsvp(email.id, response)
      setRsvpStatus(response)
      const messages: Record<string, string> = {
        accept: t("emailDetail.addedToCalendar"),
        tentative: t("emailDetail.markedTentative"),
        decline: t("emailDetail.removedFromCalendar"),
      }
      toast.success(messages[response])
    } catch {
      toast.error(t("emailDetail.failedToRsvp"))
    } finally {
      setRsvpBusy(false)
    }
  }

  // openPropose prefills the propose-new-time dialog with the invite's window.
  const openPropose = () => {
    if (!invite) return
    const range = proposeWindow(invite)
    setProposeStart(range.start)
    setProposeEnd(range.end)
    setProposeOpen(true)
  }

  // handleProposeTime emails the counter-proposal to the organizer.
  const handleProposeTime = async () => {
    if (!email || !proposeStart) return
    setProposeBusy(true)
    try {
      const range = proposalRange(proposeStart, proposeEnd)
      await api.proposeTime(email.id, range.start, range.end)
      toast.success(t("emailDetail.proposedTime"))
      setProposeOpen(false)
    } catch {
      toast.error(t("emailDetail.failedToPropose"))
    } finally {
      setProposeBusy(false)
    }
  }

  return {
    rsvpStatus, rsvpBusy, handleRsvp, openPropose, handleProposeTime,
    proposeOpen, setProposeOpen, proposeStart, setProposeStart, proposeEnd, setProposeEnd, proposeBusy,
  }
}

type InviteActions = ReturnType<typeof useInviteActions>

// useInlineReply sends a quick reply from the box under the message, with no
// compose round-trip.
function useInlineReply(email: EmailDetail | null) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [body, setBody] = useState("")
  const [busy, setBusy] = useState(false)

  const send = async () => {
    if (!email || !body.trim()) return
    setBusy(true)
    try {
      await api.sendMail({ to: [email.fromEmail], subject: replySubject(email.subject), body, is_html: false })
      toast.success(t("emailDetail.replySent"))
      setBody("")
      setOpen(false)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("emailDetail.replyFailed"))
    } finally {
      setBusy(false)
    }
  }

  return { open, setOpen, body, setBody, busy, send }
}

type InlineReplyState = ReturnType<typeof useInlineReply>

// copyToClipboard copies text and confirms with the given key.
async function copyToClipboard(t: TFunc, text: string, okKey: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t(okKey))
  } catch {
    toast.error(t("emailDetail.copyFailed"))
  }
}

// useMessageActions holds the actions the toolbar and the header take on the
// open message.
function useMessageActions(email: EmailDetail | null, setEmail: (email: EmailDetail) => void, invite: MeetingInvite | null, omitOriginal: boolean) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { user } = useAuth()
  // Keep the shared inbox state (sidebar badge, header notifications) in sync
  // with read/flag/label/delete actions taken in the reading view.
  const { patchInbox, removeFromInbox } = useMailbox()

  // run guards an action on an open message and reports its failure.
  const run = async (failKey: string, action: (m: EmailDetail) => Promise<void>) => {
    if (!email) return
    try {
      await action(email)
    } catch {
      toast.error(t(failKey))
    }
  }

  const handleDelete = () => run("emailDetail.failedToDelete", async (m) => {
    await api.deleteMail(m.id)
    removeFromInbox([m.id])
    toast.success(t("emailDetail.movedToTrash"))
    navigate("/inbox")
  })

  const handleRecall = async () => {
    if (!email || !window.confirm(t("emailDetail.recallConfirm"))) return
    await run("emailDetail.recallFailed", async (m) => {
      const outcome = recallOutcome(await api.recallMail(m.id))
      toast[outcome.level](t(outcome.key, outcome.params))
    })
  }

  const handleRecover = () => run("emailDetail.recoverFailed", async (m) => {
    const res = await api.recoverMail(m.id)
    toast.success(t("emailDetail.recovered", { folder: res.folder }))
    navigate("/inbox")
  })

  // quoteLabels resolves the localized strings the query-string builders need,
  // so those stay free of the i18n context and testable on their own.
  const quoteLabels = (m: EmailDetail): QuoteLabels => ({
    replyHeader: t("emailDetail.replyQuoteHeader", { sender: m.from, date: m.date }),
    forwardedMessage: t("emailDetail.forwardedMessage"),
    from: t("common.from"),
    date: t("common.date"),
    subject: t("common.subject"),
    to: t("common.to"),
  })

  const handleReply = () => {
    if (email) navigate(`/compose?${replyParams(email, quoteLabels(email), omitOriginal).toString()}`)
  }
  const handleReplyAll = () => {
    if (email) navigate(`/compose?${replyAllParams(email, quoteLabels(email), user?.email, omitOriginal).toString()}`)
  }
  const handleForward = () => {
    if (email) navigate(`/compose?${forwardParams(email, quoteLabels(email)).toString()}`)
  }

  const handleMarkUnread = () => run("emailDetail.failedToMarkUnread", async (m) => {
    await api.setFlag(m.id, "\\Seen", false)
    patchInbox([m.id], { read: false })
    toast.success(t("emailDetail.markedUnread"))
    navigate("/inbox")
  })

  // handleFollowup sets the message's follow-up flag: a coloured flag with an
  // optional due date, mark-complete, or clear. It ports the old webmail's rich
  // follow-up beyond the plain \Flagged star (the API call also syncs \Flagged).
  const handleFollowup = (action: FollowupAction, color?: number, due?: string) => run("emailDetail.failedToUpdateFollowUp", async (m) => {
    await api.setFollowup(m.id, action, color, due)
    setEmail({ ...m, ...followupPatch(m, action, color, due) })
    patchInbox([m.id], { starred: action === "flag" })
    toast.success(t(FOLLOWUP_TOAST_KEYS[action]))
  })

  // saveLabels persists the full label set and updates state on success.
  const saveLabels = async (next: string[]) => {
    if (!email) return
    setEmail({ ...email, labels: next })
    try {
      await api.setMailLabels(email.id, next)
      patchInbox([email.id], { labels: next })
    } catch {
      setEmail(email)
      toast.error(t("emailDetail.failedToUpdateLabels"))
    }
  }

  const addLabel = (value: string) => {
    if (!email || !value || email.labels.includes(value)) return
    void saveLabels([...email.labels, value])
  }

  const removeLabel = (label: string) => {
    if (email) void saveLabels(email.labels.filter((l) => l !== label))
  }

  const exportEML = () => run("emailDetail.exportFailed", (m) =>
    downloadBlob(`/api/v1/mail/export?id=${encodeURIComponent(m.id)}`, (m.subject || "message") + ".eml"))

  // exportICS downloads the message's embedded meeting invite as an .ics file
  // (reference mail "Export as" ICS). Only offered when the message is an
  // invite; the backend serves the calendar part verbatim to keep the iTIP
  // METHOD intact.
  const exportICS = () => run("emailDetail.exportFailed", (m) =>
    downloadBlob(`/api/v1/mail/export-ics?id=${encodeURIComponent(m.id)}`, (invite?.summary || m.subject || "invite") + ".ics"))

  const handleMove = (folder: string, label: string) => run("emailDetail.failedToMove", async (m) => {
    await api.moveMail(m.id, folder)
    toast.success(t("emailDetail.movedTo", { folder: label }))
    navigate("/inbox")
  })

  // handleCopy copies the open message into another folder, leaving the original
  // in place so the reader stays valid (no navigation, unlike move).
  const handleCopy = (folder: string, label: string) => run("emailDetail.copyFailed", async (m) => {
    await api.copyMail(m.id, folder)
    toast.success(t("emailDetail.copiedTo", { folder: label }))
  })

  const copyAddr = (addr: string) => copyToClipboard(t, addr, "emailDetail.emailCopied")
  const copyAddrs = (addrs: string[]) => copyToClipboard(t, addrs.join(", "), "emailDetail.recipientsCopied")

  return {
    handleDelete, handleRecall, handleRecover, handleReply, handleReplyAll, handleForward, handleMarkUnread,
    handleFollowup, addLabel, removeLabel, exportEML, exportICS, handleMove, handleCopy, copyAddr, copyAddrs,
  }
}

type MessageActions = ReturnType<typeof useMessageActions>

// useReaderShortcuts binds the reading-view shortcuts, gated by the shortcut
// mode: r/a (reply/reply-all) are basic; F/#/U (forward/delete/mark-unread) are
// extended. Ignored while typing, and fully off when the mode is "off".
function useReaderShortcuts(actions: MessageActions) {
  useEffect(() => {
    const handlers: Record<ReaderAction, () => void> = {
      reply: actions.handleReply,
      replyAll: actions.handleReplyAll,
      forward: actions.handleForward,
      delete: () => void actions.handleDelete(),
      markUnread: () => void actions.handleMarkUnread(),
    }
    const onKey = (e: KeyboardEvent) => {
      const action = readerShortcut(e, getShortcutMode())
      if (!action) return
      e.preventDefault()
      handlers[action]()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  })
}

// EmailDetailPage renders a single message. It reads the id from the route, or
// from an `id` prop when embedded in the inbox preview pane (no navigation).
export function EmailDetailPage({ id: propId, embedded }: { id?: string; embedded?: boolean } = {}) {
  const params = useParams()
  const id = propId ?? params.id
  const prefs = useReaderPrefs()
  const message = useMessage(id)
  const { email, setEmail, invite } = message
  const actions = useMessageActions(email, setEmail, invite, prefs.omitOriginal)
  const attachments = useAttachments(email)
  const notes = useNotes(email, message.setNotes)
  const inviteActions = useInviteActions(email, invite)
  const reply = useInlineReply(email)
  const [itemDataOpen, setItemDataOpen] = useState(false)
  useReaderShortcuts(actions)

  if (message.loading) return <div className="space-y-4"><LoadingSpinner /></div>
  if (!email) return <div className="space-y-4"><MessageNotFound /></div>
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <PrimaryActions email={email} id={id} embedded={embedded} actions={actions} onReply={() => reply.setOpen((v) => !v)} />
        <SecondaryActions
          email={email}
          actions={actions}
          prefs={prefs}
          hasInvite={invite !== null}
          onItemData={() => setItemDataOpen((v) => !v)}
        />
      </div>
      <div className="rounded-lg border bg-card">
        <MessageHeader email={email} categoryColors={prefs.categoryColors} actions={actions} />
        <NotesPanel notes={message.notes} editor={notes} />
        {invite && <InvitePanel invite={invite} actions={inviteActions} />}
        <ProposeDialog actions={inviteActions} />
        <Separator className="my-6" />
        <div className="px-6 pb-6">
          <SmimeBanner email={email} />
          <MessageBody content={email.content} showImages={message.showImages} onShowImages={() => message.setShowImages(true)} />
        </div>
        {email.attachments.length > 0 && <AttachmentList email={email} prefs={prefs} attachments={attachments} />}
        {reply.open && <InlineReply email={email} reply={reply} />}
        {itemDataOpen && <ItemDataPanel email={email} onClose={() => setItemDataOpen(false)} />}
      </div>
    </div>
  )
}

function LoadingSpinner() {
  return (
    <div className="flex items-center justify-center py-16">
      <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
    </div>
  )
}

function MessageNotFound() {
  const { t } = useI18n()
  const navigate = useNavigate()
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <h3 className="mt-4 text-lg font-semibold">{t("emailDetail.notFound")}</h3>
      <p className="text-sm text-muted-foreground">{t("emailDetail.notFoundDescription")}</p>
      <Button className="mt-4" onClick={() => navigate("/inbox")}>{t("emailDetail.backToInbox")}</Button>
    </div>
  )
}

// PrimaryActions is the toolbar's left group: back, reply, reply all, forward,
// pop out, and recall or recover where the folder allows it.
function PrimaryActions({ email, id, embedded, actions, onReply }: {
  email: EmailDetail
  id: string | undefined
  embedded: boolean | undefined
  actions: MessageActions
  onReply: () => void
}) {
  const { t } = useI18n()
  const navigate = useNavigate()
  return (
    <div className="flex items-center gap-1">
      {!embedded && (
        <Button variant="ghost" size="icon" onClick={() => navigate(-1)} title={t("common.back")}>
          <ArrowLeft className="h-5 w-5" />
        </Button>
      )}
      <Button variant="ghost" size="sm" onClick={onReply} title={t("common.reply")}>
        <Reply className="h-4 w-4 mr-1" />
        {t("common.reply")}
      </Button>
      <Button variant="ghost" size="sm" onClick={actions.handleReplyAll} title={t("common.replyAll")}>
        <ReplyAll className="h-4 w-4 mr-1" />
        {t("common.replyAll")}
      </Button>
      <Button variant="ghost" size="sm" onClick={actions.handleForward} title={t("common.forward")}>
        <Forward className="h-4 w-4 mr-1" />
        {t("common.forward")}
      </Button>
      <Button variant="ghost" size="icon" onClick={() => window.open(`/email/${id}`, "_blank", "noopener")} title={t("emailDetail.popOut")}>
        <ExternalLink className="h-4 w-4" />
      </Button>
      {email.folder === "Sent" && (
        <Button variant="ghost" size="sm" onClick={actions.handleRecall} title={t("emailDetail.recall")}>
          <Undo2 className="h-4 w-4 mr-1" />
          {t("emailDetail.recall")}
        </Button>
      )}
      {email.folder === "Recoverable Items" && (
        <Button variant="ghost" size="sm" onClick={actions.handleRecover} title={t("emailDetail.recover")}>
          <RotateCcw className="h-4 w-4 mr-1" />
          {t("emailDetail.recover")}
        </Button>
      )}
    </div>
  )
}

function IconAction({ title, onClick, children, className }: { title: string; onClick: () => void; children: ReactNode; className?: string }) {
  return (
    <Button variant="ghost" size="icon" className={className} onClick={onClick} title={title}>
      {children}
    </Button>
  )
}

// SecondaryActions is the toolbar's right group: follow-up, mark unread, move,
// copy, export, item data, print, source, headers and delete.
function SecondaryActions({ email, actions, prefs, hasInvite, onItemData }: {
  email: EmailDetail
  actions: MessageActions
  prefs: ReaderPrefs
  hasInvite: boolean
  onItemData: () => void
}) {
  const { t } = useI18n()
  const openRaw = (kind: "source" | "headers") => window.open(`/api/v1/mail/${kind}?id=${encodeURIComponent(email.id)}`, "_blank")
  return (
    <div className="flex items-center gap-1">
      <FollowupMenu email={email} onFollowup={actions.handleFollowup} />
      <IconAction title={t("common.markUnread")} onClick={actions.handleMarkUnread}>
        <Mail className="h-5 w-5" />
      </IconAction>
      <FolderMenu title={t("emailDetail.moveToFolder")} icon={<FolderInput className="h-5 w-5" />} custom={[]} onPick={actions.handleMove} />
      <FolderMenu title={t("emailDetail.copyToFolder")} icon={<Copy className="h-5 w-5" />} custom={prefs.copyFolders} onPick={actions.handleCopy} />
      <IconAction title={t("emailDetail.exportEML")} onClick={actions.exportEML}>
        <Download className="h-5 w-5" />
      </IconAction>
      {hasInvite && (
        <IconAction title={t("emailDetail.exportICS")} onClick={actions.exportICS}>
          <CalendarArrowDown className="h-5 w-5" />
        </IconAction>
      )}
      {prefs.showItemData && (
        <IconAction title={t("emailDetail.itemData")} onClick={onItemData}>
          <Braces className="h-5 w-5" />
        </IconAction>
      )}
      <IconAction title={t("emailDetail.print")} onClick={() => window.print()}>
        <Printer className="h-5 w-5" />
      </IconAction>
      <IconAction title={t("emailDetail.viewSource")} onClick={() => openRaw("source")}>
        <Code className="h-5 w-5" />
      </IconAction>
      <IconAction title={t("emailDetail.viewHeaders")} onClick={() => openRaw("headers")}>
        <FileText className="h-5 w-5" />
      </IconAction>
      <IconAction title={t("common.delete")} className="text-destructive" onClick={actions.handleDelete}>
        <Trash2 className="h-5 w-5" />
      </IconAction>
    </div>
  )
}

// followupFill is the flag icon's fill: the flag's colour while it is set.
function followupFill(email: EmailDetail): string {
  if (email.followupStatus !== 2) return ""
  return FOLLOWUP_COLORS.find((c) => c.value === email.followupColor)?.fill ?? "fill-red-500 text-red-500"
}

function FollowupMenu({ email, onFollowup }: {
  email: EmailDetail
  onFollowup: (action: FollowupAction, color?: number, due?: string) => void
}) {
  const { t } = useI18n()
  const flagged = email.followupStatus === 2
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" title={t("emailDetail.followUp")} aria-pressed={flagged}>
          <Flag className={`h-5 w-5 ${followupFill(email)}`} />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <div className="flex gap-1 px-2 py-1.5">
          {FOLLOWUP_COLORS.map((c) => (
            <button
              key={c.value}
              type="button"
              title={t(`emailDetail.${c.key}`)}
              onClick={() => onFollowup("flag", c.value)}
              className={`flex h-7 w-7 items-center justify-center rounded hover:bg-accent ${
                flagged && email.followupColor === c.value ? "ring-2 ring-ring" : ""
              }`}
            >
              <Flag className={`h-4 w-4 ${c.fill}`} />
            </button>
          ))}
        </div>
        <DropdownMenuSeparator />
        <div className="px-2 py-1.5">
          <label className="text-xs text-muted-foreground">{t("emailDetail.followUpDue")}</label>
          <Input
            type="datetime-local"
            className="mt-1 h-8"
            defaultValue={toDatetimeLocal(email.followupDue)}
            onChange={(e) =>
              e.target.value &&
              onFollowup("flag", email.followupColor || 6, new Date(e.target.value).toISOString())
            }
          />
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => onFollowup("complete")}>
          <Check className="mr-2 h-4 w-4" />
          {t("emailDetail.markComplete")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onFollowup("clear")}>
          <X className="mr-2 h-4 w-4" />
          {t("emailDetail.clearFollowUp")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// FolderMenu offers the fixed folder targets plus any custom folders.
function FolderMenu({ title, icon, custom, onPick }: {
  title: string
  icon: ReactNode
  custom: string[]
  onPick: (folder: string, label: string) => void
}) {
  const { t } = useI18n()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" title={title}>
          {icon}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {FOLDER_TARGETS.map((target) => (
          <DropdownMenuItem key={target.folder} onClick={() => onPick(target.folder, t(target.key))}>{t(target.key)}</DropdownMenuItem>
        ))}
        {custom.length > 0 && <DropdownMenuSeparator />}
        {custom.map((f) => (
          <DropdownMenuItem key={f} onClick={() => onPick(f, f)}>{f}</DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// MessageHeader shows the subject, the sender, the recipients, the date and
// the category labels.
function MessageHeader({ email, categoryColors, actions }: {
  email: EmailDetail
  categoryColors: Record<string, string>
  actions: MessageActions
}) {
  const { t } = useI18n()
  const rows = [
    { label: t("common.to"), addrs: email.to, names: email.toNames },
    { label: t("emailDetail.cc"), addrs: email.cc ?? [], names: email.ccNames },
  ].filter((row) => row.addrs.length > 0)
  return (
    <div className="p-6 pb-0">
      <h1 className="text-2xl font-semibold leading-tight">{email.subject}</h1>
      <div className="flex items-start gap-4 mt-6">
        <Avatar className="h-12 w-12 ring-2 ring-primary/10">
          <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold text-lg">
            {senderInitials(email.from)}
          </AvatarFallback>
        </Avatar>
        <div className="flex-1 min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="font-semibold text-lg">{email.from}</span>
            <span className="text-sm text-muted-foreground">
              &lt;{email.fromEmail}&gt;
            </span>
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground transition-colors"
              title={t("emailDetail.copyEmail")}
              onClick={() => actions.copyAddr(email.fromEmail)}
            >
              <Copy className="h-3.5 w-3.5" />
            </button>
          </div>
          {rows.map((row) => (
            <RecipientRow key={row.label} label={row.label} addrs={row.addrs} names={row.names} actions={actions} />
          ))}
          <div className="mt-1 text-sm text-muted-foreground">{formatAbsolute(email.date)}</div>
          <LabelBar labels={email.labels} categoryColors={categoryColors} actions={actions} />
        </div>
      </div>
    </div>
  )
}

function RecipientRow({ label, addrs, names, actions }: {
  label: string
  addrs: string[]
  names: string[] | undefined
  actions: MessageActions
}) {
  const { t } = useI18n()
  return (
    <div className="mt-1 text-sm text-muted-foreground">
      <span className="font-medium text-foreground">{label}:</span>{" "}
      {addrs.map((addr, i) => {
        const nm = names?.[i]
        return (
          <span key={addr + i} className="inline-flex items-center gap-1">
            {i > 0 && ", "}
            <span>{nm ? `${nm} <${addr}>` : addr}</span>
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground transition-colors align-middle"
              title={t("emailDetail.copyEmail")}
              onClick={() => actions.copyAddr(addr)}
            >
              <Copy className="h-3 w-3" />
            </button>
          </span>
        )
      })}
      {addrs.length > 1 && (
        <button
          type="button"
          className="ml-1 text-muted-foreground hover:text-foreground transition-colors align-middle"
          title={t("emailDetail.copyAllRecipients")}
          onClick={() => actions.copyAddrs(addrs)}
        >
          <Copy className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  )
}

// LabelBar lists the message's category labels and adds one inline.
function LabelBar({ labels, categoryColors, actions }: {
  labels: string[]
  categoryColors: Record<string, string>
  actions: MessageActions
}) {
  const { t } = useI18n()
  const [editing, setEditing] = useState(false)
  const [newLabel, setNewLabel] = useState("")
  const commit = () => {
    actions.addLabel(newLabel.trim())
    setNewLabel("")
    setEditing(false)
  }
  return (
    <div className="mt-2 flex flex-wrap items-center gap-1.5">
      <Tag className="h-3.5 w-3.5 text-muted-foreground" />
      {labels.map((label) => (
        <LabelBadge key={label} label={label} color={categoryColors[label.toLowerCase()]} onRemove={() => actions.removeLabel(label)} />
      ))}
      {editing ? (
        <Input
          autoFocus
          value={newLabel}
          onChange={(e) => setNewLabel(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") commit()
            if (e.key === "Escape") { setNewLabel(""); setEditing(false) }
          }}
          placeholder={t("emailDetail.labelPlaceholder")}
          className="h-6 w-28 text-xs"
        />
      ) : (
        <button
          onClick={() => setEditing(true)}
          className="flex items-center gap-1 rounded border border-dashed px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground"
        >
          <Plus className="h-3 w-3" />
          {t("emailDetail.addLabel")}
        </button>
      )}
    </div>
  )
}

function LabelBadge({ label, color, onRemove }: { label: string; color: string | undefined; onRemove: () => void }) {
  const { t } = useI18n()
  return (
    <Badge variant="secondary" className="gap-1" style={color ? { backgroundColor: color, color: "#fff" } : undefined}>
      {label}
      <button
        onClick={onRemove}
        className={color ? "opacity-80 hover:opacity-100" : "text-muted-foreground hover:text-destructive"}
        aria-label={t("emailDetail.removeLabel", { label })}
      >
        <X className="h-3 w-3" />
      </button>
    </Badge>
  )
}

// NotesPanel shows the sticky notes attached to this mail.
function NotesPanel({ notes, editor }: { notes: Note[]; editor: NotesEditor }) {
  const { t } = useI18n()
  return (
    <div className="mx-6 mt-4 rounded-lg border p-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <StickyNote className="h-4 w-4" />
          {t("emailDetail.notes")}
        </div>
        <Button size="sm" variant="ghost" onClick={() => editor.setOpen((open) => !open)} disabled={editor.busy}>
          <Plus className="mr-1 h-3.5 w-3.5" />
          {t("emailDetail.addNote")}
        </Button>
      </div>
      {notes.length === 0 && !editor.open && (
        <p className="mt-2 text-sm text-muted-foreground">{t("emailDetail.noNotes")}</p>
      )}
      {notes.length > 0 && (
        <ul className="mt-2 space-y-2">
          {notes.map((n) => (
            <li key={n.id} className="flex items-start justify-between gap-2 rounded-md bg-muted/50 p-2">
              {/* Note bodies are plain text; React escapes them. */}
              <span className="whitespace-pre-wrap text-sm">{n.body}</span>
              <Button
                size="icon"
                variant="ghost"
                className="h-6 w-6 shrink-0"
                onClick={() => editor.remove(n.id)}
                title={t("emailDetail.deleteNote")}
              >
                <X className="h-3.5 w-3.5" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      {editor.open && <NoteEditor editor={editor} />}
    </div>
  )
}

function NoteEditor({ editor }: { editor: NotesEditor }) {
  const { t } = useI18n()
  return (
    <div className="mt-2 space-y-2">
      <Textarea
        value={editor.draft}
        onChange={(e) => editor.setDraft(e.target.value)}
        placeholder={t("emailDetail.notePlaceholder")}
        rows={3}
      />
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="outline" onClick={() => { editor.setOpen(false); editor.setDraft("") }}>
          {t("common.cancel")}
        </Button>
        <Button size="sm" onClick={editor.add} disabled={editor.busy || !editor.draft.trim()}>
          {t("common.save")}
        </Button>
      </div>
    </div>
  )
}

function inviteTime(start: string): string {
  const d = new Date(start)
  return isNaN(d.getTime()) ? start : d.toLocaleString([], withTz())
}

// InvitePanel shows a meeting invitation with its RSVP actions.
function InvitePanel({ invite, actions }: { invite: MeetingInvite; actions: InviteActions }) {
  const { t } = useI18n()
  const rsvp = (response: "accept" | "tentative" | "decline", icon: ReactNode, label: string) => (
    <Button
      size="sm"
      variant={actions.rsvpStatus === response ? "default" : "outline"}
      onClick={() => actions.handleRsvp(response)}
      disabled={actions.rsvpBusy}
    >
      {icon}
      {label}
    </Button>
  )
  return (
    <div className="mx-6 mt-4 rounded-lg border border-primary/30 bg-primary/5 p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <CalendarCheck className="h-4 w-4 text-primary" />
        {t("emailDetail.meetingInvitation")}
      </div>
      <div className="mt-2 space-y-1 text-sm">
        {invite.summary && <div className="font-medium">{invite.summary}</div>}
        {invite.start && <div className="text-muted-foreground">{inviteTime(invite.start)}</div>}
        {invite.location && <div className="text-muted-foreground">{invite.location}</div>}
        {invite.organizer && (
          <div className="text-muted-foreground">{t("emailDetail.organizer", { name: invite.organizer })}</div>
        )}
      </div>
      <div className="mt-3 flex items-center gap-2">
        {rsvp("accept", <Check className="mr-1 h-4 w-4" />, t("emailDetail.accept"))}
        {rsvp("tentative", <HelpCircle className="mr-1 h-4 w-4" />, t("emailDetail.tentative"))}
        {rsvp("decline", <X className="mr-1 h-4 w-4" />, t("emailDetail.decline"))}
        <Button size="sm" variant="outline" onClick={actions.openPropose} disabled={actions.rsvpBusy}>
          {t("emailDetail.proposeNewTime")}
        </Button>
      </div>
    </div>
  )
}

function ProposeDialog({ actions }: { actions: InviteActions }) {
  const { t } = useI18n()
  return (
    <Dialog open={actions.proposeOpen} onOpenChange={actions.setProposeOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("emailDetail.proposeNewTime")}</DialogTitle>
          <DialogDescription>{t("emailDetail.proposeNewTimeHint")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2">
          <div className="space-y-1">
            <Label htmlFor="pt-start">{t("calendar.start")}</Label>
            <Input id="pt-start" type="datetime-local" value={actions.proposeStart} onChange={(e) => actions.setProposeStart(e.target.value)} />
          </div>
          <div className="space-y-1">
            <Label htmlFor="pt-end">{t("calendar.end")}</Label>
            <Input id="pt-end" type="datetime-local" value={actions.proposeEnd} onChange={(e) => actions.setProposeEnd(e.target.value)} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => actions.setProposeOpen(false)} disabled={actions.proposeBusy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={actions.handleProposeTime} disabled={actions.proposeBusy || !actions.proposeStart}>
            {t("emailDetail.sendProposal")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// smimeText is the S/MIME banner's text: encrypted, signed, or both.
function smimeText(t: TFunc, email: EmailDetail): string {
  const parts: string[] = []
  if (email.smimeEncrypted) parts.push(t("emailDetail.smimeEncrypted"))
  if (email.smimeSigned) {
    const key = email.smimeVerified ? "emailDetail.smimeSignedVerified" : "emailDetail.smimeSignedUnverified"
    parts.push(t(key, { signer: email.smimeSignedBy || "" }))
  }
  return parts.join(" · ")
}

function SmimeBanner({ email }: { email: EmailDetail }) {
  const { t } = useI18n()
  if (!email.smimeSigned && !email.smimeEncrypted) return null
  const unverified = email.smimeSigned && !email.smimeVerified
  return (
    <div
      className={
        "mb-4 flex items-center gap-2 rounded-md border px-3 py-2 text-sm " +
        (unverified
          ? "border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-200"
          : "border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-700 dark:bg-emerald-950 dark:text-emerald-200")
      }
    >
      <ShieldCheck className="h-4 w-4 shrink-0" />
      <span>{smimeText(t, email)}</span>
    </div>
  )
}

// MessageBody renders the sanitized body, with remote images blocked until
// the reader asks for them.
function MessageBody({ content, showImages, onShowImages }: { content: string; showImages: boolean; onShowImages: () => void }) {
  const { t } = useI18n()
  const { html, blockedRemote } = sanitizeEmailBody(content, !showImages)
  return (
    <>
      {blockedRemote && !showImages && (
        <div className="mb-4 flex items-center justify-between gap-3 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-200">
          <span>{t("emailDetail.imagesBlocked")}</span>
          <Button variant="outline" size="sm" onClick={onShowImages}>
            {t("emailDetail.showImages")}
          </Button>
        </div>
      )}
      <div
        className="prose prose-neutral dark:prose-invert max-w-none prose-headings:font-semibold prose-p:leading-relaxed prose-ul:leading-relaxed whitespace-pre-wrap"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </>
  )
}

function AttachmentList({ email, prefs, attachments }: { email: EmailDetail; prefs: ReaderPrefs; attachments: Attachments }) {
  const { t } = useI18n()
  const count = email.attachments.length
  const selected = attachments.selected.length
  return (
    <div className="border-t px-6 py-4">
      <div className="mb-2 flex items-center justify-between text-sm font-medium text-muted-foreground">
        <div className="flex items-center gap-1.5">
          <Paperclip className="h-4 w-4" />
          {t(count > 1 ? "emailDetail.attachments" : "emailDetail.attachment", { count: String(count) })}
        </div>
        {count > 1 && (
          <button
            onClick={attachments.handleDownloadAll}
            className="flex items-center gap-1 text-xs hover:text-foreground transition-colors"
            title={t("emailDetail.downloadAll")}
          >
            <Download className="h-3.5 w-3.5" />
            {selected > 0 ? t("emailDetail.downloadSelected", { count: String(selected) }) : t("emailDetail.downloadAll")}
          </button>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        {email.attachments.map((att) => (
          <AttachmentItem key={att.index} emailId={email.id} att={att} prefs={prefs} attachments={attachments} />
        ))}
      </div>
    </div>
  )
}

function AttachmentItem({ emailId, att, prefs, attachments }: {
  emailId: string
  att: AttachmentInfo
  prefs: ReaderPrefs
  attachments: Attachments
}) {
  const { t } = useI18n()
  const src = `/api/v1/mail/attachment?id=${encodeURIComponent(emailId)}&index=${att.index}`
  // The server saves rather than renders unless asked, and it only honours the
  // ask for the types it serves as themselves (a PDF or an image). Everything
  // else stays opaque bytes.
  const inlineSrc = `${src}&disposition=inline`
  return (
    <div className="flex flex-col gap-1">
      <button
        draggable
        onPointerEnter={() => void attachments.prepareDrag(att)}
        onDragStart={(e) => attachments.handleDragStart(e, att)}
        onClick={(e) => attachments.handleClick(e, att)}
        className={cn(
          "flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-left text-sm transition-colors",
          attachments.selected.includes(att.index) ? "border-primary bg-primary/10" : "hover:bg-accent/50",
        )}
        title={t("emailDetail.downloadAttachment", { filename: att.filename })}
      >
        <Paperclip className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0">
          <span className="block truncate font-medium">{att.filename}</span>
          <span className="block text-xs text-muted-foreground">{formatFileSize(att.size)}</span>
        </span>
        <Download className="h-4 w-4 shrink-0 text-muted-foreground" />
      </button>
      {/* Open the attachment on its own, for reading a long document without
          the message around it. It is a link rather than a button so the
          browser's own "open in a new window" and "copy address" apply to it. */}
      <a
        href={inlineSrc}
        target="_blank"
        rel="noreferrer"
        className="flex items-center gap-1 self-start text-xs text-muted-foreground hover:text-foreground transition-colors"
        title={t("emailDetail.openAttachment", { filename: att.filename })}
      >
        <ExternalLink className="h-3.5 w-3.5" />
        {t("emailDetail.openInNewTab")}
      </a>
      {prefs.filePreview && <AttachmentPreview att={att} src={src} inlineSrc={inlineSrc} prefs={prefs} attachments={attachments} />}
    </div>
  )
}

// AttachmentPreview previews an image or a PDF inline. A PDF above the size cap
// waits for a click.
function AttachmentPreview({ att, src, inlineSrc, prefs, attachments }: {
  att: AttachmentInfo
  src: string
  inlineSrc: string
  prefs: ReaderPrefs
  attachments: Attachments
}) {
  const { t } = useI18n()
  if (att.contentType?.startsWith("image/")) {
    return <img src={inlineSrc} alt={att.filename} className="max-h-48 w-auto rounded border" loading="lazy" />
  }
  if (att.contentType !== "application/pdf") return null
  if (att.size > prefs.previewMaxBytes && !attachments.previewRequested.includes(att.index)) {
    return (
      <button
        onClick={() => attachments.requestPreview(att.index)}
        className="rounded-lg border bg-card px-3 py-2 text-left text-sm hover:bg-accent/50 transition-colors"
      >
        {t("emailDetail.previewLarge", { size: formatFileSize(att.size) })}
      </button>
    )
  }
  return (
    <object
      data={`${inlineSrc}#${pdfZoomFragment(prefs.pdfZoom)}`}
      type="application/pdf"
      className="h-96 w-full rounded border"
      aria-label={att.filename}
    >
      <a href={src} className="text-sm text-primary underline" download={att.filename}>
        {att.filename}
      </a>
    </object>
  )
}

function InlineReply({ email, reply }: { email: EmailDetail; reply: InlineReplyState }) {
  const { t } = useI18n()
  return (
    <div className="mt-4 rounded-lg border bg-card p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-sm text-muted-foreground">{t("emailDetail.inlineReplyTo", { who: email.fromEmail })}</span>
        <button type="button" className="text-muted-foreground hover:text-foreground" onClick={() => reply.setOpen(false)}>×</button>
      </div>
      <Textarea
        value={reply.body}
        onChange={(e) => reply.setBody(e.target.value)}
        rows={4}
        placeholder={t("emailDetail.replyPlaceholder")}
        className="mb-2"
      />
      <div className="flex justify-end gap-2">
        <Button variant="outline" size="sm" onClick={() => reply.setOpen(false)} disabled={reply.busy}>{t("common.cancel")}</Button>
        <Button size="sm" onClick={reply.send} disabled={reply.busy || !reply.body.trim()}>
          {reply.busy ? t("common.sending") : t("common.send")}
        </Button>
      </div>
    </div>
  )
}

function ItemDataPanel({ email, onClose }: { email: EmailDetail; onClose: () => void }) {
  const { t } = useI18n()
  return (
    <div className="mt-4 rounded-lg border bg-muted/30 p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-xs font-medium uppercase text-muted-foreground">{t("emailDetail.itemData")}</span>
        <button type="button" className="text-muted-foreground hover:text-foreground" onClick={onClose}>×</button>
      </div>
      <div className="mb-2 text-xs">
        <span className="font-medium">{t("emailDetail.objectId")}:</span>{" "}
        <code className="break-all">{email.id}</code>
      </div>
      <div className="mb-1 text-xs font-medium">{t("emailDetail.properties")}</div>
      <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(email, null, 2)}</pre>
    </div>
  )
}
