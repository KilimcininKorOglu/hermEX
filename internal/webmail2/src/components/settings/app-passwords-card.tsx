import { useCallback, useEffect, useState } from "react"
import { KeyRound, Trash2 } from "lucide-react"
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
import { useI18n } from "@/hooks/useI18n"
import api, { type AppPasswordEntry } from "@/utils/api"

// AppPasswordsCard manages the per-client credentials a mail program uses in
// place of the account password. IMAP, POP3, SMTP, ActiveSync, EWS, DAV and MAPI
// cannot ask for a code, so an account with two-step verification reaches its
// mail from a client only through one of these.
export function AppPasswordsCard() {
  const { t } = useI18n()
  const [list, setList] = useState<AppPasswordEntry[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState("")
  const [password, setPassword] = useState("")
  const [secret, setSecret] = useState("")
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await api.listAppPasswords()
      setList(res.appPasswords ?? [])
    } catch {
      setList([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    if (busy) return
    setBusy(true)
    try {
      const res = await api.createAppPassword(name, password)
      setSecret(res.secret)
      setPassword("")
      setName("")
      await load()
    } catch {
      toast.error(t("settings.appPasswords.createFailed"))
    } finally {
      setBusy(false)
    }
  }

  const revoke = async (id: number) => {
    if (busy) return
    setBusy(true)
    try {
      await api.deleteAppPassword(id)
      toast.success(t("settings.appPasswords.revoked"))
      await load()
    } catch {
      toast.error(t("settings.appPasswords.revokeFailed"))
    } finally {
      setBusy(false)
    }
  }

  const closeCreate = () => {
    setCreateOpen(false)
    setSecret("")
    setPassword("")
    setName("")
  }

  return (
    <div className="rounded-lg border bg-card p-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <div className="rounded-full bg-primary/10 p-2">
            <KeyRound className="h-5 w-5 text-primary" />
          </div>
          <div>
            <h3 className="font-semibold">{t("settings.appPasswords.title")}</h3>
            <p className="text-sm text-muted-foreground">{t("settings.appPasswords.description")}</p>
          </div>
        </div>
        <Button variant="outline" onClick={() => setCreateOpen(true)}>
          {t("settings.appPasswords.create")}
        </Button>
      </div>

      {list.length > 0 && (
        <ul className="mt-4 divide-y rounded-lg border">
          {list.map((p) => (
            <li key={p.id} className="flex items-center justify-between px-4 py-2">
              <div>
                <p className="text-sm font-medium">{p.name}</p>
                <p className="text-xs text-muted-foreground">
                  {p.lastUsedAt > 0
                    ? t("settings.appPasswords.lastUsed", {
                        when: new Date(p.lastUsedAt * 1000).toLocaleDateString(),
                      })
                    : t("settings.appPasswords.neverUsed")}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("settings.appPasswords.revoke")}
                onClick={() => revoke(p.id)}
                disabled={busy}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <Dialog open={createOpen} onOpenChange={(open) => !open && closeCreate()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.appPasswords.createTitle")}</DialogTitle>
            <DialogDescription>{t("settings.appPasswords.createDescription")}</DialogDescription>
          </DialogHeader>
          {secret ? (
            <div className="space-y-2">
              {/* Shown once: only the hash is stored, so there is no second chance
                  to display it. */}
              <p className="text-sm">{t("settings.appPasswords.secretShownOnce")}</p>
              <code className="block select-all break-all rounded bg-muted px-3 py-2 text-center text-lg tracking-widest">
                {secret}
              </code>
            </div>
          ) : (
            <div className="space-y-3">
              <div className="space-y-2">
                <label className="text-sm font-medium" htmlFor="ap-name">
                  {t("settings.appPasswords.nameLabel")}
                </label>
                <input
                  id="ap-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t("settings.appPasswords.namePlaceholder")}
                  className="w-full rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
                />
              </div>
              <div className="space-y-2">
                <label className="text-sm font-medium" htmlFor="ap-password">
                  {t("settings.appPasswords.passwordLabel")}
                </label>
                <input
                  id="ap-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
                />
              </div>
            </div>
          )}
          <DialogFooter>
            {secret ? (
              <Button onClick={closeCreate}>{t("settings.appPasswords.savedIt")}</Button>
            ) : (
              <>
                <Button variant="outline" onClick={closeCreate}>
                  {t("common.cancel")}
                </Button>
                <Button onClick={create} disabled={busy}>
                  {t("settings.appPasswords.create")}
                </Button>
              </>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
