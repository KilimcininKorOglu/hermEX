import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import api from "@/utils/api"

// SecondFactorPage is the code prompt a login stops at when the account carries
// a second factor. The session cookie it holds authenticates nothing but this
// endpoint, so the screen is not what protects the mailbox: the API refuses
// every other path until a code is accepted here.
export function SecondFactorPage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { refresh, logout } = useAuth()
  const [code, setCode] = useState("")
  const [verifying, setVerifying] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (verifying) return
    setVerifying(true)
    try {
      await api.verifySecondFactor(code.trim())
      await refresh()
      navigate("/inbox", { replace: true })
    } catch {
      toast.error(t("secondFactor.invalid"))
      setCode("")
      setVerifying(false)
    }
  }

  const cancel = async () => {
    await logout()
    navigate("/login", { replace: true })
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-muted/30 p-4">
      <form onSubmit={submit} className="w-full max-w-md rounded-2xl border bg-card p-6 shadow-lg">
        <div className="flex flex-col items-center text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
            <ShieldCheck className="h-6 w-6 text-primary-foreground" />
          </div>
          <h1 className="mt-3 text-xl font-bold">{t("secondFactor.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("secondFactor.subtitle")}</p>
        </div>

        <div className="mt-6 space-y-2">
          <label className="text-sm font-medium" htmlFor="sf-code">
            {t("secondFactor.codeLabel")}
          </label>
          <input
            id="sf-code"
            // One field takes both a six-digit code and a recovery code, so the
            // user never has to decide which box they are in.
            inputMode="text"
            autoComplete="one-time-code"
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
            className="w-full rounded-lg border bg-background px-3 py-2 text-center text-lg tracking-widest outline-none focus:ring-2 focus:ring-primary/20"
          />
          <p className="text-xs text-muted-foreground">{t("secondFactor.recoveryHint")}</p>
        </div>

        <Button type="submit" className="mt-6 w-full" disabled={verifying}>
          {verifying ? t("common.saving") : t("secondFactor.submit")}
        </Button>
        <button
          type="button"
          onClick={cancel}
          className="mt-3 w-full text-sm text-muted-foreground hover:text-foreground"
        >
          {t("secondFactor.cancel")}
        </button>
      </form>
    </div>
  )
}
