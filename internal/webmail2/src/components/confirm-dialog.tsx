import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useI18n } from "@/hooks/useI18n"

export interface ConfirmOptions {
  title: string
  message: string
  // confirmLabel names the confirm button; the title is used when it is unset.
  confirmLabel?: string
  destructive?: boolean
}

type Confirm = (options: ConfirmOptions) => Promise<boolean>

const ConfirmContext = createContext<Confirm | null>(null)

interface Pending {
  options: ConfirmOptions
  resolve: (ok: boolean) => void
}

// ConfirmProvider serves useConfirm: one app dialog that asks a question and
// settles a promise with the answer. The browser's own confirm() is not used,
// because it blocks the page and cannot be styled or translated with the app.
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  const [pending, setPending] = useState<Pending | null>(null)
  const current = useRef<Pending | null>(null)

  // settle answers the open question once; a closed dialog answers no.
  const settle = useCallback((ok: boolean) => {
    const p = current.current
    current.current = null
    setPending(null)
    p?.resolve(ok)
  }, [])

  // A second question replaces the first, which is answered no, so no caller
  // waits on a promise that can never settle.
  const confirm = useCallback<Confirm>((options) => new Promise<boolean>((resolve) => {
    current.current?.resolve(false)
    const p = { options, resolve }
    current.current = p
    setPending(p)
  }), [])

  const options = pending?.options
  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) settle(false) }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{options?.title}</DialogTitle>
            <DialogDescription>{options?.message}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => settle(false)}>{t("common.cancel")}</Button>
            <Button variant={options?.destructive ? "destructive" : "default"} onClick={() => settle(true)}>
              {options?.confirmLabel ?? options?.title}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </ConfirmContext.Provider>
  )
}

// useConfirm returns the function that asks a question in the app dialog and
// resolves true only when the user confirms. Must be used within ConfirmProvider.
export function useConfirm(): Confirm {
  const confirm = useContext(ConfirmContext)
  if (!confirm) throw new Error("useConfirm must be used within a ConfirmProvider")
  return confirm
}
