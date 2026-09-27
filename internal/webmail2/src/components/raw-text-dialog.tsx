import { useEffect, useState } from "react"
import { Copy, Download, ExternalLink } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

// MAX_SHOWN_CHARS bounds the text the dialog renders, so a message with large
// attachments does not stall the page. Copy and Download still use the whole text.
export const MAX_SHOWN_CHARS = 1_000_000

export type RawTextKind = "source" | "headers"

type LoadState = { status: "loading" } | { status: "failed" } | { status: "loaded"; text: string }

// useRawText loads a message's source or header block while the dialog is open.
// The result is keyed by what was asked for, so a switch to another message or
// kind reads as loading until its own answer arrives.
function useRawText(open: boolean, kind: RawTextKind, id: string): LoadState {
  const key = `${kind}:${id}`
  const [result, setResult] = useState<{ key: string; text: string | null } | null>(null)
  useEffect(() => {
    if (!open) return
    let cancelled = false
    const request = kind === "source" ? api.getMessageRaw(id) : api.getMessageHeaders(id)
    request.then(
      (text) => {
        if (!cancelled) setResult({ key, text })
      },
      () => {
        if (!cancelled) setResult({ key, text: null })
      },
    )
    return () => {
      cancelled = true
    }
  }, [open, kind, id, key])
  if (!result || result.key !== key) return { status: "loading" }
  return result.text === null ? { status: "failed" } : { status: "loaded", text: result.text }
}

// saveText downloads text as a plain-text file under fileName.
function saveText(text: string, fileName: string) {
  const href = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }))
  const a = document.createElement("a")
  a.href = href
  a.download = fileName
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(href)
}

// RawTextDialog shows a message's source or internet header block in a modal,
// with copy, download and open-in-a-new-tab actions.
export function RawTextDialog({ open, onOpenChange, kind, id, baseName }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  kind: RawTextKind
  id: string
  baseName: string
}) {
  const { t } = useI18n()
  const state = useRawText(open, kind, id)
  const text = state.status === "loaded" ? state.text : ""
  const title = t(kind === "source" ? "emailDetail.viewSource" : "emailDetail.viewHeaders")
  const fileName = `${baseName || "message"}${kind === "source" ? ".eml" : "-headers.txt"}`
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t("emailDetail.rawCopied"))
    } catch {
      toast.error(t("emailDetail.copyFailed"))
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] max-w-4xl flex-col">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className="sr-only">{title}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" disabled={state.status !== "loaded"} onClick={() => void copy()}>
            <Copy className="mr-1 h-4 w-4" />
            {t("common.copy")}
          </Button>
          <Button variant="outline" size="sm" disabled={state.status !== "loaded"} onClick={() => saveText(text, fileName)}>
            <Download className="mr-1 h-4 w-4" />
            {t("common.download")}
          </Button>
          <Button variant="outline" size="sm" asChild>
            <a href={api.messageTextURL(kind, id)} target="_blank" rel="noopener noreferrer">
              <ExternalLink className="mr-1 h-4 w-4" />
              {t("emailDetail.popOut")}
            </a>
          </Button>
        </div>
        <RawTextBody state={state} />
      </DialogContent>
    </Dialog>
  )
}

// RawTextBody renders the loaded text, bounded by MAX_SHOWN_CHARS, or the load state.
function RawTextBody({ state }: { state: LoadState }) {
  const { t } = useI18n()
  if (state.status === "loading") return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
  if (state.status === "failed") return <p className="text-sm text-destructive">{t("emailDetail.rawLoadFailed")}</p>
  const truncated = state.text.length > MAX_SHOWN_CHARS
  return (
    <>
      {truncated && <p className="text-sm text-muted-foreground">{t("emailDetail.rawTruncated")}</p>}
      <pre className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/30 p-3 font-mono text-xs">
        {truncated ? state.text.slice(0, MAX_SHOWN_CHARS) : state.text}
      </pre>
    </>
  )
}
