// Icon-set preference. The reference webmail ships two swappable icon themes
// (Breeze, the thin Fluent-based default, and Classic, a heavier look). This SPA
// draws every glyph from a single stroke-based library (lucide), so instead of
// shipping two icon fonts we realise the same choice as a stroke weight: Breeze
// keeps lucide's default thin stroke, Classic thickens it. The choice is applied
// as a data attribute on <html> that index.css keys off; the authoritative value
// lives in the DB-backed appearance settings.
export type IconSet = "breeze" | "classic"

// applyIconSet reflects the chosen set onto the document root so the CSS rule
// that adjusts lucide's stroke-width takes effect app-wide, live.
export function applyIconSet(set: string): void {
  const value: IconSet = set === "classic" ? "classic" : "breeze"
  document.documentElement.dataset.iconset = value
}
