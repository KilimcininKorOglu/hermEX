import { useEffect, useState, type CSSProperties, type ReactNode } from "react"
import { useI18n } from "@/hooks/useI18n"
import { LoginPrefs } from "@/components/login-prefs"
import "@/styles/badge.css"

export interface Branding {
  app_name: string
  logo_url: string
  primary_color: string
  footer_text: string
}

const DARK_INK = "#151413"
const LIGHT_INK = "#ffffff"

// channel converts one sRGB channel (0-255) to linear light (WCAG 2.x).
function channel(v: number): number {
  const c = v / 255
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
}

function luminance(r: number, g: number, b: number): number {
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrast(a: number, b: number): number {
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

// inkFor picks the text colour for the organization's band: whichever of the
// dark and light ink has the higher WCAG contrast against it. A colour it cannot
// read as #rgb or #rrggbb gets the light ink of the default band.
export function inkFor(color: string): string {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(color.trim())
  if (!m) return LIGHT_INK
  const hex = m[1].length === 3 ? [...m[1]].map((c) => c + c).join("") : m[1]
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16))
  const band = luminance(r, g, b)
  return contrast(band, luminance(0x15, 0x14, 0x13)) > contrast(band, 1) ? DARK_INK : LIGHT_INK
}

// badgeStyle paints the band in the organization's colour, when one is set.
function badgeStyle(branding: Branding | null): CSSProperties | undefined {
  const color = branding?.primary_color
  if (!color) return undefined
  return { "--badge-brand": color, "--badge-brand-ink": inkFor(color) } as CSSProperties
}

// useBranding resolves per-tenant branding from the address-bar hostname, so the
// sign-in screens reflect the tenant before the user types anything. The server
// maps the accessed hostname to a registered domain.
export function useBranding(): Branding | null {
  const [branding, setBranding] = useState<Branding | null>(null)
  const host = window.location.hostname.toLowerCase()
  useEffect(() => {
    let cancelled = false
    fetch(`${window.location.origin}/api/v1/branding?domain=${encodeURIComponent(host)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((b: Branding | null) => {
        // Apply branding when the tenant set ANY customization, not just an
        // app name, a tenant may override only the logo, colour, or footer.
        const hasBranding = !!b && (!!b.app_name || !!b.logo_url || !!b.primary_color || !!b.footer_text)
        if (!cancelled) setBranding(hasBranding ? b : null)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [host])
  return branding
}

// LoginFooter shows the tenant footer text, or the app name. It shows no server
// version: the sign-in page is public, and the version is on the About page.
export function LoginFooter({ branding, appName }: { branding: Branding | null; appName: string }) {
  return <p className="badge-foot">{branding?.footer_text || appName}</p>
}

// SignInBadge is the staff badge every sign-in step renders: the band in the
// organization's colour with the door label and reader light, the logo panel,
// and the step's form as children. state drives the reader light.
export function SignInBadge({
  branding,
  state,
  children,
}: {
  branding: Branding | null
  state?: "pending" | "refused"
  children: ReactNode
}) {
  const { t } = useI18n()
  const appName = branding?.app_name || "hermEX"
  return (
    <div className="badge-page" style={badgeStyle(branding)}>
      <div className="badge-prefs">
        <LoginPrefs />
      </div>
      <main className="badge" data-state={state}>
        <div className="badge-band">
          <h1 className="badge-name">{appName}</h1>
          <span className="badge-slot" aria-hidden="true" />
          <span className="badge-door">
            {t("login.badgeRole")}
            <span className="badge-led" aria-hidden="true" />
          </span>
        </div>
        <div className="badge-body">
          <div className="badge-photo" aria-hidden="true">
            <img src={branding?.logo_url || "/favicon.svg"} alt="" />
          </div>
          {children}
        </div>
      </main>
      <LoginFooter branding={branding} appName={appName} />
    </div>
  )
}
