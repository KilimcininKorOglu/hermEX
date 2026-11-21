import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { toast } from "sonner"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import { SignInBadge, useBranding } from "@/components/sign-in-badge"
import api from "@/utils/api"
import { PasswordTooShortError, passwordsMismatch } from "@/utils/passwordChange"

// ForcePasswordChangePage is shown when an admin reset flagged the account
// (must_change_password). The user must set a new password before reaching the
// rest of the app; a successful change clears the flag server-side, after which
// we refresh the session and continue to the inbox.
export function ForcePasswordChangePage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { refresh } = useAuth()
  const branding = useBranding()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")

  const fail = (message: string) => {
    toast.error(message)
    setError(message)
  }

  const submit = async (e: React.SubmitEvent<HTMLFormElement>) => {
    e.preventDefault()
    setError("")
    if (passwordsMismatch(next, confirm)) {
      fail(t("forcePassword.mismatch"))
      return
    }
    setSaving(true)
    try {
      await api.changePassword(current, next)
      await refresh()
      navigate("/inbox", { replace: true })
    } catch (err) {
      fail(err instanceof PasswordTooShortError
        ? t("forcePassword.tooShort", { count: String(err.minLength) })
        : t("forcePassword.failed"))
      setSaving(false)
    }
  }

  return (
    <SignInBadge branding={branding} state={saving ? "pending" : error ? "refused" : undefined}>
      <form onSubmit={submit} className="badge-form">
        <div>
          <h2 className="badge-title">{t("forcePassword.title")}</h2>
          <p className="badge-note">{t("forcePassword.subtitle")}</p>
        </div>
        {error && (
          <p className="badge-error" role="alert">
            {error}
          </p>
        )}
        <label className="badge-field" htmlFor="fp-current">
          {t("forcePassword.current")}
          <input
            id="fp-current"
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
          />
        </label>
        <label className="badge-field" htmlFor="fp-new">
          {t("forcePassword.new")}
          <input
            id="fp-new"
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            required
          />
        </label>
        <label className="badge-field" htmlFor="fp-confirm">
          {t("forcePassword.confirm")}
          <input
            id="fp-confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
          />
        </label>
        <button type="submit" className="badge-submit" disabled={saving}>
          {saving ? t("common.saving") : t("forcePassword.submit")}
        </button>
      </form>
    </SignInBadge>
  )
}
