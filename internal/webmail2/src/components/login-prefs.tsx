import { Check, ChevronDown, Moon, Sun } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useI18n } from "@/hooks/useI18n"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { LOCALE_NAMES } from "@/utils/locale"

// LoginPrefs is the language menu and theme toggle of the sign-in page. Nobody is
// signed in yet, so a choice is kept in its cookie only; once the user signs in,
// the users record wins where it holds a choice and adopts the cookie where not.
// The language button names the current language, like the admin panel's select.
export function LoginPrefs() {
  const { t, locale, applyStoredLocale, supportedLocales } = useI18n()
  const { resolvedTheme, applyStoredTheme } = useTheme()
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className="badge-pref" title={t("sidebar.selectLanguage")} aria-label={t("sidebar.selectLanguage")}>
            {LOCALE_NAMES[locale] ?? locale.toUpperCase()}
            <ChevronDown aria-hidden="true" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-44">
          {supportedLocales.map((code) => (
            <DropdownMenuItem
              key={code}
              onClick={() => applyStoredLocale(code)}
              className="flex items-center justify-between cursor-pointer"
            >
              <span>{LOCALE_NAMES[code] ?? code.toUpperCase()}</span>
              {locale === code && <Check className="h-4 w-4" />}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      <button
        type="button"
        className="badge-pref theme-toggle"
        onClick={() => applyStoredTheme(resolvedTheme === "dark" ? "light" : "dark")}
        title={t("header.toggleTheme")}
        aria-label={t("header.toggleTheme")}
      >
        {resolvedTheme === "dark" ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}
      </button>
    </>
  )
}
