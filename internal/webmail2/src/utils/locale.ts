// SUPPORTED_LOCALES are the interface languages the SPA ships a catalogue for.
export const SUPPORTED_LOCALES = ['en', 'tr']

// LOCALE_NAMES are the names the language menus show, each in its own language.
export const LOCALE_NAMES: Record<string, string> = {
  en: 'English',
  tr: 'Türkçe',
}

// resolveLocale picks the interface language: the user's choice when it is a
// supported language, else the browser's, else English.
export function resolveLocale(choice: string | null, browser: string): string {
  if (choice && SUPPORTED_LOCALES.includes(choice)) return choice
  const lang = browser.split('-')[0].toLowerCase()
  return SUPPORTED_LOCALES.includes(lang) ? lang : 'en'
}
