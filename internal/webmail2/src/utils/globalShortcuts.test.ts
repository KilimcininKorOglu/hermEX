import { describe, expect, it } from "vitest"
import { shortcutAction } from "./globalShortcuts"

const press = (key: string, ctrl = false, shift = false) => ({ key, ctrl, shift })

describe("shortcutAction", () => {
  it.each([
    [press("n", true), { navigate: "/compose", preventDefault: true }],
    [press("I", true, true), { navigate: "/inbox", preventDefault: true }],
    [press("/"), { navigate: "/search", preventDefault: true }],
    [press("k", true), { navigate: "/search", preventDefault: true }],
    [press("?", false, true), { event: "toggle-shortcuts", preventDefault: true }],
    [press("Escape"), { event: "close-dialogs", preventDefault: false }],
  ])("binds basic shortcut %o in basic mode", (k, want) => {
    expect(shortcutAction("basic", k)).toEqual(want)
    expect(shortcutAction("extended", k)).toEqual(want)
  })

  it.each([
    ["1", "/inbox"],
    ["2", "/sent"],
    ["3", "/drafts"],
    ["4", "/trash"],
  ])("binds Ctrl+%s to %s only in extended mode", (key, path) => {
    expect(shortcutAction("extended", press(key, true))).toEqual({ navigate: path, preventDefault: true })
    expect(shortcutAction("basic", press(key, true))).toBeNull()
  })

  it("binds nothing when shortcuts are off", () => {
    expect(shortcutAction("off", press("n", true))).toBeNull()
    expect(shortcutAction("off", press("Escape"))).toBeNull()
  })

  it("ignores keys without a binding, including '/' with Ctrl and a bare 'g'", () => {
    expect(shortcutAction("extended", press("/", true))).toBeNull()
    expect(shortcutAction("extended", press("g"))).toBeNull()
    expect(shortcutAction("extended", press("n"))).toBeNull()
  })

  it("keeps Ctrl+N on compose when Shift is also held", () => {
    expect(shortcutAction("basic", press("N", true, true))).toEqual({ navigate: "/compose", preventDefault: true })
  })
})
