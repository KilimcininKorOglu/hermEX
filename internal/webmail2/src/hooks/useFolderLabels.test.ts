import { describe, expect, it } from "vitest"
import { slugLabel } from "@/hooks/useFolderLabels"
import type { UserFolder } from "@/utils/api"

// A message's folder field and the folder route carry a slug ("inbox", "f256"),
// which is an address, not a name: what the user reads must be the folder's name.
describe("slugLabel", () => {
  const t = (key: string) => `[${key}]`
  const user: UserFolder[] = [
    { id: "f40", name: "Projects", parent: "" },
    { id: "f41", name: "Old", parent: "" },
    { id: "f42", name: "2024", parent: "f40" },
    { id: "f43", name: "2024", parent: "f41" },
  ]

  it("names a built-in folder by its translated name", () => {
    expect(slugLabel("inbox", user, t)).toBe("[nav.inbox]")
    expect(slugLabel("archive", user, t)).toBe("[nav.archive]")
  })

  it("names a user folder by its name, never its slug", () => {
    expect(slugLabel("f40", user, t)).toBe("Projects")
  })

  it("tells two user folders of one name apart", () => {
    expect(slugLabel("f42", user, t)).toBe("Projects / 2024")
    expect(slugLabel("f43", user, t)).toBe("Old / 2024")
  })

  it("leaves an unknown slug as it is", () => {
    expect(slugLabel("f999", user, t)).toBe("f999")
  })
})
