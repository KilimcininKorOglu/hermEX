import { useState, useRef, useEffect, useCallback } from "react"
import { useNavigate, useSearchParams } from "react-router-dom"
import {
  ArrowLeft,
  Send,
  Save,
  Paperclip,
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
  Gauge,
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
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import api, { SenderIdentity, DiagnosticEntry, Contact as ContactType, MailAttachment, SignatureEntry, TemplateEntry } from "@/utils/api"
import * as smimeStore from "@/utils/smime"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import { useI18n } from "@/hooks/useI18n"
import { withTz, zonedInputToISO } from "@/utils/date"
import { RichTextEditor } from "@/components/RichTextEditor"

interface Attachment {
  id: string
  name: string
  size: number
  file?: File
}

interface Recipient {
  id: string
  name: string
  email: string
}

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

export function ComposePage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { user } = useAuth()
  const { currentMailbox, isInSharedMailbox } = useMailbox()
  const { t } = useI18n()

  const [to, setTo] = useState<Recipient[]>([])
  const [cc, setCc] = useState<Recipient[]>([])
  const [bcc, setBcc] = useState<Recipient[]>([])
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [lastSaved, setLastSaved] = useState<Date | null>(null)
  const [isSaving, setIsSaving] = useState(false)
  
  // Sender identity state
  const [senderIdentities, setSenderIdentities] = useState<SenderIdentity[]>([])
  const [selectedSender, setSelectedSender] = useState<SenderIdentity | null>(null)
  const [showSenderDropdown, setShowSenderDropdown] = useState(false)
  const [diagnostics, setDiagnostics] = useState<DiagnosticEntry[]>([])
  const [showDiagnostics, setShowDiagnostics] = useState(false)
  
  // Load sender identities on mount
  useEffect(() => {
    const loadSenderIdentities = async () => {
      try {
        const identities = await api.getSenderIdentities(user?.email || '')
        setSenderIdentities(identities)
        
        // Set default sender based on current mailbox context
        if (isInSharedMailbox() && currentMailbox.owner) {
          // Default to the shared mailbox identity when in shared context
          const sharedIdentity = identities.find(
            (id: SenderIdentity) => id.email === currentMailbox.owner && id.type !== 'personal'
          )
          if (sharedIdentity) {
            setSelectedSender(sharedIdentity)
          } else if (identities.length > 0) {
            setSelectedSender(identities[0])
          }
        } else if (identities.length > 0) {
          // Default to personal identity
          const personalIdentity = identities.find((id: SenderIdentity) => id.type === 'personal')
          setSelectedSender(personalIdentity || identities[0])
        }
      } catch (err) {
        console.error('Failed to load sender identities:', err)
        // Fallback to personal identity
        if (user?.email) {
          setSelectedSender({
            email: user.email,
            displayName: user.email,
            type: 'personal',
            canSend: true
          })
        }
      }
    }
    
    loadSenderIdentities()
  }, [user, currentMailbox, isInSharedMailbox])
  
  // Load diagnostics
  useEffect(() => {
    const loadDiagnostics = async () => {
      try {
        const result = await api.getDiagnostics()
        if (result.errors) {
          setDiagnostics(result.errors)
        }
      } catch (err) {
        console.error('Failed to load diagnostics:', err)
      }
    }
    
    loadDiagnostics()
  }, [])

  const [requestReadReceipt, setRequestReadReceipt] = useState(false)
  const [signMessage, setSignMessage] = useState(false)
  const [encryptMessage, setEncryptMessage] = useState(false)
  const [importance, setImportance] = useState<"low" | "normal" | "high">("normal")
  const [subject, setSubject] = useState("")
  const [body, setBody] = useState("")
  const [attachments, setAttachments] = useState<Attachment[]>([])
  const [searchQuery, setSearchQuery] = useState("")
  // Free-text recipient inputs (typed email addresses, not just picked contacts)
  const [recipientInput, setRecipientInput] = useState<{ to: string; cc: string; bcc: string }>({ to: "", cc: "", bcc: "" })
  // Which recipient field is being typed in, so GAL suggestions show inline
  // under that field (typing the main field — not just the "+" picker — surfaces
  // directory matches).
  const [activeField, setActiveField] = useState<"to" | "cc" | "bcc" | null>(null)
  const [showCc, setShowCc] = useState(false)
  const [showBcc, setShowBcc] = useState(false)
  const [sending, setSending] = useState(false)
  // Scheduled ("send later"): scheduleOpen toggles the picker; scheduledAt holds
  // the chosen wall clock (datetime-local value, interpreted in the display tz).
  const [scheduleOpen, setScheduleOpen] = useState(false)
  const [scheduledAt, setScheduledAt] = useState("")
  const [draftId, setDraftId] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const bodyRef = useRef<HTMLTextAreaElement>(null)
  const richTextRef = useRef<{ getHTML: () => string; setHTML: (html: string) => void } | null>(null)
  const autoSaveTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // richTextMode follows the user's preference; HTML bodies are sent when true.
  const [richTextMode, setRichTextMode] = useState(false)
  
  // Contacts loaded from API for recipient selection
  const [contacts, setContacts] = useState<Recipient[]>([])
  
  // Load contacts from API on mount
  useEffect(() => {
    const loadContacts = async () => {
      try {
        const result = await api.getContacts()
        if (result.contacts) {
          const recipients: Recipient[] = result.contacts.map((c: ContactType) => ({
            id: c.id,
            name: c.name,
            email: c.email,
          }))
          setContacts(recipients)
        }
      } catch (err) {
        console.error('Failed to load contacts:', err)
      }
    }
    loadContacts()
  }, [])
  
  // Handle replyTo/cc params after contacts are loaded. Both accept a
  // comma-separated list so Reply All can prefill multiple recipients.
  useEffect(() => {
    const toRecipient = (email: string, idx: number): Recipient => {
      const contact = contacts.find((c) => c.email === email)
      return contact ?? { id: `param-${idx}-${email}`, name: email, email }
    }
    const parseList = (raw: string | null) =>
      (raw ?? "")
        .split(",")
        .map((e) => e.trim())
        .filter(Boolean)

    const replyTo = parseList(searchParams.get("replyTo"))
    if (replyTo.length > 0) {
      setTo(replyTo.map(toRecipient))
    }
    const ccList = parseList(searchParams.get("cc"))
    if (ccList.length > 0) {
      setCc(ccList.map(toRecipient))
      setShowCc(true)
    }
  }, [searchParams, contacts])

  // Prefill subject/body from query params (used by reply and forward).
  useEffect(() => {
    const subjectParam = searchParams.get("subject")
    if (subjectParam) {
      setSubject(subjectParam)
    }
    const bodyParam = searchParams.get("body")
    if (bodyParam) {
      setBody(bodyParam)
    }
  }, [searchParams])

  // Outgoing-mail signatures: load the list once; the picker shows the first
  // "default" entry. For new/reply/forward messages the selected signature is
  // appended to the composer body. Skipped when editing an existing draft.
  const [signatures, setSignatures] = useState<SignatureEntry[]>([])
  const [selectedSignature, setSelectedSignature] = useState<SignatureEntry | null>(null)
  const [showSigPicker, setShowSigPicker] = useState(false)
  const signatureAppliedRef = useRef(false)

  // Message templates
  const [templates, setTemplates] = useState<TemplateEntry[]>([])
  const [selectedTemplate, setSelectedTemplate] = useState<TemplateEntry | null>(null)
  const [showTplPicker, setShowTplPicker] = useState(false)
  const templateAppliedRef = useRef(false)

  useEffect(() => {
    let cancelled = false
    api.getSignatures()
      .then((res) => {
        if (cancelled) return
        const list = res.signatures ?? []
        setSignatures(list)
        if (list.length > 0) {
          const def = list.find((s) => s.name === "default") ?? list[0]
          setSelectedSignature(def)
        }
      })
      .catch(() => {
        // no signatures configured
      })
    api.getTemplates()
      .then((res) => {
        if (cancelled) return
        setTemplates(res.templates ?? [])
      })
      .catch(() => {})
    api.getPreferences()
      .then((res) => {
        if (cancelled || !res.preferences) return
        setRichTextMode(res.preferences.richTextMode ?? false)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (signatureAppliedRef.current || !selectedSignature) return
    if (searchParams.get("draft")) return
    signatureAppliedRef.current = true
    const sep = selectedSignature.is_html ? "<br><br>-- <br>" : "\n\n-- \n"
    setBody((prev) => `${prev}${sep}${selectedSignature.body}`)
  }, [selectedSignature, searchParams])

  // Apply selected template: insert subject and body into the compose form.
  useEffect(() => {
    if (templateAppliedRef.current || !selectedTemplate) return
    if (searchParams.get("draft")) return
    templateAppliedRef.current = true
    if (selectedTemplate.subject) setSubject(selectedTemplate.subject)
    const sep = selectedTemplate.is_html ? "<br><br>" : "\n\n"
    setBody((prev) => prev ? `${prev}${sep}${selectedTemplate.body}` : selectedTemplate.body)
  }, [selectedTemplate, searchParams])

  // Load an existing draft into the composer when ?draft=<id> is present so
  // "Edit draft" reopens its content instead of a blank message.
  useEffect(() => {
    const draftParam = searchParams.get("draft")
    if (!draftParam) return
    let active = true
    api.getMessage(draftParam)
      .then((msg) => {
        if (!active || !msg) return
        setDraftId(msg.id || draftParam)
        setSubject(msg.subject || "")
        setBody(msg.body || "")
        if (Array.isArray(msg.to)) {
          const recipients = msg.to
            .map((t) => t.trim())
            .filter(Boolean)
            .map((email, idx) => ({ id: `draft-to-${idx}-${email}`, name: email, email }))
          if (recipients.length > 0) setTo(recipients)
        }
      })
      .catch((err) => console.error("Failed to load draft:", err))
    return () => {
      active = false
    }
  }, [searchParams])

  const filteredContacts = contacts.filter(
    (c) =>
      c.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      c.email.toLowerCase().includes(searchQuery.toLowerCase())
  )

  // Organization directory (GAL) results, fetched as the user types. Debounced
  // and merged below personal contacts, deduped by email.
  const [directoryResults, setDirectoryResults] = useState<Recipient[]>([])
  useEffect(() => {
    const q = searchQuery.trim()
    if (q.length < 2) {
      setDirectoryResults([])
      return
    }
    let cancelled = false
    const timer = setTimeout(async () => {
      try {
        const res = await api.searchDirectory(q)
        if (cancelled) return
        setDirectoryResults(
          (res.entries ?? []).map((e) => ({ id: `gal-${e.email}`, name: e.name || e.email, email: e.email }))
        )
      } catch {
        if (!cancelled) setDirectoryResults([])
      }
    }, 200)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [searchQuery])

  const contactEmails = new Set(contacts.map((c) => c.email.toLowerCase()))
  const suggestions = [
    ...filteredContacts,
    ...directoryResults.filter((d) => !contactEmails.has(d.email.toLowerCase())),
  ]

  const addRecipient = (contact: Recipient, field: "to" | "cc" | "bcc") => {
    if (field === "to") {
      if (!to.find((r) => r.id === contact.id)) {
        setTo([...to, contact])
      }
    } else if (field === "cc") {
      if (!cc.find((r) => r.id === contact.id)) {
        setCc([...cc, contact])
      }
    } else {
      if (!bcc.find((r) => r.id === contact.id)) {
        setBcc([...bcc, contact])
      }
    }
    setSearchQuery("")
  }

  // pickSuggestion adds a GAL/contact suggestion picked from the inline list
  // under a recipient field, then clears that field's typed text.
  const pickSuggestion = (contact: Recipient, field: "to" | "cc" | "bcc") => {
    addRecipient(contact, field)
    setRecipientInput((p) => ({ ...p, [field]: "" }))
    setActiveField(null)
  }

  // suggestionsPanel renders the inline GAL/contact suggestions under the
  // currently active recipient field. onMouseDown preventDefault keeps the
  // input's onBlur (which commits typed text) from firing before the click.
  const suggestionsPanel = (field: "to" | "cc" | "bcc") =>
    activeField === field && recipientInput[field].trim().length >= 2 && suggestions.length > 0 ? (
      <div className="absolute left-0 top-full z-20 mt-1 w-72 max-h-48 overflow-auto rounded-md border bg-popover shadow-md">
        {suggestions.map((contact) => (
          <button
            key={contact.id}
            type="button"
            onMouseDown={(e) => {
              e.preventDefault()
              pickSuggestion(contact, field)
            }}
            className="flex w-full flex-col items-start px-3 py-2 text-left hover:bg-accent"
          >
            <span className="text-sm font-medium">{contact.name}</span>
            <span className="text-xs text-muted-foreground">{contact.email}</span>
          </button>
        ))}
      </div>
    ) : null

  // addTypedRecipient adds a free-text email address to a recipient field,
  // validating its basic shape so users can email anyone, not just contacts.
  const addTypedRecipient = (field: "to" | "cc" | "bcc") => {
    const email = recipientInput[field].trim().replace(/[,;]+$/, "").trim()
    setRecipientInput((prev) => ({ ...prev, [field]: "" }))
    if (!email) return
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      toast.error(t("compose.invalidEmail"))
      return
    }
    addRecipient({ id: `typed-${field}-${email}`, name: email, email }, field)
  }

  const removeRecipient = (id: string, field: "to" | "cc" | "bcc") => {
    if (field === "to") {
      setTo(to.filter((r) => r.id !== id))
    } else if (field === "cc") {
      setCc(cc.filter((r) => r.id !== id))
    } else {
      setBcc(bcc.filter((r) => r.id !== id))
    }
  }

  const handleAttach = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (files) {
      const newAttachments = Array.from(files).map((file) => ({
        id: crypto.randomUUID(),
        name: file.name,
        size: file.size,
        file,
      }))
      setAttachments([...attachments, ...newAttachments])
      toast.success(
        files.length > 1
          ? t("compose.filesAttached", { count: String(files.length) })
          : t("compose.fileAttached", { count: String(files.length) })
      )
    }
  }

  const removeAttachment = (id: string) => {
    setAttachments(attachments.filter((a) => a.id !== id))
  }

  // applyFormat wraps the current selection with markdown-style markers (the
  // body is plain text, sent as text/plain). It keeps focus in the textarea.
  const applyFormat = (kind: "bold" | "italic" | "underline" | "link" | "list" | "image") => {
    const ta = bodyRef.current
    const start = ta ? ta.selectionStart : body.length
    const end = ta ? ta.selectionEnd : body.length
    const selected = body.slice(start, end)
    let insert = selected
    switch (kind) {
      case "bold":
        insert = `**${selected || t("compose.boldText")}**`
        break
      case "italic":
        insert = `*${selected || t("compose.italicText")}*`
        break
      case "underline":
        insert = `__${selected || t("compose.underlinedText")}__`
        break
      case "link":
        insert = `[${selected || t("compose.linkText")}](https://)`
        break
      case "image":
        insert = `![${selected || t("compose.imageText")}](https://)`
        break
      case "list":
        insert = (selected || t("compose.listItem"))
          .split("\n")
          .map((line) => `- ${line}`)
          .join("\n")
        break
    }
    const next = body.slice(0, start) + insert + body.slice(end)
    setBody(next)
    requestAnimationFrame(() => {
      if (!ta) return
      ta.focus()
      const pos = start + insert.length
      ta.setSelectionRange(pos, pos)
    })
  }

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return bytes + " B"
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB"
    return (bytes / (1024 * 1024)).toFixed(1) + " MB"
  }

  const handleAutoSave = useCallback(() => {
    if (subject || body || to.length > 0 || attachments.length > 0) {
      setIsSaving(true)
      setTimeout(() => {
        setLastSaved(new Date())
        setIsSaving(false)
      }, 500)
    }
  }, [subject, body, to, attachments])

  useEffect(() => {
    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current)
    }
    autoSaveTimerRef.current = setTimeout(handleAutoSave, 3000)
    return () => {
      if (autoSaveTimerRef.current) {
        clearTimeout(autoSaveTimerRef.current)
      }
    }
  }, [subject, body, to, attachments, handleAutoSave])

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault()
        handleSend()
      }
    }
    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [to, subject, body])

  // Check if selected sender can send
  const canSendAsSelected = selectedSender?.canSend ?? true
  const sendError = !canSendAsSelected && selectedSender
    ? t("compose.noSendPermission", { email: selectedSender.email })
    : null
  
  const handleSend = async () => {
    if (to.length === 0) {
      toast.error(t("compose.selectRecipient"))
      return
    }
    if (!subject.trim()) {
      toast.error(t("compose.enterSubject"))
      return
    }

    // Check if sender is allowed
    if (!canSendAsSelected) {
      toast.error(sendError || t("compose.cannotSendIdentity"))
      return
    }

    // Check for policy errors from diagnostics
    const policyErrors = diagnostics.filter(d => d.category === 'policy' && d.severity === 'error')
    if (policyErrors.length > 0) {
      setShowDiagnostics(true)
      toast.error(t("compose.resolveIssues"))
      return
    }

    // Resolve a scheduled send time (if any) to an absolute instant in the
    // user's display timezone, and reject a time that is not in the future.
    let sendAtISO: string | undefined
    if (scheduledAt) {
      const iso = zonedInputToISO(scheduledAt)
      if (!iso || new Date(iso).getTime() <= Date.now()) {
        toast.error(t("compose.scheduleInPast"))
        return
      }
      sendAtISO = iso
    }

    // S/MIME (browser-held key) is applied client-side, so the message must be
    // built, signed/encrypted here, and sent raw. Guard the unsupported combos.
    // Browser-mode S/MIME (a key in this browser) signs/encrypts here and sends
    // raw; otherwise the server holds the key and does it on /mail/send.
    const useBrowserSmime = (signMessage || encryptMessage) && (await smimeStore.hasIdentity())
    if (signMessage || encryptMessage) {
      if (sendAtISO) { toast.error(t("compose.smimeNoSchedule")); return }
      if (useBrowserSmime && signMessage && !smimeStore.isUnlocked()) { toast.error(t("compose.smimeLocked")); return }
    }

    setSending(true)
    toast.success(sendAtISO ? t("compose.schedulingEmail") : t("compose.sendingEmail"))

    try {
      // Use the actual API with sender identity
      const senderEmail = selectedSender?.email || user?.email || ''
      const encoded = (
        await Promise.all(
          attachments.map(async (a): Promise<MailAttachment | null> =>
            a.file
              ? {
                  filename: a.name,
                  contentType: a.file.type || "application/octet-stream",
                  content: await fileToBase64(a.file),
                }
              : null
          )
        )
      ).filter((x): x is MailAttachment => x !== null)
      // When richTextMode is on, grab HTML from the rich text editor; otherwise use plain body.
      const htmlBody = richTextMode && richTextRef.current ? richTextRef.current.getHTML() : body
      const sendPayload = {
        to: to.map(r => r.email),
        cc: cc.map(r => r.email),
        bcc: bcc.map(r => r.email),
        subject,
        body: htmlBody,
        from: senderEmail, // Pass sender identity to API
        attachments: encoded.length > 0 ? encoded : undefined,
        requestReadReceipt: requestReadReceipt || undefined,
        importance: importance !== "normal" ? importance : undefined,
        sendAt: sendAtISO,
        is_html: richTextMode,
      }
      if (useBrowserSmime) {
        // Build the MIME server-side, then sign and/or encrypt it in the browser
        // (sign-then-encrypt), and relay the raw result. The key never leaves here.
        const { raw } = await api.buildMail(sendPayload)
        let mime = atob(raw)
        if (signMessage) mime = smimeStore.signMime(mime)
        if (encryptMessage) {
          const recips = [...sendPayload.to, ...sendPayload.cc, ...sendPayload.bcc, senderEmail]
          const certs: string[] = []
          const seen = new Set<string>()
          for (const addr of recips) {
            const a = addr.trim().toLowerCase()
            if (!a || seen.has(a)) continue
            seen.add(a)
            const rc = await api.getRecipientCertificate(addr)
            if (!rc?.cert) throw new Error(t("compose.smimeNoRecipientCert", { addr }))
            certs.push(rc.cert)
          }
          mime = smimeStore.encryptMime(mime, certs)
        }
        await api.sendRawMail(btoa(mime), sendPayload.to, sendPayload.cc, sendPayload.bcc)
      } else if (signMessage || encryptMessage) {
        // Server-mode: the server holds the key and signs/encrypts on delivery.
        await api.sendMail({ ...sendPayload, signMessage, encryptMessage })
      } else {
        await api.sendMail(sendPayload)
      }

      if (sendAtISO) {
        toast.success(t("compose.emailScheduled"))
        navigate("/scheduled")
      } else {
        toast.success(t("compose.emailSent"))
        navigate("/sent")
      }
    } catch (err) {
      console.error('Failed to send email:', err)
      const fallback = sendAtISO ? t("compose.scheduleFailed") : t("compose.sendFailed")
      toast.error(err instanceof Error && err.message ? err.message : fallback)
      setSending(false)
    }
  }

  const handleSaveDraft = async () => {
    if (!(subject || body || to.length > 0 || cc.length > 0 || bcc.length > 0)) {
      toast.error(t("compose.nothingToSave"))
      return
    }
    try {
      const senderEmail = selectedSender?.email || user?.email || ''
      const res = await api.saveDraft({
        id: draftId ?? undefined,
        to: to.map((r) => r.email),
        cc: cc.map((r) => r.email),
        bcc: bcc.map((r) => r.email),
        subject,
        body,
        from: senderEmail,
      })
      if (res?.id) setDraftId(res.id)
      toast.success(t("compose.draftSaved"))
      navigate("/drafts")
    } catch (err) {
      console.error('Failed to save draft:', err)
      toast.error(t("compose.draftSaveFailed"))
    }
  }

  const handleDiscard = () => {
    if (subject || body || to.length > 0) {
      if (confirm(t("compose.discardConfirm"))) {
        handleSaveDraft()
      }
    } else {
      navigate("/inbox")
    }
  }

  const formatLastSaved = () => {
    if (!lastSaved) return null
    const now = new Date()
    const diff = Math.floor((now.getTime() - lastSaved.getTime()) / 1000)
    if (diff < 60) return t("compose.justNow")
    if (diff < 3600) return t("compose.minutesAgo", { minutes: String(Math.floor(diff / 60)) })
    return lastSaved.toLocaleTimeString([], withTz({ hour: "2-digit", minute: "2-digit" }))
  }

  return (
    <div className={cn(
      "flex flex-col bg-background transition-all duration-200",
      isFullscreen ? "fixed inset-0 z-50" : "h-[calc(100vh-4rem)]"
    )}>
      {/* Header */}
      <div className="flex items-center justify-between border-b px-4 py-2">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" onClick={handleDiscard}>
            <ArrowLeft className="h-5 w-5" />
          </Button>
          <span className="font-medium">{t("compose.newMessage")}</span>
          {lastSaved && (
            <span className="flex items-center gap-1 text-xs text-muted-foreground ml-2">
              {isSaving ? (
                <>
                  <Clock className="h-3 w-3 animate-pulse" />
                  {t("common.saving")}
                </>
              ) : (
                <>
                  <Check className="h-3 w-3" />
                  {t("compose.saved", { time: formatLastSaved() ?? "" })}
                </>
              )}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => setIsFullscreen(!isFullscreen)}
            title={isFullscreen ? t("compose.exitFullscreen") : t("compose.fullscreen")}
          >
            {isFullscreen ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </Button>
          <Button variant="ghost" size="icon" onClick={handleSaveDraft} title={t("compose.saveDraftTooltip")}>
            <Save className="h-4 w-4" />
          </Button>
          <Button
            variant={scheduledAt ? "secondary" : "ghost"}
            size="icon"
            onClick={() => setScheduleOpen(o => !o)}
            title={t("compose.scheduleSend")}
          >
            <Clock className="h-4 w-4" />
          </Button>
          {signatures.length > 0 && (
            <DropdownMenu open={showSigPicker} onOpenChange={setShowSigPicker}>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" title={t("compose.signature")}>
                  <span className="text-xs font-serif italic">Sig</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {signatures.map((sig) => (
                  <DropdownMenuItem
                    key={sig.name}
                    onClick={() => {
                      setSelectedSignature(sig)
                      setShowSigPicker(false)
                    }}
                    className={selectedSignature?.name === sig.name ? "bg-accent" : ""}
                  >
                    <span className="mr-2">{sig.is_html ? "HTML" : "TXT"}</span>
                    {sig.name}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {templates.length > 0 && (
            <DropdownMenu open={showTplPicker} onOpenChange={setShowTplPicker}>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" title={t("compose.template")}>
                  <span className="text-xs font-bold">Tpl</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {templates.map((tpl) => (
                  <DropdownMenuItem
                    key={tpl.name}
                    onClick={() => {
                      setSelectedTemplate(tpl)
                      setShowTplPicker(false)
                    }}
                    className={selectedTemplate?.name === tpl.name ? "bg-accent" : ""}
                  >
                    <span className="mr-2">{tpl.is_html ? "HTML" : "TXT"}</span>
                    {tpl.name}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <Button
            className="gap-2"
            onClick={handleSend}
            disabled={sending || to.length === 0}
          >
            <Send className="h-4 w-4" />
            {sending
              ? (scheduledAt ? t("compose.scheduling") : t("common.sending"))
              : (scheduledAt ? t("compose.scheduleSend") : t("common.send"))}
          </Button>
        </div>
      </div>

      {/* Send-later picker: choose an absolute time (interpreted in the display
          timezone); clearing the time returns to immediate send. */}
      {scheduleOpen && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/40 px-4 py-2">
          <Clock className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm text-muted-foreground">{t("compose.scheduleSendAt")}:</span>
          <Input
            type="datetime-local"
            className="h-8 w-auto"
            value={scheduledAt}
            onChange={(e) => setScheduledAt(e.target.value)}
          />
          {scheduledAt && (
            <Button variant="ghost" size="sm" onClick={() => setScheduledAt("")}>
              {t("compose.scheduleClear")}
            </Button>
          )}
        </div>
      )}

      {/* Recipients */}
      <div className="border-b px-4 py-2 space-y-2">
        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground">{t("common.to")}:</span>
          <div className="relative flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
            {to.map((r) => (
              <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                {r.name}
                <button
                  onClick={() => removeRecipient(r.id, "to")}
                  className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" className="h-6 w-6" title={t("compose.searchAddressBook")} aria-label={t("compose.searchAddressBook")}>
                  <Plus className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-72">
                <div className="p-2">
                  <Input
                    placeholder={t("compose.searchPeople")}
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                  />
                </div>
                <Separator />
                <div className="max-h-48 overflow-auto">
                  {suggestions.map((contact) => (
                    <DropdownMenuItem
                      key={contact.id}
                      onClick={() => addRecipient(contact, "to")}
                      className="flex flex-col items-start py-2"
                    >
                      <span className="font-medium">{contact.name}</span>
                      <span className="text-xs text-muted-foreground">
                        {contact.email}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </div>
              </DropdownMenuContent>
            </DropdownMenu>
            <input
              className="flex-1 min-w-[160px] bg-transparent text-sm outline-none placeholder:text-muted-foreground"
              placeholder={t("compose.searchOrType")}
              value={recipientInput.to}
              onChange={(e) => {
                setRecipientInput((p) => ({ ...p, to: e.target.value }))
                setSearchQuery(e.target.value)
                setActiveField("to")
              }}
              onFocus={() => setActiveField("to")}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === ",") {
                  e.preventDefault()
                  addTypedRecipient("to")
                }
              }}
              onBlur={() => addTypedRecipient("to")}
            />
            {suggestionsPanel("to")}
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="text-xs h-7"
            onClick={() => setShowCc(!showCc)}
          >
            {t("common.cc")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-xs h-7"
            onClick={() => setShowBcc(!showBcc)}
          >
            {t("common.bcc")}
          </Button>
        </div>

        {showCc && (
          <div className="flex items-center gap-2">
            <span className="w-12 text-sm text-muted-foreground">{t("common.cc")}:</span>
            <div className="relative flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
              {cc.map((r) => (
                <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                  {r.name}
                  <button
                    onClick={() => removeRecipient(r.id, "cc")}
                    className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" className="h-6 w-6">
                    <Plus className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-72">
                  <div className="p-2">
                    <Input
                      placeholder={t("compose.searchPeople")}
                      value={searchQuery}
                      onChange={(e) => setSearchQuery(e.target.value)}
                    />
                  </div>
                  <Separator />
                  <div className="max-h-48 overflow-auto">
                    {suggestions.map((contact) => (
                      <DropdownMenuItem
                        key={contact.id}
                        onClick={() => addRecipient(contact, "cc")}
                        className="flex flex-col items-start py-2"
                      >
                        <span className="font-medium">{contact.name}</span>
                        <span className="text-xs text-muted-foreground">
                          {contact.email}
                        </span>
                      </DropdownMenuItem>
                    ))}
                  </div>
                </DropdownMenuContent>
              </DropdownMenu>
              <input
                className="flex-1 min-w-[160px] bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                placeholder={t("compose.searchOrType")}
                value={recipientInput.cc}
                onChange={(e) => {
                  setRecipientInput((p) => ({ ...p, cc: e.target.value }))
                  setSearchQuery(e.target.value)
                  setActiveField("cc")
                }}
                onFocus={() => setActiveField("cc")}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === ",") {
                    e.preventDefault()
                    addTypedRecipient("cc")
                  }
                }}
                onBlur={() => addTypedRecipient("cc")}
              />
              {suggestionsPanel("cc")}
            </div>
          </div>
        )}

        {showBcc && (
          <div className="flex items-center gap-2">
            <span className="w-12 text-sm text-muted-foreground">{t("common.bcc")}:</span>
            <div className="relative flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
              {bcc.map((r) => (
                <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                  {r.name}
                  <button
                    onClick={() => removeRecipient(r.id, "bcc")}
                    className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" className="h-6 w-6">
                    <Plus className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-72">
                  <div className="p-2">
                    <Input
                      placeholder={t("compose.searchPeople")}
                      value={searchQuery}
                      onChange={(e) => setSearchQuery(e.target.value)}
                    />
                  </div>
                  <Separator />
                  <div className="max-h-48 overflow-auto">
                    {suggestions.map((contact) => (
                      <DropdownMenuItem
                        key={contact.id}
                        onClick={() => addRecipient(contact, "bcc")}
                        className="flex flex-col items-start py-2"
                      >
                        <span className="font-medium">{contact.name}</span>
                        <span className="text-xs text-muted-foreground">
                          {contact.email}
                        </span>
                      </DropdownMenuItem>
                    ))}
                  </div>
                </DropdownMenuContent>
              </DropdownMenu>
              <input
                className="flex-1 min-w-[160px] bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                placeholder={t("compose.searchOrType")}
                value={recipientInput.bcc}
                onChange={(e) => {
                  setRecipientInput((p) => ({ ...p, bcc: e.target.value }))
                  setSearchQuery(e.target.value)
                  setActiveField("bcc")
                }}
                onFocus={() => setActiveField("bcc")}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === ",") {
                    e.preventDefault()
                    addTypedRecipient("bcc")
                  }
                }}
                onBlur={() => addTypedRecipient("bcc")}
              />
              {suggestionsPanel("bcc")}
            </div>
          </div>
        )}

        {/* Sender Identity Selector */}
        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground flex items-center gap-1">
            <Mail className="h-3 w-3" />
            {t("common.from")}:
          </span>
          <div className="flex-1 flex items-center gap-2">
            <DropdownMenu open={showSenderDropdown} onOpenChange={setShowSenderDropdown}>
              <DropdownMenuTrigger asChild>
                <Button 
                  variant="outline" 
                  size="sm" 
                  className={cn(
                    "h-7 text-xs gap-1",
                    !canSendAsSelected && "border-red-500 text-red-500"
                  )}
                >
                  {selectedSender ? (
                    <>
                      <span className="truncate max-w-[150px]">
                        {selectedSender.displayName || selectedSender.email}
                      </span>
                      {selectedSender.type !== 'personal' && (
                        <Badge variant="secondary" className="text-[10px] h-4 ml-1">
                          {selectedSender.type === 'send-on-behalf' ? t("compose.onBehalf") : t("compose.sendAs")}
                        </Badge>
                      )}
                    </>
                  ) : (
                    <span>{t("compose.selectSender")}</span>
                  )}
                  <ChevronDown className="h-3 w-3 ml-1" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-80">
                <div className="p-2 text-xs text-muted-foreground">
                  {t("compose.selectSenderIdentity")}
                </div>
                <Separator />
                <div className="max-h-60 overflow-auto">
                  {senderIdentities.map((identity) => (
                    <DropdownMenuItem
                      key={identity.email}
                      onClick={() => {
                        setSelectedSender(identity)
                        setShowSenderDropdown(false)
                      }}
                      className={cn(
                        "flex flex-col items-start py-2 cursor-pointer",
                        !identity.canSend && "opacity-50"
                      )}
                      disabled={!identity.canSend}
                    >
                      <div className="flex items-center gap-2 w-full">
                        <span className="font-medium text-sm">{identity.displayName || identity.email}</span>
                        {identity.type === 'personal' && (
                          <Badge variant="default" className="text-[10px] h-4">{t("nav.personal")}</Badge>
                        )}
                        {identity.type === 'send-on-behalf' && (
                          <Badge variant="secondary" className="text-[10px] h-4">{t("compose.onBehalf")}</Badge>
                        )}
                        {identity.type === 'send-as' && (
                          <Badge variant="outline" className="text-[10px] h-4">{t("compose.sendAs")}</Badge>
                        )}
                      </div>
                      {identity.mailboxOwner && (
                        <span className="text-xs text-muted-foreground">
                          {t("compose.sharedMailbox", { owner: identity.mailboxOwner })}
                        </span>
                      )}
                      {!identity.canSend && (
                        <span className="text-xs text-red-500 flex items-center gap-1 mt-1">
                          <AlertTriangle className="h-3 w-3" />
                          {t("compose.noSendPermissionShort")}
                        </span>
                      )}
                    </DropdownMenuItem>
                  ))}
                </div>
              </DropdownMenuContent>
            </DropdownMenu>
            
            {/* Show permission error inline */}
            {sendError && (
              <div className="flex items-center gap-1 text-xs text-red-500">
                <AlertTriangle className="h-3 w-3" />
                <span className="truncate max-w-[200px]">{sendError}</span>
              </div>
            )}
            
            {/* Diagnostics toggle */}
            {diagnostics.length > 0 && (
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6"
                onClick={() => setShowDiagnostics(!showDiagnostics)}
                title={t("compose.viewDiagnostics")}
              >
                <AlertTriangle className={cn(
                  "h-4 w-4",
                  diagnostics.some(d => d.severity === 'error') && "text-red-500"
                )} />
              </Button>
            )}
          </div>
        </div>
        
        {/* Diagnostics Panel */}
        {showDiagnostics && diagnostics.length > 0 && (
          <div className="border rounded-md bg-muted/30 p-3 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">{t("compose.mailboxDiagnostics")}</span>
              <Button
                variant="ghost"
                size="icon"
                className="h-5 w-5"
                onClick={() => setShowDiagnostics(false)}
              >
                <X className="h-3 w-3" />
              </Button>
            </div>
            {diagnostics.map((entry) => (
              <div 
                key={entry.id}
                className={cn(
                  "text-xs p-2 rounded border-l-2",
                  entry.severity === 'error' && "bg-red-50 border-red-500 text-red-700 dark:bg-red-950 dark:text-red-400",
                  entry.severity === 'warning' && "bg-yellow-50 border-yellow-500 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-400",
                  entry.severity === 'info' && "bg-blue-50 border-blue-500 text-blue-700 dark:bg-blue-950 dark:text-blue-400"
                )}
              >
                <div className="flex items-start gap-2">
                  <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
                  <div className="flex-1">
                    <div className="font-medium">{entry.message}</div>
                    {entry.mailbox && (
                      <div className="text-muted-foreground mt-0.5">{t("compose.mailboxLabel", { mailbox: entry.mailbox })}</div>
                    )}
                    {entry.nextStep && (
                      <div className="text-muted-foreground mt-1 flex items-center gap-1">
                        <span>{t("compose.nextStep")}</span>
                        <span className="font-medium">{entry.nextStep}</span>
                      </div>
                    )}
                    <div className="text-muted-foreground mt-1">
                      {new Date(entry.timestamp).toLocaleString([], withTz())}
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground">{t("compose.subjectShort")}:</span>
          <Input
            className="flex-1 border-0 shadow-none focus-visible:ring-0 px-0 py-1 h-8"
            placeholder={t("common.subject")}
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
          />
        </div>
      </div>

      {/* Formatting Toolbar */}
      <div className="flex items-center gap-1 border-b px-4 py-1 bg-muted/30">
        {/* onMouseDown preventDefault keeps focus (and the selection) in the
            body textarea so applyFormat wraps the selected text instead of
            inserting a placeholder. */}
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.bold")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("bold")}>
          <Bold className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.italic")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("italic")}>
          <Italic className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.underline")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("underline")}>
          <Underline className="h-4 w-4" />
        </Button>
        <Separator orientation="vertical" className="h-6" />
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.insertLink")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("link")}>
          <Link className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.bulletList")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("list")}>
          <List className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t("compose.insertImageLink")} onMouseDown={(e) => e.preventDefault()} onClick={() => applyFormat("image")}>
          <Image className="h-4 w-4" />
        </Button>
        <span className="text-xs text-muted-foreground ml-2">
          {t("compose.sendTip")}
        </span>
      </div>

      {/* Body */}
      <div className="flex-1 overflow-hidden">
        {richTextMode ? (
          <RichTextEditor
            ref={richTextRef}
            value={body}
            onChange={setBody}
            placeholder={t("compose.writeMessage")}
            className="h-full"
          />
        ) : (
          <Textarea
            ref={bodyRef}
            className="h-full resize-none border-0 shadow-none focus-visible:ring-0 p-4"
            placeholder={t("compose.writeMessage")}
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
        )}
      </div>

      {/* Attachments & Footer */}
      {attachments.length > 0 && (
        <div className="border-t px-4 py-2 bg-muted/30">
          <div className="flex flex-wrap gap-2">
            {attachments.map((att) => (
              <div
                key={att.id}
                className="flex items-center gap-2 rounded border bg-background px-3 py-1.5"
              >
                <Paperclip className="h-4 w-4 text-muted-foreground" />
                <span className="text-sm">{att.name}</span>
                <span className="text-xs text-muted-foreground">
                  ({formatSize(att.size)})
                </span>
                <button
                  onClick={() => removeAttachment(att.id)}
                  className="ml-1 rounded-full hover:bg-muted p-0.5"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="flex items-center justify-between border-t px-4 py-2">
        <div className="flex items-center gap-2">
          <Button variant="outline" size="icon" onClick={() => fileInputRef.current?.click()}>
            <Paperclip className="h-5 w-5" />
          </Button>
          <input
            type="file"
            multiple
            ref={fileInputRef}
            className="hidden"
            onChange={handleAttach}
          />
          <Button
            type="button"
            variant={requestReadReceipt ? "secondary" : "outline"}
            size="sm"
            onClick={() => setRequestReadReceipt((v) => !v)}
            title={t("compose.requestReadReceipt")}
            aria-pressed={requestReadReceipt}
          >
            <Check className={requestReadReceipt ? "mr-1.5 h-4 w-4" : "mr-1.5 h-4 w-4 opacity-40"} />
            {t("compose.readReceipt")}
          </Button>
          <Button
            type="button"
            variant={signMessage ? "secondary" : "outline"}
            size="sm"
            onClick={() => setSignMessage((v) => !v)}
            title={t("compose.signMessage")}
            aria-pressed={signMessage}
          >
            <Shield className={signMessage ? "mr-1.5 h-4 w-4" : "mr-1.5 h-4 w-4 opacity-40"} />
            {t("compose.signMessage")}
          </Button>
          <Button
            type="button"
            variant={encryptMessage ? "secondary" : "outline"}
            size="sm"
            onClick={() => setEncryptMessage((v) => !v)}
            title={t("compose.encryptMessage")}
            aria-pressed={encryptMessage}
          >
            <Key className={encryptMessage ? "mr-1.5 h-4 w-4" : "mr-1.5 h-4 w-4 opacity-40"} />
            {t("compose.encryptMessage")}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant={importance !== "normal" ? "secondary" : "outline"}
                size="sm"
                title={t("compose.messageImportance")}
              >
                <Gauge className={importance === "normal" ? "mr-1.5 h-4 w-4 opacity-40" : "mr-1.5 h-4 w-4"} />
                {importance === "high" ? t("compose.importanceHigh") : importance === "low" ? t("compose.importanceLow") : t("compose.importanceNormal")}
                <ChevronDown className="ml-1 h-3 w-3 opacity-50" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setImportance("low")}>
                <span className="mr-2 text-muted-foreground">{t("compose.importanceLow")}</span>
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setImportance("normal")}>
                <span className="mr-2 text-muted-foreground">{t("compose.importanceNormal")}</span>
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setImportance("high")}>
                <span className="mr-2 font-medium">{t("compose.importanceHigh")}</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
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
