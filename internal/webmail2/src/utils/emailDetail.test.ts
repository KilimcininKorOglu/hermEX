import { describe, expect, it } from "vitest"
import type { Mail } from "@/utils/api"
import {
  emailDetailOf,
  followupPatch,
  proposalRange,
  proposeWindow,
  readerShortcut,
  recallOutcome,
  replySubject,
  senderInitials,
  toDatetimeLocal,
  type EmailDetail,
} from "./emailDetail"

const mail: Mail = {
  id: "inbox:7", from: "ada@hermex.test", fromName: "", to: ["bob@hermex.test"], subject: "Hi", body: "b", preview: "",
  date: "2026-09-25T10:00:00Z", read: true, starred: true, folder: "Inbox", hasAttachments: false, size: 1,
  smimeEncrypted: true, followupStatus: 2, followupColor: 3, followupDue: "2026-09-30T10:00:00Z",
}

describe("emailDetailOf", () => {
  it("falls back to the address for a sender without a name and reads absent lists as empty", () => {
    const email = emailDetailOf(mail, { content: "<p>x</p>", smimeSigned: true, smimeVerified: false, smimeSignedBy: "ada@hermex.test" })
    expect(email).toEqual({
      id: "inbox:7", from: "ada@hermex.test", fromEmail: "ada@hermex.test", to: ["bob@hermex.test"], toNames: [], cc: [], ccNames: [],
      subject: "Hi", date: "2026-09-25T10:00:00Z", content: "<p>x</p>", flagged: true, followupStatus: 2, followupColor: 3,
      followupDue: "2026-09-30T10:00:00Z", labels: [], attachments: [], folder: "Inbox", smimeSigned: true, smimeEncrypted: true,
      smimeVerified: false, smimeSignedBy: "ada@hermex.test",
    })
    expect(emailDetailOf({ ...mail, fromName: "Ada" }, { content: "" }).from).toBe("Ada")
  })
})

describe("followupPatch", () => {
  const email = { followupColor: 3, followupDue: "2026-09-30T10:00:00Z" } as EmailDetail

  it("flags with a new colour and due date or keeps the current ones", () => {
    expect(followupPatch(email, "flag", 5, "2026-10-01T00:00:00Z")).toEqual({ flagged: true, followupStatus: 2, followupColor: 5, followupDue: "2026-10-01T00:00:00Z" })
    expect(followupPatch(email, "flag")).toEqual({ flagged: true, followupStatus: 2, followupColor: 3, followupDue: "2026-09-30T10:00:00Z" })
  })

  it("completes or clears, keeping the colour and dropping the due date", () => {
    expect(followupPatch(email, "complete", 5)).toEqual({ flagged: false, followupStatus: 1, followupColor: 3, followupDue: "" })
    expect(followupPatch(email, "clear")).toEqual({ flagged: false, followupStatus: 0, followupColor: 3, followupDue: "" })
  })
})

describe("recallOutcome", () => {
  it("reports no recipients, all, none and a part", () => {
    expect(recallOutcome({ recalled: 0, total: 0 })).toEqual({ level: "info", key: "emailDetail.recallNoRecipients" })
    expect(recallOutcome({ recalled: 2, total: 2 })).toEqual({ level: "success", key: "emailDetail.recallAll", params: { count: "2" } })
    expect(recallOutcome({ recalled: 0, total: 2 })).toEqual({ level: "warning", key: "emailDetail.recallNone" })
    expect(recallOutcome({ recalled: 1, total: 3 })).toEqual({ level: "warning", key: "emailDetail.recallPartial", params: { recalled: "1", total: "3" } })
  })
})

describe("readerShortcut", () => {
  const key = (k: string, extra: Partial<{ metaKey: boolean; ctrlKey: boolean; altKey: boolean; target: EventTarget | null }> = {}) =>
    ({ key: k, metaKey: false, ctrlKey: false, altKey: false, target: null, ...extra })

  it("maps r and a in every mode but off", () => {
    expect(readerShortcut(key("r"), "basic")).toBe("reply")
    expect(readerShortcut(key("a"), "extended")).toBe("replyAll")
    expect(readerShortcut(key("r"), "off")).toBeNull()
  })

  it("maps F, # and U only in the extended mode", () => {
    expect(readerShortcut(key("F"), "extended")).toBe("forward")
    expect(readerShortcut(key("#"), "extended")).toBe("delete")
    expect(readerShortcut(key("u"), "extended")).toBe("markUnread")
    expect(readerShortcut(key("f"), "basic")).toBeNull()
    expect(readerShortcut(key("x"), "extended")).toBeNull()
  })

  it("ignores a key with a modifier or typed into a field", () => {
    expect(readerShortcut(key("r", { ctrlKey: true }), "basic")).toBeNull()
    const input = document.createElement("input")
    expect(readerShortcut(key("r", { target: input }), "basic")).toBeNull()
  })
})

describe("propose-new-time", () => {
  const local = (h: number, m: number) => new Date(2026, 8, 25, h, m)

  it("prefills the invite's window and defaults the end to an hour later", () => {
    expect(proposeWindow({ start: local(10, 0).toISOString(), end: local(10, 30).toISOString() })).toEqual({ start: "2026-09-25T10:00", end: "2026-09-25T10:30" })
    expect(proposeWindow({ start: local(10, 0).toISOString() })).toEqual({ start: "2026-09-25T10:00", end: "2026-09-25T11:00" })
  })

  it("sends instants and defaults an empty end to an hour after the start", () => {
    expect(proposalRange("2026-09-25T10:00", "")).toEqual({ start: local(10, 0).toISOString(), end: local(11, 0).toISOString() })
    expect(proposalRange("2026-09-25T10:00", "2026-09-25T12:00")).toEqual({ start: local(10, 0).toISOString(), end: local(12, 0).toISOString() })
  })
})

describe("small helpers", () => {
  it("prefixes Re: once, takes initials and renders a local input value", () => {
    expect(replySubject("Hi")).toBe("Re: Hi")
    expect(replySubject("Re: Hi")).toBe("Re: Hi")
    expect(senderInitials("Ada Byron Lovelace")).toBe("AB")
    expect(toDatetimeLocal(undefined)).toBe("")
    expect(toDatetimeLocal("nope")).toBe("")
  })
})
