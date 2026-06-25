import { useState, useEffect } from "react"
import { useParams, useNavigate } from "react-router-dom"
import {
  ArrowLeft,
  Trash2,
  Reply,
  ReplyAll,
  Forward,
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
  Printer,
  Code,
  ShieldCheck,
  Undo2,
  RotateCcw,
  Copy,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import { sanitizeEmailBody } from "@/utils/sanitize"
import api from "@/utils/api"
import type { MeetingInvite, AttachmentInfo } from "@/utils/api"
import * as smimeStore from "@/utils/smime"
import { formatAbsolute, withTz } from "@/utils/date"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"

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

// toDatetimeLocal converts an RFC3339 instant to the "YYYY-MM-DDTHH:mm" value a
// native datetime-local input expects, in the browser's local zone.
function toDatetimeLocal(iso?: string): string {
  if (!iso) return ""
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

interface EmailDetail {
  id: string
  from: string
  fromEmail: string
  to: string[]
  toNames: string[]
  subject: string
  date: string
  content: string
  flagged: boolean
  followupStatus?: number
  followupColor?: number
  followupDue?: string
  labels: string[]
  attachments: AttachmentInfo[]
  folder: string
  smimeSigned?: boolean
  smimeEncrypted?: boolean
  smimeVerified?: boolean
  smimeSignedBy?: string
}

export function EmailDetailPage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { t } = useI18n()
  const { user } = useAuth()
  // Keep the shared inbox state (sidebar badge, header notifications) in sync
  // with read/flag/label/delete actions taken in the reading view.
  const { patchInbox, removeFromInbox } = useMailbox()
  const [email, setEmail] = useState<EmailDetail | null>(null)
  const [loading, setLoading] = useState(true)
  // Remote images are blocked on open (tracking-pixel protection); the reader
  // offers a one-time "show images" once the user chooses to load them.
  const [showImages, setShowImages] = useState(false)
  const [newLabel, setNewLabel] = useState("")
  const [labelEditing, setLabelEditing] = useState(false)
  const [invite, setInvite] = useState<MeetingInvite | null>(null)
  const [rsvpStatus, setRsvpStatus] = useState<string | null>(null)
  const [rsvpBusy, setRsvpBusy] = useState(false)
  // Category name → color, so labels render with their configured color.
  const [categoryColors, setCategoryColors] = useState<Record<string, string>>({})
  // Custom folders offered as copy-to targets, alongside the fixed built-ins.
  const [copyFolders, setCopyFolders] = useState<string[]>([])

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
    return () => { cancelled = true }
  }, [])

  // Load the message by id (the backend resolves it across all folders).
  useEffect(() => {
    setShowImages(false) // re-block remote images for each newly opened message
    const loadEmail = async () => {
      if (!id) {
        setLoading(false)
        return
      }
      try {
        setLoading(true)
        const result = await api.getMessage(id)
        if (result && result.id) {
          // The API returns a bare sender address (result.from) and a resolved
          // display name (result.fromName, "" when unknown); recipients come as
          // bare addresses (result.to) with names in result.toNames (same index).
          const fromEmail = result.from
          const fromName = result.fromName || result.from
          // A safe-listed sender's remote images are trusted, so load them without
          // the one-time prompt (the server computed the allowlist match).
          if (result.senderTrusted) setShowImages(true)
          // S/MIME encrypted: a browser-mode reader (key in this browser) decrypts
          // client-side; a server-mode reader's message was already decrypted server-side.
          let content = result.body
          // S/MIME signal: the server fills these for cleartext-signed mail, but for
          // a browser-mode encrypted message it never sees the decrypted signed inner,
          // so the client overrides them after decrypting and verifying locally.
          let smimeSigned = result.smimeSigned
          let smimeVerified = result.smimeVerified
          let smimeSignedBy = result.smimeSignedBy
          if (result.smimeEncrypted && (await smimeStore.hasIdentity())) {
            if (smimeStore.isUnlocked()) {
              try {
                const inner = smimeStore.decryptMime(await api.getMessageRaw(result.id))
                const extracted = smimeStore.extractMimeBody(inner)
                content = extracted.html
                  ? extracted.body
                  : extracted.body.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
                // Verify the inner signature in the browser; posting the decrypted
                // signed content to the server would leak the plaintext.
                const verdict = smimeStore.verifyMime(inner)
                if (verdict) {
                  smimeSigned = true
                  smimeVerified = verdict.verified
                  smimeSignedBy = verdict.signedBy
                }
              } catch {
                content = `<p>${t("emailDetail.smimeDecryptFailed")}</p>`
              }
            } else {
              content = `<p>${t("emailDetail.smimeLockedBody")}</p>`
            }
          }
          setEmail({
            id: result.id,
            from: fromName,
            fromEmail,
            to: result.to ?? [],
            toNames: result.toNames ?? [],
            subject: result.subject,
            date: result.date,
            content,
            flagged: !!result.starred,
            followupStatus: result.followupStatus,
            followupColor: result.followupColor,
            followupDue: result.followupDue,
            labels: result.labels ?? [],
            attachments: result.attachments ?? [],
            folder: result.folder ?? "",
            smimeSigned,
            smimeEncrypted: result.smimeEncrypted,
            smimeVerified,
            smimeSignedBy,
          })
          // Mark the message read on open (server-side) if it was unread, so
          // the unread count reflects reading — standard mail-client behavior.
          // Fire-and-forget: a failure must not block reading the message.
          if (!result.read) {
            api.setFlag(result.id, "\\Seen", true).catch(() => undefined)
            patchInbox([result.id], { read: true })
          }
          // Detect a meeting invite so we can offer RSVP actions. A failure
          // here must not block reading the message.
          try {
            const inv = await api.getInvite(result.id)
            setInvite(inv.isInvite ? inv : null)
          } catch {
            setInvite(null)
          }
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

  const handleDelete = async () => {
    if (!email) return
    try {
      await api.deleteMail(email.id)
      removeFromInbox([email.id])
      toast.success(t("emailDetail.movedToTrash"))
      navigate("/inbox")
    } catch {
      toast.error(t("emailDetail.failedToDelete"))
    }
  }

  const handleRecall = async () => {
    if (!email) return
    if (!window.confirm(t("emailDetail.recallConfirm"))) return
    try {
      const res = await api.recallMail(email.id)
      if (res.total === 0) {
        toast.info(t("emailDetail.recallNoRecipients"))
      } else if (res.recalled === res.total) {
        toast.success(t("emailDetail.recallAll", { count: String(res.recalled) }))
      } else if (res.recalled === 0) {
        toast.warning(t("emailDetail.recallNone"))
      } else {
        toast.warning(t("emailDetail.recallPartial", { recalled: String(res.recalled), total: String(res.total) }))
      }
    } catch {
      toast.error(t("emailDetail.recallFailed"))
    }
  }

  const handleRecover = async () => {
    if (!email) return
    try {
      const res = await api.recoverMail(email.id)
      toast.success(t("emailDetail.recovered", { folder: res.folder }))
      navigate("/inbox")
    } catch {
      toast.error(t("emailDetail.recoverFailed"))
    }
  }

  const handleReply = () => {
    if (!email) return
    const params = new URLSearchParams({
      replyTo: email.fromEmail,
      subject: email.subject.startsWith("Re: ") ? email.subject : `Re: ${email.subject}`,
    })
    navigate(`/compose?${params.toString()}`)
  }

  const handleReplyAll = () => {
    if (!email) return
    const self = user?.email?.toLowerCase()
    // Other To recipients become Cc, excluding the original sender and ourselves.
    const others = email.to
      .map((t) => {
        const m = t.match(/<([^>]+)>/)
        return (m ? m[1] : t).trim()
      })
      .filter(
        (e) =>
          e &&
          e.toLowerCase() !== self &&
          e.toLowerCase() !== email.fromEmail.toLowerCase()
      )
    const params = new URLSearchParams({
      replyTo: email.fromEmail,
      subject: email.subject.startsWith("Re: ") ? email.subject : `Re: ${email.subject}`,
    })
    if (others.length > 0) params.set("cc", others.join(","))
    navigate(`/compose?${params.toString()}`)
  }

  const handleForward = () => {
    if (!email) return
    const quoted = `\n\n---------- ${t("emailDetail.forwardedMessage")} ----------\n${t("common.from")}: ${email.from} <${email.fromEmail}>\n${t("common.date")}: ${email.date}\n${t("common.subject")}: ${email.subject}\n${t("common.to")}: ${email.to.join(", ")}\n\n${email.content}`
    const params = new URLSearchParams({
      subject: email.subject.startsWith("Fwd: ") ? email.subject : `Fwd: ${email.subject}`,
      body: quoted,
    })
    navigate(`/compose?${params.toString()}`)
  }

  const handleMarkUnread = async () => {
    if (!email) return
    try {
      await api.setFlag(email.id, "\\Seen", false)
      patchInbox([email.id], { read: false })
      toast.success(t("emailDetail.markedUnread"))
      navigate("/inbox")
    } catch {
      toast.error(t("emailDetail.failedToMarkUnread"))
    }
  }

  // handleFollowup sets the message's follow-up flag: a coloured flag with an
  // optional due date, mark-complete, or clear. It ports the old webmail's rich
  // follow-up beyond the plain \Flagged star (the API call also syncs \Flagged).
  const handleFollowup = async (action: "flag" | "complete" | "clear", color?: number, due?: string) => {
    if (!email) return
    try {
      await api.setFollowup(email.id, action, color, due)
      const status = action === "flag" ? 2 : action === "complete" ? 1 : 0
      setEmail({
        ...email,
        flagged: action === "flag",
        followupStatus: status,
        followupColor: action === "flag" ? color ?? email.followupColor : email.followupColor,
        followupDue: action === "flag" ? due ?? email.followupDue : "",
      })
      patchInbox([email.id], { starred: action === "flag" })
      toast.success(
        action === "complete"
          ? t("emailDetail.followUpCompleted")
          : action === "clear"
            ? t("emailDetail.followUpCleared")
            : t("emailDetail.flaggedForFollowUp"),
      )
    } catch {
      toast.error(t("emailDetail.failedToUpdateFollowUp"))
    }
  }

  // saveLabels persists the full label set and updates state on success.
  const saveLabels = async (next: string[]) => {
    if (!email) return
    const prev = email.labels
    setEmail({ ...email, labels: next })
    try {
      await api.setMailLabels(email.id, next)
      patchInbox([email.id], { labels: next })
    } catch {
      setEmail({ ...email, labels: prev })
      toast.error(t("emailDetail.failedToUpdateLabels"))
    }
  }

  const handleAddLabel = () => {
    if (!email) return
    const value = newLabel.trim()
    if (!value || email.labels.includes(value)) {
      setNewLabel("")
      return
    }
    setNewLabel("")
    void saveLabels([...email.labels, value])
  }

  const handleRemoveLabel = (label: string) => {
    if (!email) return
    void saveLabels(email.labels.filter((l) => l !== label))
  }

  // handleRsvp responds to a meeting invite. Accept/tentative add the event to
  // the user's calendar; decline removes it. (The send path is local-only, so
  // the organizer is not emailed a reply.)
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

  const handleDownloadAttachment = async (att: AttachmentInfo) => {
    if (!email) return
    try {
      await api.downloadAttachment(email.id, att.index, att.filename)
    } catch {
      toast.error(t("emailDetail.failedToDownload"))
    }
  }

  // handleDownloadAll fetches every attachment as one .zip. A same-origin anchor
  // carries the session cookie, and the server names the file.
  const handleDownloadAll = () => {
    if (!email) return
    const a = document.createElement("a")
    a.href = `/api/v1/mail/attachments-zip?id=${encodeURIComponent(email.id)}`
    a.download = "attachments.zip"
    document.body.appendChild(a)
    a.click()
    a.remove()
  }

  const handleExportEML = async () => {
    if (!email) return
    try {
      const res = await fetch(`/api/v1/mail/export?id=${encodeURIComponent(email.id)}`, {
        credentials: "include",
      })
      if (!res.ok) throw new Error()
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = (email.subject || "message") + ".eml"
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch {
      toast.error(t("emailDetail.exportFailed"))
    }
  }

  const handleMove = async (folder: string, label: string) => {
    if (!email) return
    try {
      await api.moveMail(email.id, folder)
      toast.success(t("emailDetail.movedTo", { folder: label }))
      navigate("/inbox")
    } catch {
      toast.error(t("emailDetail.failedToMove"))
    }
  }

  // handleCopy copies the open message into another folder, leaving the original
  // in place so the reader stays valid (no navigation, unlike move).
  const handleCopy = async (folder: string, label: string) => {
    if (!email) return
    try {
      await api.copyMail(email.id, folder)
      toast.success(t("emailDetail.copiedTo", { folder: label }))
    } catch {
      toast.error(t("emailDetail.copyFailed"))
    }
  }

  return (
    <div className="space-y-4">
      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
        </div>
      ) : !email ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <h3 className="mt-4 text-lg font-semibold">{t("emailDetail.notFound")}</h3>
          <p className="text-sm text-muted-foreground">{t("emailDetail.notFoundDescription")}</p>
          <Button className="mt-4" onClick={() => navigate("/inbox")}>{t("emailDetail.backToInbox")}</Button>
        </div>
      ) : (
        <>
          {/* Toolbar */}
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-1">
              <Button variant="ghost" size="icon" onClick={() => navigate(-1)} title={t("common.back")}>
                <ArrowLeft className="h-5 w-5" />
              </Button>
              <Button variant="ghost" size="sm" onClick={handleReply} title={t("common.reply")}>
                <Reply className="h-4 w-4 mr-1" />
                {t("common.reply")}
              </Button>
              <Button variant="ghost" size="sm" onClick={handleReplyAll} title={t("common.replyAll")}>
                <ReplyAll className="h-4 w-4 mr-1" />
                {t("common.replyAll")}
              </Button>
              <Button variant="ghost" size="sm" onClick={handleForward} title={t("common.forward")}>
                <Forward className="h-4 w-4 mr-1" />
                {t("common.forward")}
              </Button>
              {email.folder === "Sent" && (
                <Button variant="ghost" size="sm" onClick={handleRecall} title={t("emailDetail.recall")}>
                  <Undo2 className="h-4 w-4 mr-1" />
                  {t("emailDetail.recall")}
                </Button>
              )}
              {email.folder === "Recoverable Items" && (
                <Button variant="ghost" size="sm" onClick={handleRecover} title={t("emailDetail.recover")}>
                  <RotateCcw className="h-4 w-4 mr-1" />
                  {t("emailDetail.recover")}
                </Button>
              )}
            </div>
            <div className="flex items-center gap-1">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    title={t("emailDetail.followUp")}
                    aria-pressed={email.followupStatus === 2}
                  >
                    <Flag
                      className={`h-5 w-5 ${
                        email.followupStatus === 2
                          ? FOLLOWUP_COLORS.find((c) => c.value === email.followupColor)?.fill ??
                            "fill-red-500 text-red-500"
                          : ""
                      }`}
                    />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-60">
                  <div className="flex gap-1 px-2 py-1.5">
                    {FOLLOWUP_COLORS.map((c) => (
                      <button
                        key={c.value}
                        type="button"
                        title={t(`emailDetail.${c.key}`)}
                        onClick={() => handleFollowup("flag", c.value)}
                        className={`flex h-7 w-7 items-center justify-center rounded hover:bg-accent ${
                          email.followupStatus === 2 && email.followupColor === c.value ? "ring-2 ring-ring" : ""
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
                        handleFollowup("flag", email.followupColor || 6, new Date(e.target.value).toISOString())
                      }
                    />
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={() => handleFollowup("complete")}>
                    <Check className="mr-2 h-4 w-4" />
                    {t("emailDetail.markComplete")}
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleFollowup("clear")}>
                    <X className="mr-2 h-4 w-4" />
                    {t("emailDetail.clearFollowUp")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button variant="ghost" size="icon" onClick={handleMarkUnread} title={t("common.markUnread")}>
                <Mail className="h-5 w-5" />
              </Button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" title={t("emailDetail.moveToFolder")}>
                    <FolderInput className="h-5 w-5" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => handleMove("inbox", t("nav.inbox"))}>{t("nav.inbox")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("archive", t("common.archive"))}>{t("common.archive")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("spam", t("nav.spam"))}>{t("nav.spam")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("trash", t("nav.trash"))}>{t("nav.trash")}</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" title={t("emailDetail.copyToFolder")}>
                    <Copy className="h-5 w-5" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => handleCopy("inbox", t("nav.inbox"))}>{t("nav.inbox")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleCopy("archive", t("common.archive"))}>{t("common.archive")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleCopy("spam", t("nav.spam"))}>{t("nav.spam")}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleCopy("trash", t("nav.trash"))}>{t("nav.trash")}</DropdownMenuItem>
                  {copyFolders.length > 0 && <DropdownMenuSeparator />}
                  {copyFolders.map((f) => (
                    <DropdownMenuItem key={f} onClick={() => handleCopy(f, f)}>{f}</DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                variant="ghost"
                size="icon"
                onClick={handleExportEML}
                title={t("emailDetail.exportEML")}
              >
                <Download className="h-5 w-5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => window.print()}
                title={t("emailDetail.print")}
              >
                <Printer className="h-5 w-5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => window.open(`/api/v1/mail/source?id=${encodeURIComponent(email.id)}`, "_blank")}
                title={t("emailDetail.viewSource")}
              >
                <Code className="h-5 w-5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="text-destructive"
                onClick={handleDelete}
                title={t("common.delete")}
              >
                <Trash2 className="h-5 w-5" />
              </Button>
            </div>
          </div>

          {/* Email Content */}
          <div className="rounded-lg border bg-card">
            {/* Header */}
            <div className="p-6 pb-0">
              <h1 className="text-2xl font-semibold leading-tight">{email.subject}</h1>

              <div className="flex items-start gap-4 mt-6">
                <Avatar className="h-12 w-12 ring-2 ring-primary/10">
                  <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold text-lg">
                    {email.from.split(" ").map((n) => n[0]).join("").slice(0, 2)}
                  </AvatarFallback>
                </Avatar>

                <div className="flex-1 min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-semibold text-lg">{email.from}</span>
                    <span className="text-sm text-muted-foreground">
                      &lt;{email.fromEmail}&gt;
                    </span>
                  </div>

                  <div className="mt-1 text-sm text-muted-foreground">
                    <span className="font-medium text-foreground">{t("common.to")}:</span>{" "}
                    {email.to
                      .map((addr, i) => {
                        const nm = email.toNames?.[i]
                        return nm ? `${nm} <${addr}>` : addr
                      })
                      .join(", ")}
                  </div>

                  <div className="mt-1 text-sm text-muted-foreground">{formatAbsolute(email.date)}</div>

                  {/* Category labels */}
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    <Tag className="h-3.5 w-3.5 text-muted-foreground" />
                    {email.labels.map((label) => {
                      const color = categoryColors[label.toLowerCase()]
                      return (
                        <Badge
                          key={label}
                          variant="secondary"
                          className="gap-1"
                          style={color ? { backgroundColor: color, color: "#fff" } : undefined}
                        >
                          {label}
                          <button
                            onClick={() => handleRemoveLabel(label)}
                            className={color ? "opacity-80 hover:opacity-100" : "text-muted-foreground hover:text-destructive"}
                            aria-label={t("emailDetail.removeLabel", { label })}
                          >
                            <X className="h-3 w-3" />
                          </button>
                        </Badge>
                      )
                    })}
                    {labelEditing ? (
                      <Input
                        autoFocus
                        value={newLabel}
                        onChange={(e) => setNewLabel(e.target.value)}
                        onBlur={() => { handleAddLabel(); setLabelEditing(false) }}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") { handleAddLabel(); setLabelEditing(false) }
                          if (e.key === "Escape") { setNewLabel(""); setLabelEditing(false) }
                        }}
                        placeholder={t("emailDetail.labelPlaceholder")}
                        className="h-6 w-28 text-xs"
                      />
                    ) : (
                      <button
                        onClick={() => setLabelEditing(true)}
                        className="flex items-center gap-1 rounded border border-dashed px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground"
                      >
                        <Plus className="h-3 w-3" />
                        {t("emailDetail.addLabel")}
                      </button>
                    )}
                  </div>
                </div>
              </div>
            </div>

            {/* Meeting invitation: RSVP actions */}
            {invite && (
              <div className="mx-6 mt-4 rounded-lg border border-primary/30 bg-primary/5 p-4">
                <div className="flex items-center gap-2 text-sm font-medium">
                  <CalendarCheck className="h-4 w-4 text-primary" />
                  {t("emailDetail.meetingInvitation")}
                </div>
                <div className="mt-2 space-y-1 text-sm">
                  {invite.summary && <div className="font-medium">{invite.summary}</div>}
                  {invite.start && (
                    <div className="text-muted-foreground">
                      {(() => {
                        const d = new Date(invite.start)
                        return isNaN(d.getTime()) ? invite.start : d.toLocaleString([], withTz())
                      })()}
                    </div>
                  )}
                  {invite.location && (
                    <div className="text-muted-foreground">{invite.location}</div>
                  )}
                  {invite.organizer && (
                    <div className="text-muted-foreground">{t("emailDetail.organizer", { name: invite.organizer })}</div>
                  )}
                </div>
                <div className="mt-3 flex items-center gap-2">
                  <Button
                    size="sm"
                    variant={rsvpStatus === "accept" ? "default" : "outline"}
                    onClick={() => handleRsvp("accept")}
                    disabled={rsvpBusy}
                  >
                    <Check className="mr-1 h-4 w-4" />
                    {t("emailDetail.accept")}
                  </Button>
                  <Button
                    size="sm"
                    variant={rsvpStatus === "tentative" ? "default" : "outline"}
                    onClick={() => handleRsvp("tentative")}
                    disabled={rsvpBusy}
                  >
                    <HelpCircle className="mr-1 h-4 w-4" />
                    {t("emailDetail.tentative")}
                  </Button>
                  <Button
                    size="sm"
                    variant={rsvpStatus === "decline" ? "default" : "outline"}
                    onClick={() => handleRsvp("decline")}
                    disabled={rsvpBusy}
                  >
                    <X className="mr-1 h-4 w-4" />
                    {t("emailDetail.decline")}
                  </Button>
                </div>
              </div>
            )}

            <Separator className="my-6" />

            {/* Body */}
            <div className="px-6 pb-6">
              {(email.smimeSigned || email.smimeEncrypted) && (
                <div
                  className={
                    "mb-4 flex items-center gap-2 rounded-md border px-3 py-2 text-sm " +
                    (email.smimeSigned && !email.smimeVerified
                      ? "border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-200"
                      : "border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-700 dark:bg-emerald-950 dark:text-emerald-200")
                  }
                >
                  <ShieldCheck className="h-4 w-4 shrink-0" />
                  <span>
                    {email.smimeEncrypted && t("emailDetail.smimeEncrypted")}
                    {email.smimeEncrypted && email.smimeSigned && " · "}
                    {email.smimeSigned &&
                      (email.smimeVerified
                        ? t("emailDetail.smimeSignedVerified", { signer: email.smimeSignedBy || "" })
                        : t("emailDetail.smimeSignedUnverified", { signer: email.smimeSignedBy || "" }))}
                  </span>
                </div>
              )}
              {(() => {
                const { html, blockedRemote } = sanitizeEmailBody(email.content, !showImages)
                return (
                  <>
                    {blockedRemote && !showImages && (
                      <div className="mb-4 flex items-center justify-between gap-3 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-200">
                        <span>{t("emailDetail.imagesBlocked")}</span>
                        <Button variant="outline" size="sm" onClick={() => setShowImages(true)}>
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
              })()}
            </div>

            {/* Attachments */}
            {email.attachments.length > 0 && (
              <div className="border-t px-6 py-4">
                <div className="mb-2 flex items-center justify-between text-sm font-medium text-muted-foreground">
                  <div className="flex items-center gap-1.5">
                    <Paperclip className="h-4 w-4" />
                    {email.attachments.length > 1
                      ? t("emailDetail.attachments", { count: String(email.attachments.length) })
                      : t("emailDetail.attachment", { count: String(email.attachments.length) })}
                  </div>
                  {email.attachments.length > 1 && (
                    <button
                      onClick={handleDownloadAll}
                      className="flex items-center gap-1 text-xs hover:text-foreground transition-colors"
                      title={t("emailDetail.downloadAll")}
                    >
                      <Download className="h-3.5 w-3.5" />
                      {t("emailDetail.downloadAll")}
                    </button>
                  )}
                </div>
                <div className="flex flex-wrap gap-2">
                  {email.attachments.map((att) => (
                    <button
                      key={att.index}
                      onClick={() => handleDownloadAttachment(att)}
                      className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-left text-sm hover:bg-accent/50 transition-colors"
                      title={t("emailDetail.downloadAttachment", { filename: att.filename })}
                    >
                      <Paperclip className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <span className="min-w-0">
                        <span className="block truncate font-medium">{att.filename}</span>
                        <span className="block text-xs text-muted-foreground">{formatFileSize(att.size)}</span>
                      </span>
                      <Download className="h-4 w-4 shrink-0 text-muted-foreground" />
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
