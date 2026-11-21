import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { toast } from "sonner"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import { SignInBadge, useBranding } from "@/components/sign-in-badge"
import api from "@/utils/api"

// SecondFactorPage is the code prompt a login stops at when the account carries
// a second factor. The session cookie it holds authenticates nothing but this
// endpoint, so the screen is not what protects the mailbox: the API refuses
// every other path until a code is accepted here.
export function SecondFactorPage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { refresh, logout } = useAuth()
  const branding = useBranding()
  const [code, setCode] = useState("")
  const [verifying, setVerifying] = useState(false)
  const [refused, setRefused] = useState(false)

  const submit = async (e: React.SubmitEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (verifying) return
    setVerifying(true)
    setRefused(false)
    try {
      await api.verifySecondFactor(code.trim())
      await refresh()
      navigate("/inbox", { replace: true })
    } catch {
      toast.error(t("secondFactor.invalid"))
      setCode("")
      setVerifying(false)
      setRefused(true)
    }
  }

  const cancel = async () => {
    await logout()
    navigate("/login", { replace: true })
  }

  return (
    <SignInBadge branding={branding} state={verifying ? "pending" : refused ? "refused" : undefined}>
      <form onSubmit={submit} className="badge-form">
        <div>
          <h2 className="badge-title">{t("secondFactor.title")}</h2>
          <p className="badge-note">{t("secondFactor.subtitle")}</p>
        </div>
        {refused && (
          <p className="badge-error" role="alert">
            {t("secondFactor.invalid")}
          </p>
        )}
        <label className="badge-field" htmlFor="sf-code">
          {t("secondFactor.codeLabel")}
          <input
            id="sf-code"
            className="badge-code"
            // One field takes both a six-digit code and a recovery code, so the
            // user never has to decide which box they are in.
            inputMode="text"
            autoComplete="one-time-code"
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
        </label>
        <p className="badge-note">{t("secondFactor.recoveryHint")}</p>
        <button type="submit" className="badge-submit" disabled={verifying}>
          {verifying ? t("common.saving") : t("secondFactor.submit")}
        </button>
        <button type="button" onClick={cancel} className="badge-link">
          {t("secondFactor.cancel")}
        </button>
      </form>
    </SignInBadge>
  )
}
