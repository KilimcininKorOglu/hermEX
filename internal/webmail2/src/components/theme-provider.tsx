import { createContext, useContext, useEffect, useState } from "react"
import { getCookie, setCookie } from "@/utils/cookies"
import api from "@/utils/api"

type Theme = "dark" | "light" | "system"

type ThemeProviderProps = {
  children: React.ReactNode
  defaultTheme?: Theme
  storageKey?: string
}

type ThemeProviderState = {
  theme: Theme
  setTheme: (theme: Theme) => void
  resolvedTheme: "dark" | "light"
}

const initialState: ThemeProviderState = {
  theme: "system",
  setTheme: () => null,
  resolvedTheme: "light",
}

const ThemeProviderContext = createContext<ThemeProviderState>(initialState)

export function ThemeProvider({
  children,
  defaultTheme = "system",
  storageKey = "webmail-theme",
  ...props
}: ThemeProviderProps) {
  // The cookie is a fast cache so the first render lands on the right theme
  // without a flash; the DB (PrWebmailSettings) is the source of truth and is
  // synced on mount and on every setTheme.
  const [theme, setTheme] = useState<Theme>(
    () => (getCookie(storageKey) as Theme) || defaultTheme
  )
  const [resolvedTheme, setResolvedTheme] = useState<"dark" | "light">("light")

  useEffect(() => {
    const root = window.document.documentElement
    root.classList.remove("light", "dark")

    let resolved: "dark" | "light"
    if (theme === "system") {
      resolved = window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light"
    } else {
      resolved = theme
    }

    root.classList.add(resolved)
    setResolvedTheme(resolved)
  }, [theme])

  // On mount, load the persisted theme from the DB so a fresh login on a new
  // browser picks up the saved preference (the cookie cache may be absent).
  useEffect(() => {
    api.getAppearanceSettings()
      .then((s) => {
        if (s.theme === "light" || s.theme === "dark" || s.theme === "system") {
          setTheme(s.theme as Theme)
        }
      })
      .catch(() => {
        /* best-effort: fall back to the cookie/default */
      })
  }, [])

  const value = {
    theme,
    setTheme: (next: Theme) => {
      setCookie(storageKey, next)
      setTheme(next)
      // Persist to the DB (best-effort) so the preference survives a new browser.
      api.getAppearanceSettings()
        .then((s) => api.setAppearanceSettings({ ...s, theme: next }))
        .catch(() => {
          /* best-effort */
        })
    },
    resolvedTheme,
  }

  return (
    <ThemeProviderContext.Provider {...props} value={value}>
      {children}
    </ThemeProviderContext.Provider>
  )
}

export const useTheme = () => {
  const context = useContext(ThemeProviderContext)
  if (context === undefined)
    throw new Error("useTheme must be used within a ThemeProvider")
  return context
}
