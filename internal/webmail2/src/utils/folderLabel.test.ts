import { describe, expect, it } from "vitest"
import { folderLabel, type UserFolder } from "@/utils/api"

// Folder names are unique only among siblings, so a list naming two folders
// "2024" must label them differently or the user cannot tell which is which.
describe("folderLabel", () => {
  const projects: UserFolder = { id: "f40", name: "Projects", parent: "" }
  const old: UserFolder = { id: "f41", name: "Old", parent: "" }
  const a: UserFolder = { id: "f42", name: "2024", parent: "f40" }
  const b: UserFolder = { id: "f43", name: "2024", parent: "f41" }
  const all = [projects, old, a, b]

  it("names a folder of a unique name by its name alone", () => {
    expect(folderLabel(projects, all)).toBe("Projects")
  })

  it("tells two folders of one name apart by their parents", () => {
    expect(folderLabel(a, all)).toBe("Projects / 2024")
    expect(folderLabel(b, all)).toBe("Old / 2024")
  })

  it("names a built-in parent by its slug", () => {
    const underInbox: UserFolder = { id: "f50", name: "2024", parent: "inbox" }
    expect(folderLabel(underInbox, [...all, underInbox])).toBe("inbox / 2024")
  })
})
