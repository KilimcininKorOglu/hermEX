import { useCallback, useEffect, useState } from "react"
import { Bell, Lock, Monitor } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useI18n } from "@/hooks/useI18n"
import api, { type ClientSession } from "@/utils/api"
import { disablePushNotifications, enablePushNotifications, pushSupported } from "@/utils/push"
import { SettingSection } from "./setting-layout"

export function PushSection() {
  const { t } = useI18n()
  const [busy, setBusy] = useState(false)
  const supported = pushSupported()

  const run = async (action: () => Promise<void>, okKey: string, failKey: string, showCause: boolean) => {
    setBusy(true)
    try {
      await action()
      toast.success(t(okKey))
    } catch (err) {
      toast.error(showCause && err instanceof Error ? err.message : t(failKey))
    } finally {
      setBusy(false)
    }
  }

  return (
    <SettingSection icon={Bell} title={t("settings.push.title")} description={t("settings.push.description")}>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          onClick={() => void run(enablePushNotifications, "settings.push.enabled", "settings.push.enableFailed", true)}
          disabled={busy || !supported}
        >
          {busy ? t("settings.push.working") : t("settings.push.enable")}
        </Button>
        <Button
          variant="outline"
          onClick={() => void run(disablePushNotifications, "settings.push.disabled", "settings.push.disableFailed", false)}
          disabled={busy || !supported}
        >
          {t("settings.disable")}
        </Button>
        {!supported && <span className="text-sm text-muted-foreground">{t("settings.push.notSupported")}</span>}
      </div>
    </SettingSection>
  )
}

function SessionRow({ s, onRevoke }: { s: ClientSession; onRevoke: () => void }) {
  const { t } = useI18n()
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
      <div className="min-w-0">
        <p className="text-sm font-medium truncate">
          {s.device_type || t("settings.sessions.unknownDevice")} · {s.client_ip || t("settings.sessions.unknownIp")}
        </p>
        <p className="text-xs text-muted-foreground truncate">{s.user_agent || "-"}</p>
        <p className="text-xs text-muted-foreground">{t("settings.sessions.lastActive", { time: s.last_active })}</p>
      </div>
      <Button variant="outline" size="sm" onClick={onRevoke}>{t("settings.sessions.revoke")}</Button>
    </div>
  )
}

export function SessionsSection() {
  const { t } = useI18n()
  const [sessions, setSessions] = useState<ClientSession[]>([])

  const load = useCallback(async () => {
    try {
      setSessions((await api.getSessions()).sessions ?? [])
    } catch (err) {
      console.error("Failed to load sessions:", err)
      setSessions([])
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const revoke = async (id: string) => {
    try {
      await api.revokeSession(id)
      toast.success(t("settings.sessions.revoked"))
      setSessions((prev) => prev.filter((s) => s.id !== id))
    } catch (err) {
      console.error("Failed to revoke session:", err)
      toast.error(t("settings.sessions.revokeFailed"))
    }
  }

  return (
    <SettingSection icon={Monitor} title={t("settings.sessions.title")} description={t("settings.sessions.description")}>
      {sessions.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("settings.sessions.empty")}</p>
      ) : (
        <div className="space-y-2">
          {sessions.map((s) => <SessionRow key={s.id} s={s} onRevoke={() => void revoke(s.id)} />)}
        </div>
      )}
    </SettingSection>
  )
}

const MIN_PASSWORD_LENGTH = 8

// passwordError returns the i18n key of the reason a new password is refused
// before it is sent, or null.
function passwordError(next: string, confirm: string): string | null {
  if (next.length < MIN_PASSWORD_LENGTH) return "settings.password.tooShort"
  if (next !== confirm) return "settings.password.mismatch"
  return null
}

const PASSWORD_FIELDS: { key: "current" | "next" | "confirm"; id: string; label: string; autoComplete: string }[] = [
  { key: "current", id: "pw-current", label: "settings.password.current", autoComplete: "current-password" },
  { key: "next", id: "pw-new", label: "settings.password.new", autoComplete: "new-password" },
  { key: "confirm", id: "pw-confirm", label: "settings.password.confirm", autoComplete: "new-password" },
]

const EMPTY_PASSWORDS = { current: "", next: "", confirm: "" }

function PasswordDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()
  const [values, setValues] = useState(EMPTY_PASSWORDS)
  const [saving, setSaving] = useState(false)

  const submit = async () => {
    const invalid = passwordError(values.next, values.confirm)
    if (invalid) {
      toast.error(t(invalid))
      return
    }
    setSaving(true)
    try {
      await api.changePassword(values.current, values.next)
      toast.success(t("settings.password.updated"))
      onOpenChange(false)
      setValues(EMPTY_PASSWORDS)
    } catch (err) {
      console.error("Failed to change password:", err)
      toast.error(t("settings.password.changeFailed"))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("settings.password.title")}</DialogTitle>
          <DialogDescription>{t("settings.password.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {PASSWORD_FIELDS.map((f) => (
            <div key={f.key} className="space-y-1">
              <Label htmlFor={f.id}>{t(f.label)}</Label>
              <Input
                id={f.id}
                type="password"
                value={values[f.key]}
                onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                autoComplete={f.autoComplete}
              />
            </div>
          ))}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>{t("common.cancel")}</Button>
          <Button onClick={() => void submit()} disabled={saving}>
            {saving ? t("common.saving") : t("settings.password.update")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// AccountSection opens the password change dialog.
export function AccountSection() {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  return (
    <>
      <div className="rounded-lg border bg-card p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-4">
            <div className="rounded-full bg-destructive/10 p-2">
              <Lock className="h-5 w-5 text-destructive" />
            </div>
            <div>
              <h3 className="font-semibold">{t("settings.account.title")}</h3>
              <p className="text-sm text-muted-foreground">{t("settings.account.description")}</p>
            </div>
          </div>
          <Button variant="outline" onClick={() => setOpen(true)}>{t("settings.account.manage")}</Button>
        </div>
      </div>
      <PasswordDialog open={open} onOpenChange={setOpen} />
    </>
  )
}
