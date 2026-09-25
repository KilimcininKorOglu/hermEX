import type { AttachmentInfo, Mail, MeetingInvite } from "@/utils/api"

// EmailDetail is the reading view's model of one message.
export interface EmailDetail {
  id: string
  from: string
  fromEmail: string
  to: string[]
  toNames: string[]
  cc?: string[]
  ccNames?: string[]
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

// BodyView is the body the reader renders plus the S/MIME signature state,
// which a client-side decryption can override.
export interface BodyView {
  content: string
  smimeSigned?: boolean
  smimeVerified?: boolean
  smimeSignedBy?: string
}

// emailDetailOf builds the reading view's model from the API message and the
// body the reader renders. The API returns a bare sender address (from) and a
// resolved display name (fromName, "" when unknown); recipients come as bare
// addresses with names at the same index.
export function emailDetailOf(result: Mail, view: BodyView): EmailDetail {
  return {
    id: result.id,
    from: result.fromName || result.from,
    fromEmail: result.from,
    to: result.to ?? [],
    toNames: result.toNames ?? [],
    cc: result.cc ?? [],
    ccNames: result.ccNames ?? [],
    subject: result.subject,
    date: result.date,
    content: view.content,
    flagged: !!result.starred,
    followupStatus: result.followupStatus,
    followupColor: result.followupColor,
    followupDue: result.followupDue,
    labels: result.labels ?? [],
    attachments: result.attachments ?? [],
    folder: result.folder ?? "",
    smimeSigned: view.smimeSigned,
    smimeEncrypted: result.smimeEncrypted,
    smimeVerified: view.smimeVerified,
    smimeSignedBy: view.smimeSignedBy,
  }
}

export type FollowupAction = "flag" | "complete" | "clear"

// FOLLOWUP_STATUS is the follow-up flag status each action leaves: 2 flagged,
// 1 complete, 0 none.
const FOLLOWUP_STATUS: Record<FollowupAction, number> = { flag: 2, complete: 1, clear: 0 }

// FOLLOWUP_TOAST_KEYS is the i18n key each follow-up action confirms with.
export const FOLLOWUP_TOAST_KEYS: Record<FollowupAction, string> = {
  flag: "emailDetail.flaggedForFollowUp",
  complete: "emailDetail.followUpCompleted",
  clear: "emailDetail.followUpCleared",
}

// followupPatch returns the fields a follow-up action changes. Flagging keeps
// the current colour and due date unless new ones are given; completing keeps
// the colour and drops the due date; clearing does the same.
export function followupPatch(email: EmailDetail, action: FollowupAction, color?: number, due?: string): Partial<EmailDetail> {
  const flagging = action === "flag"
  return {
    flagged: flagging,
    followupStatus: FOLLOWUP_STATUS[action],
    followupColor: flagging ? color ?? email.followupColor : email.followupColor,
    followupDue: flagging ? due ?? email.followupDue : "",
  }
}

// RecallOutcome is how the reader reports a recall: the toast level and the
// i18n key with its parameters.
export interface RecallOutcome {
  level: "info" | "success" | "warning"
  key: string
  params?: Record<string, string>
}

// recallOutcome maps a recall result to the toast that reports it.
export function recallOutcome(res: { recalled: number; total: number }): RecallOutcome {
  if (res.total === 0) return { level: "info", key: "emailDetail.recallNoRecipients" }
  if (res.recalled === res.total) return { level: "success", key: "emailDetail.recallAll", params: { count: String(res.recalled) } }
  if (res.recalled === 0) return { level: "warning", key: "emailDetail.recallNone" }
  return { level: "warning", key: "emailDetail.recallPartial", params: { recalled: String(res.recalled), total: String(res.total) } }
}

export type ReaderAction = "reply" | "replyAll" | "forward" | "delete" | "markUnread"

// BASIC_KEYS are the reading-view shortcuts every shortcut mode offers.
const BASIC_KEYS: Record<string, ReaderAction> = { r: "reply", a: "replyAll" }
// EXTENDED_KEYS are the shortcuts only the extended mode offers.
const EXTENDED_KEYS: Record<string, ReaderAction> = { f: "forward", F: "forward", "#": "delete", u: "markUnread", U: "markUnread" }

function isTypingTarget(el: HTMLElement | null): boolean {
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)
}

// readerShortcut maps a key press to a reading-view action under the shortcut
// mode: r/a are basic, F/#/U are extended. A key typed into a field, a key with
// a modifier, and every key in the "off" mode map to nothing.
export function readerShortcut(
  e: { key: string; metaKey: boolean; ctrlKey: boolean; altKey: boolean; target: EventTarget | null },
  mode: string,
): ReaderAction | null {
  if (mode === "off" || isTypingTarget(e.target as HTMLElement | null)) return null
  if (e.metaKey || e.ctrlKey || e.altKey) return null
  const extended = mode === "extended" ? EXTENDED_KEYS[e.key] : undefined
  return BASIC_KEYS[e.key] ?? extended ?? null
}

// toDatetimeLocal converts an RFC3339 instant to the "YYYY-MM-DDTHH:mm" value a
// native datetime-local input expects, in the browser's local zone.
export function toDatetimeLocal(iso?: string): string {
  if (!iso) return ""
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const HOUR_MS = 60 * 60 * 1000

// proposeWindow prefills the propose-new-time inputs with the invite's window:
// its start (now when absent) and its end (an hour later when absent).
export function proposeWindow(invite: Pick<MeetingInvite, "start" | "end">): { start: string; end: string } {
  const s = invite.start ? new Date(invite.start) : new Date()
  const e = invite.end ? new Date(invite.end) : new Date(s.getTime() + HOUR_MS)
  return { start: toDatetimeLocal(s.toISOString()), end: toDatetimeLocal(e.toISOString()) }
}

// proposalRange turns the propose-new-time inputs into the instants sent to
// the organizer; an empty end means an hour after the start.
export function proposalRange(start: string, end: string): { start: string; end: string } {
  const s = new Date(start)
  const e = end ? new Date(end) : new Date(s.getTime() + HOUR_MS)
  return { start: s.toISOString(), end: e.toISOString() }
}

// replySubject prefixes "Re: " once.
export function replySubject(subject: string): string {
  return subject.startsWith("Re: ") ? subject : `Re: ${subject}`
}

// senderInitials takes the first letter of up to two words of the sender name.
export function senderInitials(name: string): string {
  return name.split(" ").map((n) => n[0]).join("").slice(0, 2)
}
