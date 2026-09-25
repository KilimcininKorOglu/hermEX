import { basicEnabled, extendedEnabled, type ShortcutMode } from "@/utils/shortcutMode"

// ShortcutKey is the part of a keydown event the global shortcuts read.
export interface ShortcutKey {
  key: string
  ctrl: boolean
  shift: boolean
}

// ShortcutAction is what a matched shortcut does: open a route, or dispatch a
// document event. preventDefault says whether the browser's own action is cancelled.
export type ShortcutAction =
  | { navigate: string; preventDefault: true }
  | { event: string; preventDefault: boolean }

interface Rule {
  level: "basic" | "extended"
  matches: (k: ShortcutKey) => boolean
  action: ShortcutAction
}

const go = (path: string): ShortcutAction => ({ navigate: path, preventDefault: true })
const ctrlKey = (key: string) => (k: ShortcutKey) => k.ctrl && k.key === key

// RULES lists the global shortcuts in match order; the first rule that matches
// wins. Ctrl stands for Ctrl or Cmd. Keys are lower-cased before matching.
const RULES: Rule[] = [
  { level: "basic", matches: ctrlKey("n"), action: go("/compose") },
  { level: "basic", matches: (k) => k.ctrl && k.shift && k.key === "i", action: go("/inbox") },
  { level: "basic", matches: (k) => !k.ctrl && k.key === "/", action: go("/search") },
  { level: "extended", matches: ctrlKey("1"), action: go("/inbox") },
  { level: "extended", matches: ctrlKey("2"), action: go("/sent") },
  { level: "extended", matches: ctrlKey("3"), action: go("/drafts") },
  { level: "extended", matches: ctrlKey("4"), action: go("/trash") },
  { level: "basic", matches: ctrlKey("k"), action: go("/search") },
  { level: "basic", matches: (k) => k.shift && k.key === "?", action: { event: "toggle-shortcuts", preventDefault: true } },
  { level: "basic", matches: (k) => k.key === "escape", action: { event: "close-dialogs", preventDefault: false } },
]

// levelEnabled reports whether a rule of the given level fires under mode.
function levelEnabled(mode: ShortcutMode, level: Rule["level"]): boolean {
  return level === "basic" ? basicEnabled(mode) : extendedEnabled(mode)
}

// shortcutAction returns the action a key press triggers under mode, or null.
export function shortcutAction(mode: ShortcutMode, k: ShortcutKey): ShortcutAction | null {
  const key = { ...k, key: k.key.toLowerCase() }
  const rule = RULES.find((r) => levelEnabled(mode, r.level) && r.matches(key))
  return rule ? rule.action : null
}
