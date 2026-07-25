import { useState, useEffect } from "react"
import { Keyboard } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { shortcuts } from "@/hooks/useKeyboardShortcuts"
import { Separator } from "@/components/ui/separator"
import { Badge } from "@/components/ui/badge"
import { useI18n } from "@/hooks/useI18n"
import { getShortcutMode, basicEnabled, extendedEnabled, type ShortcutMode } from "@/utils/shortcutMode"

export function ShortcutsDialog() {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<ShortcutMode>(() => getShortcutMode())

  useEffect(() => {
    const handleToggle = () => { setMode(getShortcutMode()); setOpen((prev) => !prev) }
    const handleClose = () => setOpen(false)
    const handleModeChange = () => setMode(getShortcutMode())

    document.addEventListener("toggle-shortcuts", handleToggle)
    document.addEventListener("close-dialogs", handleClose)
    document.addEventListener("shortcut-mode-changed", handleModeChange)

    return () => {
      document.removeEventListener("toggle-shortcuts", handleToggle)
      document.removeEventListener("close-dialogs", handleClose)
      document.removeEventListener("shortcut-mode-changed", handleModeChange)
    }
  }, [])

  // enabled tells whether a shortcut of the given level fires under the current mode.
  const enabled = (level: string) =>
    level === "extended" ? extendedEnabled(mode) : basicEnabled(mode)

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-w-2xl max-h-[80vh] overflow-hidden flex flex-col">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Keyboard className="h-5 w-5" />
            {t("shortcuts.title")}
          </DialogTitle>
          <DialogDescription>
            {t("shortcuts.subtitle")}
          </DialogDescription>
        </DialogHeader>
        <div className="flex-1 overflow-y-auto">
          <div className="grid gap-6 md:grid-cols-2">
            {shortcuts.map((section) => (
              <div key={section.category}>
                <h3 className="text-sm font-semibold text-muted-foreground mb-3">
                  {t(section.category)}
                </h3>
                <div className="space-y-2">
                  {section.items.map((item, index) => {
                    const active = enabled(item.level)
                    return (
                    <div
                      key={index}
                      className={`flex items-center justify-between py-1 ${active ? "" : "opacity-40"}`}
                    >
                      <span className="flex items-center gap-1.5 text-sm">
                        {t(item.description)}
                        {item.level === "extended" && (
                          <Badge variant="secondary" className="text-[10px] px-1 py-0">
                            {t("shortcuts.extended")}
                          </Badge>
                        )}
                      </span>
                      <div className="flex items-center gap-1">
                        {item.keys.map((key, keyIndex) => (
                          <span key={keyIndex}>
                            <kbd className="inline-flex items-center justify-center rounded border bg-muted px-2 py-0.5 text-xs font-mono font-medium shadow-sm min-w-[1.5rem]">
                              {key}
                            </kbd>
                            {keyIndex < item.keys.length - 1 && (
                              <span className="mx-0.5 text-muted-foreground" />
                            )}
                          </span>
                        ))}
                      </div>
                    </div>
                    )
                  })}
                </div>
              </div>
            ))}
          </div>
        </div>
        <Separator className="my-4" />
        <div className="text-xs text-muted-foreground text-center">
          {t("shortcuts.pressBefore")} <kbd className="inline-flex items-center justify-center rounded border bg-muted px-1.5 py-0.5 text-xs font-mono">?</kbd> {t("shortcuts.pressAfter")}
        </div>
      </DialogContent>
    </Dialog>
  )
}
