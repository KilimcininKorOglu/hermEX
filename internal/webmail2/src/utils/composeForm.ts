import type { DiagnosticEntry, SenderIdentity, SignatureEntry, TemplateEntry } from "@/utils/api"

// Recipient is one address chip in a recipient field.
export interface Recipient {
  id: string
  name: string
  email: string
}

export type RecipientField = "to" | "cc" | "bcc"

// ADDRESS_SHAPE is the basic shape a typed address must have before it becomes a
// chip; the server parses it properly on send.
const ADDRESS_SHAPE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// typedAddress cleans what the user typed into a recipient field: surrounding
// space and the separator that committed it.
export function typedAddress(raw: string): string {
  return raw.trim().replace(/[,;]+$/, "").trim()
}

// isAddress reports whether a cleaned address has the basic shape of one.
export function isAddress(email: string): boolean {
  return ADDRESS_SHAPE.test(email)
}

// typedRecipient makes a chip from a typed address.
export function typedRecipient(field: RecipientField, email: string): Recipient {
  return { id: `typed-${field}-${email}`, name: email, email }
}

// withRecipient adds a recipient to a field unless the same entry is already there.
export function withRecipient(list: Recipient[], r: Recipient): Recipient[] {
  return list.some((x) => x.id === r.id) ? list : [...list, r]
}

// dedupeRecipients keeps the first recipient per address, compared without case.
export function dedupeRecipients(list: Recipient[]): Recipient[] {
  const seen = new Set<string>()
  return list.filter((r) => {
    const key = r.email.toLowerCase()
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

// withAutoCc appends the configured automatic Cc addresses (a comma-separated
// setting) that the list does not already hold.
export function withAutoCc(list: Recipient[], setting: string | undefined): Recipient[] {
  const have = new Set(list.map((r) => r.email.toLowerCase()))
  const extra = (setting ?? "").split(",").map((a) => a.trim()).filter(Boolean)
  const merged = [...list]
  for (const a of extra) {
    if (have.has(a.toLowerCase())) continue
    have.add(a.toLowerCase())
    merged.push({ id: `autoCc-${a}`, name: a, email: a })
  }
  return merged
}

// addressList splits a comma-separated URL parameter into addresses.
export function addressList(raw: string | null): string[] {
  return (raw ?? "").split(",").map((e) => e.trim()).filter(Boolean)
}

// ToastOutcome is a toast level plus the i18n key and parameters it shows.
export interface ToastOutcome {
  level: "error" | "success"
  key: string
  params?: Record<string, string>
}

// namesCheckedOutcome reports a check-names pass: nothing to check, all resolved,
// or how many resolved and how many did not.
export function namesCheckedOutcome(total: number, unresolved: number): ToastOutcome {
  if (total === 0) return { level: "error", key: "compose.noRecipientsToCheck" }
  if (unresolved === 0) return { level: "success", key: "compose.allNamesResolved" }
  return {
    level: "success",
    key: "compose.namesChecked",
    params: { resolved: String(total - unresolved), unresolved: String(unresolved) },
  }
}

// defaultSender picks the identity a new message starts from: the shared
// mailbox's own identity while one is open, otherwise the personal identity,
// otherwise the first one offered.
export function defaultSender(identities: SenderIdentity[], sharedOwner: string | null): SenderIdentity | null {
  const shared = sharedOwner ? identities.find((id) => id.email === sharedOwner && id.type !== "personal") : undefined
  const personal = sharedOwner ? undefined : identities.find((id) => id.type === "personal")
  return shared ?? personal ?? identities[0] ?? null
}

// ATTACHMENT_HINTS are words that suggest the writer meant to attach a file, so a
// send with no attachment is worth a confirm. "attach" covers attached and
// attachment; the Turkish forms are specific (not the bare "ek", which matches
// many words). Mirrors the server-rendered webmail.
const ATTACHMENT_HINTS = ["attach", "ekli", "ekte", "iliştir"]

// mentionsAttachment reports whether any field hints at an intended attachment.
export function mentionsAttachment(...fields: string[]): boolean {
  return fields.some((f) => {
    const low = f.toLowerCase()
    return ATTACHMENT_HINTS.some((h) => low.includes(h))
  })
}

// hasPolicyError reports whether the mailbox diagnostics hold a policy error
// that has to be resolved before anything is sent.
function hasPolicyError(diagnostics: DiagnosticEntry[]): boolean {
  return diagnostics.some((d) => d.category === "policy" && d.severity === "error")
}

// SendDraft is the part of the composer a send is checked against.
export interface SendDraft {
  to: Recipient[]
  subject: string
  canSend: boolean
  // senderEmail is the selected identity, named when it may not be sent from.
  senderEmail?: string
  diagnostics: DiagnosticEntry[]
}

// SendBlock is why a send cannot start: an i18n key, and whether the
// diagnostics panel has to open to show the cause.
export interface SendBlock {
  key: string
  params?: Record<string, string>
  showDiagnostics?: boolean
}

// sendBlock returns the first reason the message cannot be sent, or null.
export function sendBlock(d: SendDraft): SendBlock | null {
  if (d.to.length === 0) return { key: "compose.selectRecipient" }
  if (!d.subject.trim()) return { key: "compose.enterSubject" }
  if (!d.canSend) {
    return d.senderEmail
      ? { key: "compose.noSendPermission", params: { email: d.senderEmail } }
      : { key: "compose.cannotSendIdentity" }
  }
  if (hasPolicyError(d.diagnostics)) return { key: "compose.resolveIssues", showDiagnostics: true }
  return null
}

// scheduledInstant turns the send-later value into an absolute instant. An empty
// value is an immediate send; a value that is not in the future is refused.
export function scheduledInstant(value: string, toISO: (v: string) => string | null, now: number): { iso?: string; error?: true } {
  if (!value) return {}
  const iso = toISO(value)
  if (!iso || new Date(iso).getTime() <= now) return { error: true }
  return { iso }
}

// SmimeRequest is what a send asks of S/MIME and where the key lives.
export interface SmimeRequest {
  sign: boolean
  encrypt: boolean
  scheduled: boolean
  browserKey: boolean
  unlocked: boolean
}

// smimeBlock returns the i18n key of the reason an S/MIME send cannot run, or
// null. A scheduled message is released by the server later, so it cannot carry
// a browser signature, and a browser key has to be unlocked to sign.
export function smimeBlock(r: SmimeRequest): string | null {
  if (!r.sign && !r.encrypt) return null
  if (r.scheduled) return "compose.smimeNoSchedule"
  if (r.browserKey && r.sign && !r.unlocked) return "compose.smimeLocked"
  return null
}

// uniqueAddresses lists the addresses once each, compared without case.
export function uniqueAddresses(list: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const addr of list) {
    const a = addr.trim().toLowerCase()
    if (!a || seen.has(a)) continue
    seen.add(a)
    out.push(addr)
  }
  return out
}

export type FormatKind = "bold" | "italic" | "underline" | "link" | "list" | "image"

// FORMAT_PLACEHOLDER_KEYS is the i18n key of the text each marker wraps when
// nothing is selected.
export const FORMAT_PLACEHOLDER_KEYS: Record<FormatKind, string> = {
  bold: "compose.boldText",
  italic: "compose.italicText",
  underline: "compose.underlinedText",
  link: "compose.linkText",
  image: "compose.imageText",
  list: "compose.listItem",
}

// formatText wraps a plain-text selection with the markdown-style marker of a
// format (the plain body is sent as text/plain); an empty selection is replaced
// by the placeholder.
export function formatText(kind: FormatKind, selected: string, placeholder: string): string {
  const text = selected || placeholder
  switch (kind) {
    case "bold":
      return `**${text}**`
    case "italic":
      return `*${text}*`
    case "underline":
      return `__${text}__`
    case "link":
      return `[${text}](https://)`
    case "image":
      return `![${text}](https://)`
    case "list":
      return text.split("\n").map((line) => `- ${line}`).join("\n")
  }
}

// formatSize renders a byte count for the attachment list.
export function formatSize(bytes: number): string {
  if (bytes < 1024) return bytes + " B"
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB"
  return (bytes / (1024 * 1024)).toFixed(1) + " MB"
}

// withSignature appends a signature under the standard "-- " separator.
export function withSignature(body: string, sig: SignatureEntry): string {
  const sep = sig.is_html ? "<br><br>-- <br>" : "\n\n-- \n"
  return `${body}${sep}${sig.body}`
}

// withTemplate appends a template's body, or uses it alone on an empty message.
export function withTemplate(body: string, tpl: TemplateEntry): string {
  if (!body) return tpl.body
  return `${body}${tpl.is_html ? "<br><br>" : "\n\n"}${tpl.body}`
}

// safeFileBase makes a file name base from a subject or name: characters a file
// name should not carry become "_", and the result is capped at 60 characters.
export function safeFileBase(name: string | undefined, fallback: string): string {
  return (name?.trim() || fallback).replace(/[^\w.\- ]+/g, "_").slice(0, 60)
}
