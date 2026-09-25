import { useCallback, useEffect, useState } from "react"
import { FileKey } from "lucide-react"
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
import api, { type SMIMECertInfo } from "@/utils/api"
import * as smimeStore from "@/utils/smime"
import type { CertInfo } from "@/utils/smime"

type SmimeMode = "browser" | "server"

// certFields keeps the certificate fields the page shows.
function certFields(c: SMIMECertInfo): CertInfo {
  return {
    subject: c.subject,
    issuer: c.issuer,
    notBefore: c.notBefore,
    notAfter: c.notAfter,
    serialNumber: c.serialNumber,
    fingerprint: c.fingerprint,
  }
}

// bytesToBase64 encodes a .p12 file for the server-mode upload.
function bytesToBase64(bytes: ArrayBuffer): string {
  const arr = new Uint8Array(bytes)
  let bin = ""
  for (let i = 0; i < arr.length; i++) bin += String.fromCharCode(arr[i])
  return btoa(bin)
}

// serverCert returns the certificate the server holds in server mode, or null.
async function serverCert(): Promise<CertInfo | null> {
  const res = await api.getSMIMECertificate()
  if ("hasKeys" in res || res.mode !== "server") return null
  return certFields(res)
}

// useSmime manages the S/MIME identity. "browser" mode keeps the key in THIS
// browser (IndexedDB) and the server holds only the public certificate;
// "server" mode keeps the key on the server (encrypted at rest), so it signs and
// decrypts on every device.
function useSmime() {
  const { t } = useI18n()
  const [cert, setCert] = useState<CertInfo | null>(null)
  const [mode, setMode] = useState<SmimeMode | null>(null)
  const [unlocked, setUnlocked] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const show = (next: CertInfo | null, nextMode: SmimeMode | null, isUnlocked: boolean) => {
    setCert(next)
    setMode(next ? nextMode : null)
    setUnlocked(isUnlocked)
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const local = await smimeStore.storedCertInfo()
      if (local) {
        setCert(local); setMode("browser"); setUnlocked(smimeStore.isUnlocked())
      } else {
        const remote = await serverCert()
        setCert(remote); setMode(remote ? "server" : null); setUnlocked(remote !== null)
      }
    } catch {
      setCert(null); setMode(null)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  // importP12 stores an identity in the chosen place and reports whether it did.
  const importP12 = async (file: File, password: string, target: SmimeMode): Promise<boolean> => {
    setSaving(true)
    try {
      const bytes = await file.arrayBuffer()
      if (target === "server") {
        // Server mode: send the .p12 + its password; the server stores the key.
        show(certFields(await api.uploadServerSMIME(bytesToBase64(bytes), password)), "server", true)
      } else {
        // Browser mode: store the key in IndexedDB, publish only the public cert.
        const { info, certPem } = await smimeStore.importP12(bytes, password)
        await api.uploadSMIMECertificate(certPem)
        show(info, "browser", true)
      }
      toast.success(t("settings.privacy.smimeCertSaved"))
      return true
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.privacy.smimeCertSaveFailed"))
      return false
    } finally {
      setSaving(false)
    }
  }

  const unlock = async (password: string): Promise<boolean> => {
    setSaving(true)
    try {
      await smimeStore.unlock(password)
      setUnlocked(true)
      toast.success(t("settings.privacy.smimeUnlocked"))
      return true
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.privacy.smimeUnlockFailed"))
      return false
    } finally {
      setSaving(false)
    }
  }

  // remove deletes the server record first. A failed server delete then leaves
  // the published certificate and the browser key both in place, instead of
  // reporting success while the server still holds the certificate (and, in
  // server mode, the private key).
  const remove = async (): Promise<boolean> => {
    setDeleting(true)
    try {
      await api.deleteSMIMECertificate()
      await smimeStore.removeIdentity()
      show(null, null, false)
      toast.success(t("settings.privacy.smimeCertDeleted"))
      return true
    } catch {
      toast.error(t("settings.privacy.smimeCertDeleteFailed"))
      return false
    } finally {
      setDeleting(false)
    }
  }

  return { cert, mode, unlocked, loading, saving, deleting, importP12, unlock, remove }
}

type Smime = ReturnType<typeof useSmime>

// smimeBadgeKey is the i18n key of where the key lives and whether it is ready.
function smimeBadgeKey(s: Smime): string {
  if (s.mode === "server") return "settings.privacy.smimeServerBadge"
  return s.unlocked ? "settings.privacy.smimeUnlockedBadge" : "settings.privacy.smimeLockedBadge"
}

function SmimeStatus({ smime }: { smime: Smime }) {
  const { t } = useI18n()
  if (smime.loading) return <p className="text-xs text-muted-foreground mt-1">{t("common.loading")}</p>
  if (!smime.cert) return <p className="text-xs text-muted-foreground mt-1">{t("settings.privacy.smimeCertNone")}</p>
  return (
    <p className="text-xs text-muted-foreground mt-1 truncate">
      {smime.cert.subject} · {t(smimeBadgeKey(smime))}
    </p>
  )
}

function CertDetails({ cert }: { cert: CertInfo }) {
  const { t } = useI18n()
  const date = (v?: string) => (v ? new Date(v).toLocaleDateString() : "-")
  return (
    <div className="rounded-lg border bg-muted/50 p-4 space-y-2">
      <div className="grid grid-cols-[120px_1fr] gap-1 text-sm">
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertSubject")}:</span>
        <span className="break-all">{cert.subject}</span>
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertIssuer")}:</span>
        <span className="break-all">{cert.issuer}</span>
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertValidFrom")}:</span>
        <span>{date(cert.notBefore)}</span>
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertValidUntil")}:</span>
        <span>{date(cert.notAfter)}</span>
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertSerial")}:</span>
        <span className="font-mono text-xs break-all">{cert.serialNumber}</span>
        <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertFingerprint")}:</span>
        <span className="font-mono text-xs break-all">{cert.fingerprint}</span>
      </div>
    </div>
  )
}

function StorageChoice({ value, onChange }: { value: SmimeMode; onChange: (mode: SmimeMode) => void }) {
  const { t } = useI18n()
  const choices: { mode: SmimeMode; label: string; hint: string }[] = [
    { mode: "browser", label: "settings.privacy.smimeStoreBrowser", hint: "settings.privacy.smimeStoreBrowserHint" },
    { mode: "server", label: "settings.privacy.smimeStoreServer", hint: "settings.privacy.smimeStoreServerHint" },
  ]
  return (
    <div className="space-y-1">
      <Label>{t("settings.privacy.smimeStorageLabel")}</Label>
      <div className="grid grid-cols-2 gap-2">
        {choices.map((c) => (
          <button
            key={c.mode}
            type="button"
            onClick={() => onChange(c.mode)}
            className={`rounded-md border p-2 text-left text-xs ${value === c.mode ? "border-primary bg-primary/5" : "border-input"}`}
          >
            <div className="font-medium">{t(c.label)}</div>
            <div className="text-muted-foreground">{t(c.hint)}</div>
          </button>
        ))}
      </div>
    </div>
  )
}

// ImportDialog shows the current certificate and imports a .p12 identity.
function ImportDialog({ smime, open, onOpenChange }: { smime: Smime; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()
  const [target, setTarget] = useState<SmimeMode>("browser")
  const [file, setFile] = useState<File | null>(null)
  const [password, setPassword] = useState("")

  const close = () => {
    onOpenChange(false)
    setFile(null)
    setPassword("")
  }
  const submit = async () => {
    if (!file || !password) return
    if (await smime.importP12(file, password, target)) close()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{smime.cert ? t("settings.privacy.smimeCertView") : t("settings.privacy.smimeCertUpload")}</DialogTitle>
          <DialogDescription>{t("settings.privacy.smimeImportDesc")}</DialogDescription>
        </DialogHeader>
        {smime.cert && <CertDetails cert={smime.cert} />}
        <div className="space-y-3">
          <StorageChoice value={target} onChange={setTarget} />
          <div className="space-y-1">
            <Label htmlFor="smime-p12">{t("settings.privacy.smimeP12File")}</Label>
            <Input id="smime-p12" type="file" accept=".p12,.pfx" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
          </div>
          <div className="space-y-1">
            <Label htmlFor="smime-p12-pass">{t("settings.privacy.smimeP12Password")}</Label>
            <Input
              id="smime-p12-pass"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={t("settings.privacy.smimeP12PasswordPlaceholder")}
            />
          </div>
          <p className="text-xs text-muted-foreground">{t("settings.privacy.smimeImportHint")}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={smime.saving}>{t("common.cancel")}</Button>
          <Button onClick={() => void submit()} disabled={smime.saving || !file || !password}>
            {smime.saving ? t("common.saving") : t("settings.privacy.smimeCertAdd")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// UnlockDialog opens a browser-held key for this session.
function UnlockDialog({ smime, open, onOpenChange }: { smime: Smime; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()
  const [password, setPassword] = useState("")

  const close = () => {
    onOpenChange(false)
    setPassword("")
  }
  const submit = async () => {
    if (!password) return
    if (await smime.unlock(password)) close()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("settings.privacy.smimeUnlock")}</DialogTitle>
          <DialogDescription>{t("settings.privacy.smimeUnlockDesc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1">
          <Label htmlFor="smime-unlock-pass">{t("settings.privacy.smimeP12Password")}</Label>
          <Input
            id="smime-unlock-pass"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") void submit() }}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={smime.saving}>{t("common.cancel")}</Button>
          <Button onClick={() => void submit()} disabled={smime.saving || !password}>{t("settings.privacy.smimeUnlock")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DeleteDialog({ smime, open, onOpenChange }: { smime: Smime; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()
  const submit = async () => {
    if (await smime.remove()) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("settings.privacy.smimeCertDelete")}</DialogTitle>
          <DialogDescription>{t("settings.privacy.smimeCertDeleteConfirm")}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={smime.deleting}>{t("common.cancel")}</Button>
          <Button variant="destructive" onClick={() => void submit()} disabled={smime.deleting}>
            {smime.deleting ? t("common.deleting") : t("settings.privacy.smimeCertDelete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

type OpenDialog = "import" | "unlock" | "delete" | null

function SmimeActions({ smime, onOpen }: { smime: Smime; onOpen: (d: OpenDialog) => void }) {
  const { t } = useI18n()
  const locked = smime.cert !== null && smime.mode === "browser" && !smime.unlocked
  return (
    <div className="flex items-center gap-2 ml-4">
      {locked && (
        <Button variant="outline" size="sm" onClick={() => onOpen("unlock")}>{t("settings.privacy.smimeUnlock")}</Button>
      )}
      {smime.cert && (
        <Button variant="outline" size="sm" onClick={() => onOpen("delete")}>{t("settings.privacy.smimeCertDelete")}</Button>
      )}
      <Button variant={smime.cert ? "outline" : "default"} size="sm" onClick={() => onOpen("import")}>
        {smime.cert ? t("settings.privacy.smimeCertView") : t("settings.privacy.smimeCertAdd")}
      </Button>
    </div>
  )
}

// SmimeControls is the S/MIME row of the privacy section with its dialogs.
export function SmimeControls() {
  const { t } = useI18n()
  const smime = useSmime()
  const [dialog, setDialog] = useState<OpenDialog>(null)
  const openChange = (d: OpenDialog) => (open: boolean) => setDialog(open ? d : null)

  return (
    <>
      <div className="flex items-center justify-between py-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <FileKey className="h-4 w-4 text-muted-foreground shrink-0" />
            <span className="text-sm font-medium">{t("settings.privacy.smimeCert")}</span>
          </div>
          <p className="text-xs text-muted-foreground mt-0.5">{t("settings.privacy.smimeCertDescription")}</p>
          <SmimeStatus smime={smime} />
        </div>
        <SmimeActions smime={smime} onOpen={setDialog} />
      </div>
      <ImportDialog smime={smime} open={dialog === "import"} onOpenChange={openChange("import")} />
      <UnlockDialog smime={smime} open={dialog === "unlock"} onOpenChange={openChange("unlock")} />
      <DeleteDialog smime={smime} open={dialog === "delete"} onOpenChange={openChange("delete")} />
    </>
  )
}
