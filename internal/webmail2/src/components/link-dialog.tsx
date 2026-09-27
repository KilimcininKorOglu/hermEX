import { useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useI18n } from "@/hooks/useI18n"
import { isSafeLinkURL } from "@/utils/sanitize"

interface LinkDialogProps {
  open: boolean
  onInsert: (url: string) => void
  onCancel: () => void
}

// LinkDialog asks for the address a link points to. A scheme outside the
// allowlist (javascript: above all) is refused here, before the address can
// reach the document, and the field says why.
export function LinkDialog({ open, onInsert, onCancel }: LinkDialogProps) {
  const { t } = useI18n()
  const [url, setUrl] = useState("")
  const [invalid, setInvalid] = useState(false)

  const close = () => {
    setUrl("")
    setInvalid(false)
    onCancel()
  }

  const submit = () => {
    const value = url.trim()
    if (!value) return
    if (!isSafeLinkURL(value)) {
      setInvalid(true)
      return
    }
    setUrl("")
    setInvalid(false)
    onInsert(value)
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      {/* The editor restores its own selection on insert, so focus is not handed
          back to the toolbar button when the dialog closes. */}
      <DialogContent className="max-w-md" onCloseAutoFocus={(e) => e.preventDefault()}>
        <form onSubmit={(e) => { e.preventDefault(); submit() }} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{t("editor.insertLink")}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="link-dialog-url">{t("editor.linkUrl")}</Label>
            <Input
              id="link-dialog-url"
              autoFocus
              value={url}
              placeholder={t("editor.linkPlaceholder")}
              aria-invalid={invalid}
              onChange={(e) => { setUrl(e.target.value); setInvalid(false) }}
            />
            {invalid && <p role="alert" className="text-sm text-destructive">{t("editor.linkInvalid")}</p>}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>{t("common.cancel")}</Button>
            <Button type="submit" disabled={!url.trim()}>{t("editor.linkInsert")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
