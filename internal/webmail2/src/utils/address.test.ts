import { describe, expect, it } from "vitest"
import { splitAddress } from "./address"

describe("splitAddress", () => {
  it("splits a display name from its address", () => {
    expect(splitAddress("Ada Lovelace <ada@hermex.test>")).toEqual({ name: "Ada Lovelace", email: "ada@hermex.test" })
  })

  it("uses the address as the name when the name is empty", () => {
    expect(splitAddress("<ada@hermex.test>")).toEqual({ name: "ada@hermex.test", email: "ada@hermex.test" })
  })

  it("uses a bare address for both parts", () => {
    expect(splitAddress("ada@hermex.test")).toEqual({ name: "ada@hermex.test", email: "ada@hermex.test" })
  })

  it("takes the address from the brackets at the end, not the first '<' in the value", () => {
    // Splitting on the first '<' read " b " as the address when the name holds one.
    expect(splitAddress("a < b <ada@hermex.test>")).toEqual({ name: "a < b", email: "ada@hermex.test" })
  })

  it("does not invent an address from an unterminated bracket", () => {
    expect(splitAddress("Ada <ada@hermex.test")).toEqual({ name: "Ada <ada@hermex.test", email: "Ada <ada@hermex.test" })
  })
})
