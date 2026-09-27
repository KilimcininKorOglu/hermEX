import { createContext, useContext, useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { getCookie, setCookie } from "@/utils/cookies"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

export type Theme = "dark" | "light" | "system"

// isTheme reports whether a stored value names a theme.
export function isTheme(value: unknown): value is Theme {
  return value === "dark" || value === "light" || value === "system"
}

type ThemeProviderProps = {
  children: React.ReactNode
  defaultTheme?: Theme
  storageKey?: string
}

type ThemeProviderState = {
  theme: Theme
  // setTheme applies and stores a theme.
  setTheme: (theme: Theme) => void
  // applyStoredTheme applies the theme read from the users record without storing it again.
  applyStoredTheme: (theme: Theme) => void
  resolvedTheme: "dark" | "light"
}

const initialState: ThemeProviderState = {
  theme: "system",
  setTheme: () => null,
  applyStoredTheme: () => null,
  resolvedTheme: "light",
}

const ThemeProviderContext = createContext<ThemeProviderState>(initialState)

export function ThemeProvider({
  children,
  defaultTheme = "system",
  storageKey = "webmail-theme",
  ...props
}: ThemeProviderProps) {
  const { t } = useI18n()
  // The cookie is a cache so the first render, the login page included, lands on
  // the right theme without a flash; the users record (shared with the admin
  // panel) is where the theme is stored, applied once the session is known.
  const [theme, setThemeState] = useState<Theme>(() => {
    const c = getCookie(storageKey)
    return isTheme(c) ? c : defaultTheme
  })
  const [resolvedTheme, setResolvedTheme] = useState<"dark" | "light">("light")
  // saves numbers the saves, so a failed save restores the theme only while no
  // later change has replaced it.
  const saves = useRef(0)

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

  const applyStoredTheme = (next: Theme) => {
    setCookie(storageKey, next)
    setThemeState(next)
  }

  const value = {
    theme,
    applyStoredTheme,
    // setTheme shows the theme at once and stores it; a failed save puts the
    // previous theme back and says so, so the page never shows an unsaved theme.
    setTheme: (next: Theme) => {
      const prev = theme
      const seq = ++saves.current
      applyStoredTheme(next)
      api.setUserPrefs({ theme: next }).catch(() => {
        if (seq === saves.current) applyStoredTheme(prev)
        toast.error(t("settings.settingSaveFailed"))
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
