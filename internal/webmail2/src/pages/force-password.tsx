import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { Lock } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/hooks/useI18n"
import { useAuth } from "@/contexts/AuthContext"
import api from "@/utils/api"

// ForcePasswordChangePage is shown when an admin reset flagged the account
// (must_change_password). The user must set a new password before reaching the
// rest of the app; a successful change clears the flag server-side, after which
// we refresh the session and continue to the inbox.
export function ForcePasswordChangePage() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const { refresh } = useAuth()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [saving, setSaving] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (next.length < 8) {
      toast.error(t("forcePassword.tooShort"))
      return
    }
    if (next !== confirm) {
      toast.error(t("forcePassword.mismatch"))
      return
    }
    setSaving(true)
    try {
      await api.changePassword(current, next)
      await refresh()
      navigate("/inbox", { replace: true })
    } catch {
      toast.error(t("forcePassword.failed"))
      setSaving(false)
    }
  }

  const inputClass =
    "w-full rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"

  return (
    <div className="min-h-screen flex items-center justify-center bg-muted/30 p-4">
      <form onSubmit={submit} className="w-full max-w-md rounded-2xl border bg-card p-6 shadow-lg">
        <div className="flex flex-col items-center text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
            <Lock className="h-6 w-6 text-primary-foreground" />
          </div>
          <h1 className="mt-3 text-xl font-bold">{t("forcePassword.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("forcePassword.subtitle")}</p>
        </div>

        <div className="mt-6 space-y-4">
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="fp-current">
              {t("forcePassword.current")}
            </label>
            <input
              id="fp-current"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              required
              className={inputClass}
            />
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="fp-new">
              {t("forcePassword.new")}
            </label>
            <input
              id="fp-new"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              required
              className={inputClass}
            />
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="fp-confirm">
              {t("forcePassword.confirm")}
            </label>
            <input
              id="fp-confirm"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
              className={inputClass}
            />
          </div>
        </div>

        <Button type="submit" className="mt-6 w-full" disabled={saving}>
          {saving ? t("common.saving") : t("forcePassword.submit")}
        </Button>
      </form>
    </div>
  )
}
