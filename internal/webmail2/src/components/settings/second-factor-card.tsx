import { useCallback, useEffect, useState } from "react"
import { ShieldCheck } from "lucide-react"
import { toast } from "sonner"
import qrcode from "qrcode-generator"
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
import api, { type SecondFactorStatus } from "@/utils/api"

// qrDataURI renders the otpauth URI as a scannable image. Error correction level
// M with an automatic version keeps the code small enough to scan from a phone
// held at a laptop screen while tolerating the odd smudge.
function qrDataURI(text: string): string {
  const qr = qrcode(0, "M")
  qr.addData(text)
  qr.make()
  return qr.createDataURL(6, 8)
}

type Step = "idle" | "scanning" | "codes"

// SecondFactorCard is the account's own second-factor setup. It is deliberately
// self-contained: the enrollment is a short state machine (start, confirm with a
// code, keep the recovery codes) and folding it into the settings page would
// tangle it with every other form there.
export function SecondFactorCard() {
  const { t } = useI18n()
  const [status, setStatus] = useState<SecondFactorStatus | null>(null)
  const [step, setStep] = useState<Step>("idle")
  const [secret, setSecret] = useState("")
  const [uri, setUri] = useState("")
  const [code, setCode] = useState("")
  const [codes, setCodes] = useState<string[]>([])
  const [password, setPassword] = useState("")
  const [disableOpen, setDisableOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setStatus(await api.secondFactorStatus())
    } catch {
      setStatus(null)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const begin = async () => {
    if (busy) return
    setBusy(true)
    try {
      const res = await api.beginSecondFactor()
      setSecret(res.secret)
      setUri(res.uri)
      setCode("")
      setStep("scanning")
    } catch {
      toast.error(t("settings.twoFactor.beginFailed"))
    } finally {
      setBusy(false)
    }
  }

  const activate = async () => {
    if (busy) return
    setBusy(true)
    try {
      const res = await api.activateSecondFactor(code.trim())
      setCodes(res.recoveryCodes)
      setStep("codes")
      await load()
    } catch {
      toast.error(t("settings.twoFactor.invalidCode"))
    } finally {
      setBusy(false)
    }
  }

  const disable = async () => {
    if (busy) return
    setBusy(true)
    try {
      await api.disableSecondFactor(password)
      setDisableOpen(false)
      setPassword("")
      toast.success(t("settings.twoFactor.disabled"))
      await load()
    } catch {
      toast.error(t("settings.twoFactor.disableFailed"))
    } finally {
      setBusy(false)
    }
  }

  const enabled = status?.enabled ?? false

  return (
    <div className="rounded-lg border bg-card p-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <div className="rounded-full bg-primary/10 p-2">
            <ShieldCheck className="h-5 w-5 text-primary" />
          </div>
          <div>
            <h3 className="font-semibold">{t("settings.twoFactor.title")}</h3>
            <p className="text-sm text-muted-foreground">
              {enabled
                ? t("settings.twoFactor.enabledDescription", {
                    count: String(status?.recoveryRemaining ?? 0),
                  })
                : t("settings.twoFactor.description")}
            </p>
          </div>
        </div>
        {enabled ? (
          <Button variant="outline" onClick={() => setDisableOpen(true)}>
            {t("settings.twoFactor.disable")}
          </Button>
        ) : (
          <Button variant="outline" onClick={begin} disabled={busy}>
            {t("settings.twoFactor.enable")}
          </Button>
        )}
      </div>

      {/* Enrollment: scan, then prove a code came from the app. */}
      <Dialog open={step === "scanning"} onOpenChange={(open) => !open && setStep("idle")}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.twoFactor.scanTitle")}</DialogTitle>
            <DialogDescription>{t("settings.twoFactor.scanDescription")}</DialogDescription>
          </DialogHeader>
          {uri && (
            <div className="flex flex-col items-center gap-3">
              <img src={qrDataURI(uri)} alt={t("settings.twoFactor.qrAlt")} className="rounded bg-white p-2" />
              {/* The secret is shown as text too: a user whose camera cannot see
                  the screen types it into the app by hand. */}
              <code className="select-all break-all rounded bg-muted px-2 py-1 text-xs">{secret}</code>
            </div>
          )}
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="tf-code">
              {t("settings.twoFactor.codeLabel")}
            </label>
            <input
              id="tf-code"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              className="w-full rounded-lg border bg-background px-3 py-2 text-center text-lg tracking-widest outline-none focus:ring-2 focus:ring-primary/20"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setStep("idle")}>
              {t("common.cancel")}
            </Button>
            <Button onClick={activate} disabled={busy}>
              {t("settings.twoFactor.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* The recovery codes are shown exactly once, because only their hashes are
          stored: there is no second chance to display them. */}
      <Dialog open={step === "codes"} onOpenChange={(open) => !open && setStep("idle")}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.twoFactor.recoveryTitle")}</DialogTitle>
            <DialogDescription>{t("settings.twoFactor.recoveryDescription")}</DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-2">
            {codes.map((c) => (
              <code key={c} className="select-all rounded bg-muted px-2 py-1 text-center text-sm">
                {c}
              </code>
            ))}
          </div>
          <DialogFooter>
            <Button onClick={() => setStep("idle")}>{t("settings.twoFactor.savedThem")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Turning it off asks for the password again, because that is the first
          thing someone holding a stolen session would do. */}
      <Dialog open={disableOpen} onOpenChange={setDisableOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.twoFactor.disableTitle")}</DialogTitle>
            <DialogDescription>{t("settings.twoFactor.disableDescription")}</DialogDescription>
          </DialogHeader>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDisableOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={disable} disabled={busy}>
              {t("settings.twoFactor.disable")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
