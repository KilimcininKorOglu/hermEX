import { getCookie, setCookie } from "@/utils/cookies"

// ShortcutMode is the keyboard-shortcut level the user picked (reference
// SettingsKeyShortcutWidget): "off" binds nothing, "basic" binds the essential
// set, "extended" binds every shortcut.
export type ShortcutMode = "off" | "basic" | "extended"

const COOKIE = "webmail-shortcut-mode"

// getShortcutMode reads the cached mode synchronously so key handlers can gate
// without an async settings fetch; the DB stays the source of truth and is
// mirrored here on settings load/save. Defaults to "extended" (all shortcuts).
export function getShortcutMode(): ShortcutMode {
  const v = getCookie(COOKIE)
  return v === "off" || v === "basic" || v === "extended" ? v : "extended"
}

// setShortcutMode mirrors the chosen mode to the cookie and notifies listeners
// (the key hooks) so a change in Settings takes effect without a reload.
export function setShortcutMode(mode: ShortcutMode): void {
  setCookie(COOKIE, mode)
  document.dispatchEvent(new CustomEvent("shortcut-mode-changed"))
}

// basicEnabled / extendedEnabled answer whether a shortcut of that level is
// active under the current mode. Basic shortcuts fire in both basic and extended;
// extended shortcuts fire only in extended.
export function basicEnabled(mode: ShortcutMode): boolean {
  return mode !== "off"
}
export function extendedEnabled(mode: ShortcutMode): boolean {
  return mode === "extended"
}
