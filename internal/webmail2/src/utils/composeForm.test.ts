import { describe, expect, it } from "vitest"
import type { DiagnosticEntry, SenderIdentity } from "@/utils/api"
import {
  addressList,
  dedupeRecipients,
  defaultSender,
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
  uniqueAddresses,
  withAutoCc,
  withRecipient,
  withSignature,
  withTemplate,
} from "./composeForm"

const r = (email: string, id = email) => ({ id, name: email, email })

describe("recipient helpers", () => {
  it("cleans a typed address and checks its shape", () => {
    expect(typedAddress("  bob@hermex.test,; ")).toBe("bob@hermex.test")
    expect(isAddress("bob@hermex.test")).toBe(true)
    expect(isAddress("bob@")).toBe(false)
  })

  it("adds a recipient once and dedupes by address without case", () => {
    expect(withRecipient([r("a@x.io")], r("a@x.io"))).toHaveLength(1)
    expect(withRecipient([r("a@x.io")], r("b@x.io"))).toHaveLength(2)
    expect(dedupeRecipients([r("A@x.io", "1"), r("a@x.io", "2")]).map((x) => x.id)).toEqual(["1"])
  })

  it("appends automatic Cc addresses the list does not hold", () => {
    const out = withAutoCc([r("boss@x.io")], " Boss@x.io, audit@x.io ,audit@x.io")
    expect(out.map((x) => x.email)).toEqual(["boss@x.io", "audit@x.io"])
    expect(withAutoCc([], undefined)).toEqual([])
  })

  it("splits a comma-separated address parameter", () => {
    expect(addressList(" a@x.io, ,b@x.io")).toEqual(["a@x.io", "b@x.io"])
    expect(addressList(null)).toEqual([])
  })

  it("lists each address once", () => {
    expect(uniqueAddresses(["a@x.io", "A@x.io", " ", "b@x.io"])).toEqual(["a@x.io", "b@x.io"])
  })
})

describe("namesCheckedOutcome", () => {
  it("reports nothing to check, all resolved, or the split", () => {
    expect(namesCheckedOutcome(0, 0)).toEqual({ level: "error", key: "compose.noRecipientsToCheck" })
    expect(namesCheckedOutcome(2, 0)).toEqual({ level: "success", key: "compose.allNamesResolved" })
    expect(namesCheckedOutcome(3, 1)).toEqual({ level: "success", key: "compose.namesChecked", params: { resolved: "2", unresolved: "1" } })
  })
})

describe("defaultSender", () => {
  const personal: SenderIdentity = { email: "alice@x.io", displayName: "Alice", type: "personal", canSend: true }
  const shared: SenderIdentity = { email: "sales@x.io", displayName: "Sales", type: "send-as", canSend: true }

  it("starts from the shared identity in a shared mailbox, else the personal one", () => {
    expect(defaultSender([personal, shared], "sales@x.io")).toBe(shared)
    expect(defaultSender([shared, personal], null)).toBe(personal)
    expect(defaultSender([personal, shared], "other@x.io")).toBe(personal)
    expect(defaultSender([], null)).toBeNull()
  })
})

describe("send checks", () => {
  const policy: DiagnosticEntry = { id: "1", category: "policy", severity: "error", message: "m", timestamp: "" } as DiagnosticEntry
  const draft = { to: [r("b@x.io")], subject: "Hi", canSend: true, diagnostics: [] as DiagnosticEntry[] }

  it("blocks in order: recipient, subject, identity, policy", () => {
    expect(sendBlock({ ...draft, to: [] })?.key).toBe("compose.selectRecipient")
    expect(sendBlock({ ...draft, subject: " " })?.key).toBe("compose.enterSubject")
    expect(sendBlock({ ...draft, canSend: false, senderEmail: "s@x.io" })).toEqual({ key: "compose.noSendPermission", params: { email: "s@x.io" } })
    expect(sendBlock({ ...draft, canSend: false })?.key).toBe("compose.cannotSendIdentity")
    expect(sendBlock({ ...draft, diagnostics: [policy] })).toEqual({ key: "compose.resolveIssues", showDiagnostics: true })
    expect(sendBlock(draft)).toBeNull()
  })

  it("accepts only a future send-later time", () => {
    const toISO = (v: string) => (v === "bad" ? null : v)
    expect(scheduledInstant("", toISO, 0)).toEqual({})
    expect(scheduledInstant("bad", toISO, 0)).toEqual({ error: true })
    expect(scheduledInstant("2026-01-01T00:00:00Z", toISO, Date.parse("2026-06-01T00:00:00Z"))).toEqual({ error: true })
    expect(scheduledInstant("2026-09-01T00:00:00Z", toISO, Date.parse("2026-06-01T00:00:00Z"))).toEqual({ iso: "2026-09-01T00:00:00Z" })
  })

  it("refuses S/MIME on a scheduled send and a locked browser key", () => {
    const base = { sign: true, encrypt: false, scheduled: false, browserKey: true, unlocked: true }
    expect(smimeBlock({ ...base, sign: false })).toBeNull()
    expect(smimeBlock({ ...base, scheduled: true })).toBe("compose.smimeNoSchedule")
    expect(smimeBlock({ ...base, unlocked: false })).toBe("compose.smimeLocked")
    expect(smimeBlock({ ...base, unlocked: false, browserKey: false })).toBeNull()
  })

  it("spots a mention of an attachment", () => {
    expect(mentionsAttachment("Report", "see the ATTACHED file")).toBe(true)
    expect(mentionsAttachment("dosya ekte")).toBe(true)
    expect(mentionsAttachment("nothing here")).toBe(false)
  })
})

describe("text helpers", () => {
  it("wraps a selection or the placeholder with a marker", () => {
    expect(formatText("bold", "x", "p")).toBe("**x**")
    expect(formatText("link", "", "p")).toBe("[p](https://)")
    expect(formatText("list", "a\nb", "p")).toBe("- a\n- b")
  })

  it("formats sizes and file name bases", () => {
    expect(formatSize(512)).toBe("512 B")
    expect(formatSize(2048)).toBe("2.0 KB")
    expect(formatSize(3 * 1024 * 1024)).toBe("3.0 MB")
    expect(safeFileBase(" Q3: plan/v2 ", "message")).toBe("Q3_ plan_v2")
    expect(safeFileBase("", "message")).toBe("message")
  })

  it("appends a signature or a template", () => {
    expect(withSignature("hi", { name: "d", body: "Ada", is_html: false, ord: 0 })).toBe("hi\n\n-- \nAda")
    expect(withTemplate("", { name: "t", subject: "", body: "T", is_html: false })).toBe("T")
    expect(withTemplate("hi", { name: "t", subject: "", body: "T", is_html: true })).toBe("hi<br><br>T")
  })
})
