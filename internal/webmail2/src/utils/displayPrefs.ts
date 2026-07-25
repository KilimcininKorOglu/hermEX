// Display-appearance preferences that render as document-root data attributes so
// index.css can key off them app-wide from first paint, mirroring the iconSet
// pattern. The authoritative values live in the DB-backed appearance settings;
// the theme provider applies them on mount and on every save.

// applyUnreadBorder reflects the "color the border of unread messages" toggle
// onto <html>; index.css draws a left accent border on unread message rows only
// while this is on.
export function applyUnreadBorder(on: boolean): void {
  const root = document.documentElement
  if (on) root.dataset.unreadBorder = "true"
  else delete root.dataset.unreadBorder
}
