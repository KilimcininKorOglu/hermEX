import { describe, expect, it } from "vitest"
import { sharedSenderIdentity } from "@/utils/senderIdentity"

const team = { owner: "team@hermex.test", mailbox: "team@hermex.test" }

describe("sharedSenderIdentity", () => {
  it("offers a send-as grant as a send-as identity", () => {
    const id = sharedSenderIdentity({ ...team, sendGrant: "send-as" })
    expect(id.type).toBe("send-as")
    expect(id.canSend).toBe(true)
  })

  it("offers an on-behalf grant as a send-on-behalf identity", () => {
    const id = sharedSenderIdentity({ ...team, sendGrant: "on-behalf" })
    expect(id.type).toBe("send-on-behalf")
    expect(id.canSend).toBe(true)
  })

  it("keeps a mailbox without a send grant unpickable", () => {
    expect(sharedSenderIdentity({ ...team, sendGrant: "none" }).canSend).toBe(false)
    expect(sharedSenderIdentity(team).canSend).toBe(false)
  })
})
