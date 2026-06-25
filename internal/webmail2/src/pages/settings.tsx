import { useState, useEffect, useCallback, useRef, useMemo } from "react"
import { Moon, Sun, Bell, Shield, ShieldCheck, Palette, Keyboard, Mail, Globe, Lock, Plane, Monitor, UserCog, Trash2, Plus, Tag, X, Camera, HardDrive, FileKey, FileText } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useAuth } from "@/contexts/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Progress } from "@/components/ui/progress"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import api from "@/utils/api"
import type { VacationAutoReply, ClientSession, Delegation, Category, SignatureEntry, TemplateEntry } from "@/utils/api"
import * as smimeStore from "@/utils/smime"
import type { CertInfo } from "@/utils/smime"
import { detectTimeZone, listTimeZones } from "@/utils/timezone"
import { enablePushNotifications, disablePushNotifications, pushSupported } from "@/utils/push"
import { RichTextEditor } from "@/components/RichTextEditor"

// rfc3339ToDate extracts the YYYY-MM-DD part from an RFC3339 string for <input type="date">.
function rfc3339ToDate(value?: string): string {
  if (!value) return ""
  return value.slice(0, 10)
}

// dateToRFC3339 turns a YYYY-MM-DD value into an RFC3339 UTC timestamp, or undefined when empty.
function dateToRFC3339(value: string): string | undefined {
  if (!value) return undefined
  return `${value}T00:00:00Z`
}

const emptyVacation: VacationAutoReply = {
  enabled: false,
  subject: "Out of Office",
  message: "",
  external_message: "",
  audience: "all",
}

// formatStorageBytes renders a byte count with a binary-prefix unit for the
// storage gauge, falling back to a raw byte count below 1 KiB.
function formatStorageBytes(n: number): string {
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GB`
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${n} B`
}

export function SettingsPage() {
  const { theme, setTheme, resolvedTheme } = useTheme()
  const { user, updatePrefs } = useAuth()
  const { t } = useI18n()

  // Profile photo (self-service avatar). avatarVersion cache-busts the <img>
  // after an upload/removal so the new photo shows immediately.
  const email = user?.email ?? ""
  const initials = (email ? email.slice(0, 2) : "?").toUpperCase()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [avatarVersion, setAvatarVersion] = useState(1)
  const [avatarBusy, setAvatarBusy] = useState(false)
  // Only request the avatar endpoint when the user actually has a photo,
  // otherwise the <img> 404s and spams the console. Tracks upload/removal.
  const [hasAvatar, setHasAvatar] = useState(!!user?.hasAvatar)

  const handlePickAvatar = async (file: File) => {
    if (!file.type.startsWith("image/")) {
      toast.error(t("settings.profilePhoto.invalidType"))
      return
    }
    if (file.size > 1024 * 1024) {
      toast.error(t("settings.profilePhoto.tooLarge"))
      return
    }
    const dataURL = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = () => reject(reader.error)
      reader.readAsDataURL(file)
    })
    setAvatarBusy(true)
    try {
      await api.updateAvatar(dataURL)
      setHasAvatar(true)
      setAvatarVersion((v) => v + 1)
      toast.success(t("settings.profilePhoto.updated"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profilePhoto.updateFailed"))
    } finally {
      setAvatarBusy(false)
    }
  }

  const handleRemoveAvatar = async () => {
    setAvatarBusy(true)
    try {
      await api.removeAvatar()
      setHasAvatar(false)
      setAvatarVersion((v) => v + 1)
      toast.success(t("settings.profilePhoto.removed"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profilePhoto.removeFailed"))
    } finally {
      setAvatarBusy(false)
    }
  }

  // Directory profile (display name, title, department, phone) — shown in the
  // GAL and on Outlook contact cards. Self-service via /api/v1/profile.
  const [profile, setProfile] = useState({ display_name: "", title: "", department: "", phone: "" })
  const [profileBusy, setProfileBusy] = useState(false)

  // Read-only storage usage and graduated quota thresholds (absolute bytes,
  // 0 = disabled/unlimited), surfaced by GET /profile for the storage gauge.
  const [quota, setQuota] = useState({ used: 0, limit: 0, warn: 0, prohibitSend: 0 })

  // Display timezone — the IANA zone every date is rendered in. Empty means
  // "follow this device". Saved on change (like the theme buttons) and applied
  // live across the app via updatePrefs.
  const timeZones = useMemo(listTimeZones, [])
  const [timezone, setTimezone] = useState("")

  useEffect(() => {
    api.getProfile()
      .then((p) => {
        setProfile({
          display_name: p.display_name ?? "",
          title: p.title ?? "",
          department: p.department ?? "",
          phone: p.phone ?? "",
        })
        setQuota({
          used: p.quota_used ?? 0,
          limit: p.quota_limit ?? 0,
          warn: p.quota_warn ?? 0,
          prohibitSend: p.quota_prohibit_send ?? 0,
        })
        setTimezone(p.timezone ?? "")
      })
      .catch(() => undefined)
  }, [])

  const handleTimezoneChange = async (tz: string) => {
    setTimezone(tz)
    try {
      await api.updateProfile({ timezone: tz })
      updatePrefs({ timezone: tz })
      toast.success(t("settings.appearance.timezoneSaved"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.appearance.timezoneSaveFailed"))
    }
  }

  const handleSaveProfile = async () => {
    setProfileBusy(true)
    try {
      await api.updateProfile(profile)
      toast.success(t("settings.profile.updated"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profile.updateFailed"))
    } finally {
      setProfileBusy(false)
    }
  }

  // Password change dialog (Manage Account)
  const [pwOpen, setPwOpen] = useState(false)
  const [pwCurrent, setPwCurrent] = useState("")
  const [pwNew, setPwNew] = useState("")
  const [pwConfirm, setPwConfirm] = useState("")
  const [pwSaving, setPwSaving] = useState(false)

  // Active sessions
  const [sessions, setSessions] = useState<ClientSession[]>([])

  const loadSessions = useCallback(async () => {
    try {
      const res = await api.getSessions()
      setSessions(res.sessions ?? [])
    } catch (err) {
      console.error("Failed to load sessions:", err)
      setSessions([])
    }
  }, [])

  useEffect(() => {
    loadSessions()
  }, [loadSessions])

  const handleRevokeSession = async (id: string) => {
    try {
      await api.revokeSession(id)
      toast.success(t("settings.sessions.revoked"))
      setSessions((prev) => prev.filter((s) => s.id !== id))
    } catch (err) {
      console.error("Failed to revoke session:", err)
      toast.error(t("settings.sessions.revokeFailed"))
    }
  }

  const [pushBusy, setPushBusy] = useState(false)

  const handleEnablePush = async () => {
    setPushBusy(true)
    try {
      await enablePushNotifications()
      toast.success(t("settings.push.enabled"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.push.enableFailed"))
    } finally {
      setPushBusy(false)
    }
  }

  const handleDisablePush = async () => {
    setPushBusy(true)
    try {
      await disablePushNotifications()
      toast.success(t("settings.push.disabled"))
    } catch {
      toast.error(t("settings.push.disableFailed"))
    } finally {
      setPushBusy(false)
    }
  }

  // S/MIME identity: "browser" mode keeps the key in THIS browser (IndexedDB) and
  // the server holds only the public cert; "server" mode keeps the key on the
  // server (encrypted at rest) so it signs/decrypts and works across devices.
  const [smimeCert, setSmimeCert] = useState<CertInfo | null>(null)
  const [smimeMode, setSmimeMode] = useState<"browser" | "server" | null>(null)
  const [smimeUnlocked, setSmimeUnlocked] = useState(false)
  const [smimeLoading, setSmimeLoading] = useState(true)
  const [smimeImportOpen, setSmimeImportOpen] = useState(false)
  const [smimeImportMode, setSmimeImportMode] = useState<"browser" | "server">("browser")
  const [smimeFile, setSmimeFile] = useState<File | null>(null)
  const [smimePassword, setSmimePassword] = useState("")
  const [smimeSaving, setSmimeSaving] = useState(false)
  const [smimeUnlockOpen, setSmimeUnlockOpen] = useState(false)
  const [smimeUnlockPassword, setSmimeUnlockPassword] = useState("")
  const [smimeDeleteOpen, setSmimeDeleteOpen] = useState(false)
  const [smimeDeleting, setSmimeDeleting] = useState(false)

  const loadSmimeCert = useCallback(async () => {
    setSmimeLoading(true)
    try {
      const local = await smimeStore.storedCertInfo()
      if (local) {
        setSmimeCert(local); setSmimeMode("browser"); setSmimeUnlocked(smimeStore.isUnlocked())
      } else {
        const res = await api.getSMIMECertificate()
        if (!("hasKeys" in res) && res.mode === "server") {
          setSmimeCert({ subject: res.subject, issuer: res.issuer, notBefore: res.notBefore, notAfter: res.notAfter, serialNumber: res.serialNumber, fingerprint: res.fingerprint })
          setSmimeMode("server"); setSmimeUnlocked(true)
        } else {
          setSmimeCert(null); setSmimeMode(null)
        }
      }
    } catch {
      setSmimeCert(null); setSmimeMode(null)
    } finally {
      setSmimeLoading(false)
    }
  }, [])

  useEffect(() => { loadSmimeCert() }, [loadSmimeCert])

  const handleSmimeImport = async () => {
    if (!smimeFile || !smimePassword) return
    setSmimeSaving(true)
    try {
      const bytes = await smimeFile.arrayBuffer()
      if (smimeImportMode === "server") {
        // Server mode: send the .p12 + its password; the server stores the key.
        const arr = new Uint8Array(bytes)
        let bin = ""
        for (let i = 0; i < arr.length; i++) bin += String.fromCharCode(arr[i])
        const info = await api.uploadServerSMIME(btoa(bin), smimePassword)
        setSmimeCert({ subject: info.subject, issuer: info.issuer, notBefore: info.notBefore, notAfter: info.notAfter, serialNumber: info.serialNumber, fingerprint: info.fingerprint })
        setSmimeMode("server"); setSmimeUnlocked(true)
      } else {
        // Browser mode: store the key in IndexedDB, publish only the public cert.
        const { info, certPem } = await smimeStore.importP12(bytes, smimePassword)
        await api.uploadSMIMECertificate(certPem)
        setSmimeCert(info); setSmimeMode("browser"); setSmimeUnlocked(true)
      }
      setSmimeImportOpen(false)
      setSmimeFile(null)
      setSmimePassword("")
      toast.success(t("settings.privacy.smimeCertSaved"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.privacy.smimeCertSaveFailed"))
    } finally {
      setSmimeSaving(false)
    }
  }

  const handleSmimeUnlock = async () => {
    if (!smimeUnlockPassword) return
    setSmimeSaving(true)
    try {
      await smimeStore.unlock(smimeUnlockPassword)
      setSmimeUnlocked(true)
      setSmimeUnlockOpen(false)
      setSmimeUnlockPassword("")
      toast.success(t("settings.privacy.smimeUnlocked"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.privacy.smimeUnlockFailed"))
    } finally {
      setSmimeSaving(false)
    }
  }

  const handleSmimeDelete = async () => {
    setSmimeDeleting(true)
    try {
      await smimeStore.removeIdentity()
      await api.deleteSMIMECertificate().catch(() => {})
      setSmimeCert(null)
      setSmimeMode(null)
      setSmimeUnlocked(false)
      setSmimeDeleteOpen(false)
      toast.success(t("settings.privacy.smimeCertDeleted"))
    } catch {
      toast.error(t("settings.privacy.smimeCertDeleteFailed"))
    } finally {
      setSmimeDeleting(false)
    }
  }

  const handleChangePassword = async () => {
    if (pwNew.length < 8) {
      toast.error(t("settings.password.tooShort"))
      return
    }
    if (pwNew !== pwConfirm) {
      toast.error(t("settings.password.mismatch"))
      return
    }
    setPwSaving(true)
    try {
      await api.changePassword(pwCurrent, pwNew)
      toast.success(t("settings.password.updated"))
      setPwOpen(false)
      setPwCurrent("")
      setPwNew("")
      setPwConfirm("")
    } catch (err) {
      console.error("Failed to change password:", err)
      toast.error(t("settings.password.changeFailed"))
    } finally {
      setPwSaving(false)
    }
  }
  const [settings, setSettings] = useState({
    // Notifications
    emailNotifications: true,
    browserNotifications: false,
    soundNotifications: true,
    desktopNotifications: true,
    // Email
    autoSaveDraft: true,
    readReceipts: false,
    deliveryReceipts: true,
    // Privacy
    showOnlineStatus: false,
    allowReadReceipts: true,
    // Composition
    richTextMode: true,
    autoCorrect: true,
    spellCheck: true,
  })

  // Load persisted preferences on mount and merge over the defaults.
  useEffect(() => {
    let cancelled = false
    api.getPreferences()
      .then((res) => {
        if (cancelled || !res.preferences) return
        setSettings((prev) => ({ ...prev, ...res.preferences }))
      })
      .catch(() => {
        // keep defaults
      })
    return () => {
      cancelled = true
    }
  }, [])

  const handleToggle = async (key: keyof typeof settings) => {
    const next = { ...settings, [key]: !settings[key] }
    setSettings(next)
    try {
      await api.setPreferences(next)
      toast.success(t("settings.settingUpdated"))
    } catch (err) {
      console.error("Failed to save setting:", err)
      toast.error(t("settings.settingSaveFailed"))
      setSettings(settings) // revert
    }
  }

  // Vacation / Out-of-Office auto-reply (backed by /api/v1/vacation).
  const [vacation, setVacation] = useState<VacationAutoReply>(emptyVacation)
  const [vacationLoading, setVacationLoading] = useState(true)
  const [vacationSaving, setVacationSaving] = useState(false)

  const loadVacation = useCallback(async () => {
    setVacationLoading(true)
    try {
      const cfg = await api.getVacation()
      setVacation({ ...emptyVacation, ...cfg })
    } catch {
      setVacation(emptyVacation)
    } finally {
      setVacationLoading(false)
    }
  }, [])

  useEffect(() => {
    loadVacation()
  }, [loadVacation])

  const handleVacationSave = async () => {
    if (vacation.enabled && !vacation.subject.trim()) {
      toast.error(t("settings.autoReply.subjectRequired"))
      return
    }
    if (vacation.enabled && !vacation.message.trim()) {
      toast.error(t("settings.autoReply.messageRequired"))
      return
    }
    setVacationSaving(true)
    try {
      await api.setVacation(vacation)
      toast.success(t("settings.autoReply.saved"))
      await loadVacation()
    } catch {
      toast.error(t("settings.autoReply.saveFailed"))
    } finally {
      setVacationSaving(false)
    }
  }

  const handleVacationDisable = async () => {
    setVacationSaving(true)
    try {
      await api.deleteVacation()
      toast.success(t("settings.autoReply.disabled"))
      await loadVacation()
    } catch {
      toast.error(t("settings.autoReply.disableFailed"))
    } finally {
      setVacationSaving(false)
    }
  }

  // Outgoing-mail signatures (multi-row, backed by /api/v1/signatures).
  const [signatures, setSignatures] = useState<SignatureEntry[]>([])
  const [sigEditName, setSigEditName] = useState("")
  const [sigEditBody, setSigEditBody] = useState("")
  const [sigEditHTML, setSigEditHTML] = useState(false)
  const [sigSaving, setSigSaving] = useState(false)
  const [sigError, setSigError] = useState<string | null>(null)
  const sigBodyRef = useRef<{ getHTML: () => string; setHTML: (html: string) => void } | null>(null)

  useEffect(() => {
    let cancelled = false
    api.getSignatures()
      .then((res) => {
        if (!cancelled) setSignatures(res.signatures ?? [])
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const handleSignatureSave = async () => {
    const name = sigEditName.trim()
    if (!name) { setSigError("Name is required"); return }
    setSigError(null)
    setSigSaving(true)
    try {
      // When in HTML mode, grab the HTML from the rich-text editor
      const body = sigEditHTML && sigBodyRef.current
        ? sigBodyRef.current.getHTML()
        : sigEditBody
      await api.saveSignature({ name, body, is_html: sigEditHTML, ord: 0 })
      const res = await api.getSignatures()
      setSignatures(res.signatures ?? [])
      setSigEditName(""); setSigEditBody(""); setSigEditHTML(false)
      toast.success(t("settings.signature.saved"))
    } catch {
      toast.error(t("settings.signature.saveFailed"))
    } finally {
      setSigSaving(false)
    }
  }

  const handleSignatureDelete = async (name: string) => {
    try {
      await api.deleteSignature(name)
      setSignatures((prev) => prev.filter((s) => s.name !== name))
      toast.success(t("settings.signature.deleted"))
    } catch {
      toast.error(t("settings.signature.deleteFailed"))
    }
  }

  const editSignature = (sig: SignatureEntry) => {
    setSigEditName(sig.name)
    setSigEditBody(sig.body)
    setSigEditHTML(sig.is_html)
    setSigError(null)
  }

  // Message templates / snippets (backed by /api/v1/templates).
  const [templates, setTemplates] = useState<TemplateEntry[]>([])
  const [tplEditName, setTplEditName] = useState("")
  const [tplEditSubject, setTplEditSubject] = useState("")
  const [tplEditBody, setTplEditBody] = useState("")
  const [tplEditHTML, setTplEditHTML] = useState(false)
  const [tplSaving, setTplSaving] = useState(false)
  const [tplError, setTplError] = useState<string | null>(null)
  const tplBodyRef = useRef<{ getHTML: () => string; setHTML: (html: string) => void } | null>(null)

  useEffect(() => {
    let cancelled = false
    api.getTemplates()
      .then((res) => { if (!cancelled) setTemplates(res.templates ?? []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const handleTemplateSave = async () => {
    const name = tplEditName.trim()
    if (!name) { setTplError("Name is required"); return }
    setTplError(null)
    setTplSaving(true)
    try {
      const body = tplEditHTML && tplBodyRef.current
        ? tplBodyRef.current.getHTML()
        : tplEditBody
      await api.saveTemplate({ name, subject: tplEditSubject, body, is_html: tplEditHTML })
      const res = await api.getTemplates()
      setTemplates(res.templates ?? [])
      setTplEditName(""); setTplEditSubject(""); setTplEditBody(""); setTplEditHTML(false)
      toast.success(t("settings.template.saved"))
    } catch {
      toast.error(t("settings.template.saveFailed"))
    } finally {
      setTplSaving(false)
    }
  }

  const handleTemplateDelete = async (name: string) => {
    try {
      await api.deleteTemplate(name)
      setTemplates((prev) => prev.filter((t) => t.name !== name))
      toast.success(t("settings.template.deleted"))
    } catch {
      toast.error(t("settings.template.deleteFailed"))
    }
  }

  const editTemplate = (tpl: TemplateEntry) => {
    setTplEditName(tpl.name)
    setTplEditSubject(tpl.subject)
    setTplEditBody(tpl.body)
    setTplEditHTML(tpl.is_html)
    setTplError(null)
  }

  // Categories: named, colored labels the user can apply to messages.
  const [categories, setCategories] = useState<Category[]>([])
  const [catName, setCatName] = useState("")
  const [catColor, setCatColor] = useState("#ef4444")
  const [catBusy, setCatBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.getCategories()
      .then((res) => { if (!cancelled) setCategories(res.categories ?? []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const saveCategories = async (next: Category[]) => {
    const prev = categories
    setCategories(next)
    setCatBusy(true)
    try {
      const res = await api.setCategories(next)
      setCategories(res.categories ?? next)
    } catch {
      setCategories(prev)
      toast.error(t("settings.categories.saveFailed"))
    } finally {
      setCatBusy(false)
    }
  }

  const addCategory = () => {
    const name = catName.trim()
    if (!name) return
    if (categories.some((c) => c.name.toLowerCase() === name.toLowerCase())) {
      toast.error(t("settings.categories.exists"))
      return
    }
    setCatName("")
    void saveCategories([...categories, { name, color: catColor }])
  }

  const removeCategory = (name: string) => void saveCategories(categories.filter((c) => c.name !== name))

  // Safe senders: addresses/domains whose messages load remote images automatically.
  const [safeSenders, setSafeSenders] = useState<string[]>([])
  const [safeInput, setSafeInput] = useState("")
  const [safeBusy, setSafeBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.getSafeSenders()
      .then((res) => { if (!cancelled) setSafeSenders(res.safeSenders ?? []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const saveSafeSenders = async (next: string[]) => {
    const prev = safeSenders
    setSafeSenders(next)
    setSafeBusy(true)
    try {
      const res = await api.setSafeSenders(next)
      setSafeSenders(res.safeSenders ?? next)
    } catch {
      setSafeSenders(prev)
      toast.error(t("settings.safeSenders.saveFailed"))
    } finally {
      setSafeBusy(false)
    }
  }

  const addSafeSender = () => {
    const addr = safeInput.trim().toLowerCase()
    if (!addr) return
    if (safeSenders.includes(addr)) {
      setSafeInput("")
      return
    }
    setSafeInput("")
    void saveSafeSenders([...safeSenders, addr])
  }

  const removeSafeSender = (addr: string) => void saveSafeSenders(safeSenders.filter((s) => s !== addr))

  // Delegates: people the user grants access to their own mailbox.
  const [delegations, setDelegations] = useState<Delegation[]>([])
  const [delEmail, setDelEmail] = useState("")
  const [delWrite, setDelWrite] = useState(false)
  const [delSendOnBehalf, setDelSendOnBehalf] = useState(false)
  const [delBusy, setDelBusy] = useState(false)

  const loadDelegations = useCallback(async () => {
    try {
      const res = await api.getDelegations()
      setDelegations(res.delegations ?? [])
    } catch {
      setDelegations([])
    }
  }, [])

  useEffect(() => {
    loadDelegations()
  }, [loadDelegations])

  const handleAddDelegate = async () => {
    const grantee = delEmail.trim().toLowerCase()
    if (!grantee) {
      toast.error(t("settings.delegates.emailRequired"))
      return
    }
    setDelBusy(true)
    try {
      await api.createDelegation({
        grantee,
        rights: delWrite ? ["read", "write"] : ["read"],
        canSendOnBehalf: delSendOnBehalf,
      })
      toast.success(t("settings.delegates.added"))
      setDelEmail("")
      setDelWrite(false)
      setDelSendOnBehalf(false)
      await loadDelegations()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.delegates.addFailed"))
    } finally {
      setDelBusy(false)
    }
  }

  const handleRemoveDelegate = async (id: string) => {
    setDelBusy(true)
    try {
      await api.deleteDelegation(id)
      toast.success(t("settings.delegates.removed"))
      await loadDelegations()
    } catch {
      toast.error(t("settings.delegates.removeFailed"))
    } finally {
      setDelBusy(false)
    }
  }

  const SettingSection = ({
    icon: Icon,
    title,
    description,
    children
  }: {
    icon: React.ElementType
    title: string
    description: string
    children: React.ReactNode
  }) => (
    <div className="rounded-lg border bg-card">
      <div className="flex items-center gap-4 p-6 pb-4">
        <div className="rounded-full bg-muted p-2">
          <Icon className="h-5 w-5" />
        </div>
        <div>
          <h3 className="font-semibold">{title}</h3>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
      </div>
      <div className="px-6 pb-6">
        {children}
      </div>
    </div>
  )

  const SettingRow = ({
    title,
    description,
    checked,
    onChange
  }: {
    title: string
    description: string
    checked: boolean
    onChange: () => void
  }) => (
    <div className="flex items-center justify-between py-3">
      <div>
        <p className="font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  )

  return (
    <div className="space-y-6 max-w-3xl">
      <div>
        <h2 className="text-2xl font-bold">{t("nav.settings")}</h2>
        <p className="text-muted-foreground">
          {t("settings.description")}
        </p>
      </div>

      {/* Profile photo */}
      <SettingSection
        icon={Camera}
        title={t("settings.profilePhoto.title")}
        description={t("settings.profilePhoto.description")}
      >
        <div className="flex items-center gap-4">
          <Avatar className="h-16 w-16 ring-2 ring-primary/20">
            <AvatarImage src={hasAvatar && email ? api.avatarUrl(email, avatarVersion) : ""} alt={email} />
            <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground text-lg font-semibold">
              {initials}
            </AvatarFallback>
          </Avatar>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0]
                if (file) void handlePickAvatar(file)
                e.target.value = ""
              }}
            />
            <Button variant="outline" disabled={avatarBusy} onClick={() => fileInputRef.current?.click()}>
              <Camera className="mr-2 h-4 w-4" />
              {avatarBusy ? t("common.saving") : t("settings.profilePhoto.upload")}
            </Button>
            <Button variant="ghost" disabled={avatarBusy} onClick={handleRemoveAvatar}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.remove")}
            </Button>
          </div>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">{t("settings.profilePhoto.hint")}</p>
      </SettingSection>

      {/* Directory profile */}
      <SettingSection
        icon={UserCog}
        title={t("settings.profile.title")}
        description={t("settings.profile.description")}
      >
        <div className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="profile-display-name">{t("settings.profile.displayName")}</Label>
              <Input
                id="profile-display-name"
                value={profile.display_name}
                onChange={(e) => setProfile({ ...profile, display_name: e.target.value })}
                placeholder={t("settings.profile.displayNamePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-title">{t("settings.profile.jobTitle")}</Label>
              <Input
                id="profile-title"
                value={profile.title}
                onChange={(e) => setProfile({ ...profile, title: e.target.value })}
                placeholder={t("settings.profile.titlePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-department">{t("settings.profile.department")}</Label>
              <Input
                id="profile-department"
                value={profile.department}
                onChange={(e) => setProfile({ ...profile, department: e.target.value })}
                placeholder={t("settings.profile.departmentPlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-phone">{t("settings.profile.phone")}</Label>
              <Input
                id="profile-phone"
                value={profile.phone}
                onChange={(e) => setProfile({ ...profile, phone: e.target.value })}
                placeholder={t("settings.profile.phonePlaceholder")}
              />
            </div>
          </div>
          <Button onClick={handleSaveProfile} disabled={profileBusy}>
            {profileBusy ? t("common.saving") : t("settings.profile.save")}
          </Button>
        </div>
      </SettingSection>

      {/* Storage usage (read-only) */}
      <SettingSection
        icon={HardDrive}
        title={t("settings.storage.title")}
        description={t("settings.storage.description")}
      >
        <div className="space-y-3">
          {quota.limit > 0 ? (
            <>
              <Progress value={Math.min(100, Math.round((quota.used / quota.limit) * 100))} />
              <p className="text-sm text-muted-foreground">
                {t("settings.storage.used", {
                  used: formatStorageBytes(quota.used),
                  limit: formatStorageBytes(quota.limit),
                  pct: String(Math.min(100, Math.round((quota.used / quota.limit) * 100))),
                })}
              </p>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("settings.storage.unlimited", { used: formatStorageBytes(quota.used) })}
            </p>
          )}
          {quota.warn > 0 && (
            <p className="text-xs text-muted-foreground">
              {t("settings.storage.warnAt", { size: formatStorageBytes(quota.warn) })}
            </p>
          )}
          {quota.prohibitSend > 0 && (
            <p className="text-xs text-muted-foreground">
              {t("settings.storage.blockSendAt", { size: formatStorageBytes(quota.prohibitSend) })}
            </p>
          )}
        </div>
      </SettingSection>

      {/* Appearance */}
      <SettingSection
        icon={Palette}
        title={t("settings.appearance.title")}
        description={t("settings.appearance.description")}
      >
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <p className="font-medium">{t("settings.appearance.theme")}</p>
              <p className="text-sm text-muted-foreground">{t("settings.appearance.themeDescription")}</p>
            </div>
            <div className="flex gap-2">
              <Button
                variant={theme === "light" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("light")}
                title={t("settings.appearance.lightMode")}
              >
                <Sun className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "dark" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("dark")}
                title={t("settings.appearance.darkMode")}
              >
                <Moon className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "system" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("system")}
                title={t("settings.appearance.systemDefault")}
              >
                <Globe className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <Separator />
          <SettingRow
            title={t("settings.appearance.darkMode")}
            description={t("settings.appearance.darkModeDescription")}
            checked={theme === "dark"}
            onChange={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
          />
          <Separator />
          <div className="flex items-center justify-between gap-4">
            <div>
              <p className="font-medium">{t("settings.appearance.timezone")}</p>
              <p className="text-sm text-muted-foreground">{t("settings.appearance.timezoneDescription")}</p>
            </div>
            <select
              value={timezone}
              onChange={(e) => void handleTimezoneChange(e.target.value)}
              className="max-w-[16rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
            >
              <option value="">{t("settings.appearance.timezoneAuto", { zone: detectTimeZone() })}</option>
              {timeZones.map((z) => (
                <option key={z} value={z}>
                  {z}
                </option>
              ))}
            </select>
          </div>
        </div>
      </SettingSection>

      {/* Notifications */}
      <SettingSection
        icon={Bell}
        title={t("settings.notifications.title")}
        description={t("settings.notifications.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.notifications.email")}
            description={t("settings.notifications.emailDescription")}
            checked={settings.emailNotifications}
            onChange={() => handleToggle("emailNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.browser")}
            description={t("settings.notifications.browserDescription")}
            checked={settings.browserNotifications}
            onChange={() => handleToggle("browserNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.sound")}
            description={t("settings.notifications.soundDescription")}
            checked={settings.soundNotifications}
            onChange={() => handleToggle("soundNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.desktop")}
            description={t("settings.notifications.desktopDescription")}
            checked={settings.desktopNotifications}
            onChange={() => handleToggle("desktopNotifications")}
          />
        </div>
      </SettingSection>

      {/* Email Settings */}
      <SettingSection
        icon={Mail}
        title={t("settings.composition.title")}
        description={t("settings.composition.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.composition.autoSaveDrafts")}
            description={t("settings.composition.autoSaveDraftsDescription")}
            checked={settings.autoSaveDraft}
            onChange={() => handleToggle("autoSaveDraft")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.richText")}
            description={t("settings.composition.richTextDescription")}
            checked={settings.richTextMode}
            onChange={() => handleToggle("richTextMode")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.autoCorrect")}
            description={t("settings.composition.autoCorrectDescription")}
            checked={settings.autoCorrect}
            onChange={() => handleToggle("autoCorrect")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.spellCheck")}
            description={t("settings.composition.spellCheckDescription")}
            checked={settings.spellCheck}
            onChange={() => handleToggle("spellCheck")}
          />
        </div>
      </SettingSection>

      {/* Auto-Reply (Out of Office) */}
      <SettingSection
        icon={Plane}
        title={t("settings.autoReply.title")}
        description={t("settings.autoReply.description")}
      >
        {vacationLoading ? (
          <p className="text-sm text-muted-foreground py-3">{t("common.loading")}</p>
        ) : (
          <div className="space-y-4">
            <SettingRow
              title={t("settings.autoReply.enable")}
              description={t("settings.autoReply.enableDescription")}
              checked={vacation.enabled}
              onChange={() => setVacation({ ...vacation, enabled: !vacation.enabled })}
            />
            <Separator />
            <div className="space-y-2">
              <Label htmlFor="vacation-subject">{t("common.subject")}</Label>
              <Input
                id="vacation-subject"
                value={vacation.subject}
                onChange={(e) => setVacation({ ...vacation, subject: e.target.value })}
                placeholder={t("settings.autoReply.subjectPlaceholder")}
                disabled={!vacation.enabled}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="vacation-audience">{t("settings.autoReply.audience")}</Label>
              <select
                id="vacation-audience"
                value={vacation.audience || "all"}
                onChange={(e) => setVacation({ ...vacation, audience: e.target.value })}
                disabled={!vacation.enabled}
                className="max-w-[20rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
              >
                <option value="all">{t("settings.autoReply.audienceAll")}</option>
                <option value="internal">{t("settings.autoReply.audienceInternal")}</option>
                <option value="external">{t("settings.autoReply.audienceExternal")}</option>
              </select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="vacation-message">{t("settings.autoReply.internalMessage")}</Label>
              <Textarea
                id="vacation-message"
                value={vacation.message}
                onChange={(e) => setVacation({ ...vacation, message: e.target.value })}
                placeholder={t("settings.autoReply.messagePlaceholder")}
                rows={4}
                disabled={!vacation.enabled}
              />
            </div>
            {vacation.audience !== "internal" && (
              <div className="space-y-2">
                <Label htmlFor="vacation-external-message">{t("settings.autoReply.externalMessage")}</Label>
                <Textarea
                  id="vacation-external-message"
                  value={vacation.external_message || ""}
                  onChange={(e) => setVacation({ ...vacation, external_message: e.target.value })}
                  placeholder={t("settings.autoReply.externalMessagePlaceholder")}
                  rows={4}
                  disabled={!vacation.enabled}
                />
                <p className="text-xs text-muted-foreground">{t("settings.autoReply.externalMessageHelp")}</p>
              </div>
            )}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="vacation-start">{t("settings.autoReply.startDate")}</Label>
                <Input
                  id="vacation-start"
                  type="date"
                  value={rfc3339ToDate(vacation.start_date)}
                  onChange={(e) =>
                    setVacation({ ...vacation, start_date: dateToRFC3339(e.target.value) })
                  }
                  disabled={!vacation.enabled}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="vacation-end">{t("settings.autoReply.endDate")}</Label>
                <Input
                  id="vacation-end"
                  type="date"
                  value={rfc3339ToDate(vacation.end_date)}
                  onChange={(e) =>
                    setVacation({ ...vacation, end_date: dateToRFC3339(e.target.value) })
                  }
                  disabled={!vacation.enabled}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button onClick={handleVacationSave} disabled={vacationSaving}>
                {t("common.save")}
              </Button>
              <Button
                variant="outline"
                onClick={handleVacationDisable}
                disabled={vacationSaving}
              >
                {t("settings.disable")}
              </Button>
            </div>
          </div>
        )}
      </SettingSection>

      {/* Signature */}
      <SettingSection
        icon={Mail}
        title={t("settings.signature.title")}
        description={t("settings.signature.description")}
      >
        <div className="space-y-4">
          {/* Existing signatures list */}
          {signatures.length > 0 && (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t("settings.signature.current")}</p>
              {signatures.map((sig) => (
                <div key={sig.name} className="flex items-center justify-between rounded-md border px-3 py-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs rounded px-1.5 py-0.5 bg-muted font-mono">{sig.is_html ? "HTML" : "TXT"}</span>
                    <span className="truncate text-sm font-medium">{sig.name}</span>
                    <span className="text-xs text-muted-foreground truncate hidden sm:inline">
                      {sig.body.slice(0, 60)}{sig.body.length > 60 ? "…" : ""}
                    </span>
                  </div>
                  <div className="flex items-center gap-1 ml-2">
                    <Button variant="ghost" size="sm" onClick={() => editSignature(sig)}>{t("common.edit")}</Button>
                    <Button variant="ghost" size="sm" onClick={() => handleSignatureDelete(sig.name)}>
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
          {/* Add / edit form */}
          <div className="space-y-2 rounded-md border p-3">
            <p className="text-sm font-medium">{sigEditName ? t("settings.signature.edit") : t("settings.signature.addNew")}</p>
            <div className="flex items-center gap-2">
              <Input
                value={sigEditName}
                onChange={(e) => setSigEditName(e.target.value)}
                placeholder={t("settings.signature.namePlaceholder")}
                className="max-w-xs"
              />
              <label className="flex items-center gap-1.5 text-sm cursor-pointer">
                <input
                  type="checkbox"
                  checked={sigEditHTML}
                  onChange={(e) => setSigEditHTML(e.target.checked)}
                />
                {t("settings.signature.isHtml")}
              </label>
            </div>
            {sigError && <p className="text-xs text-destructive">{sigError}</p>}
            {sigEditHTML ? (
              <RichTextEditor
                ref={sigBodyRef}
                value={sigEditBody}
                onChange={setSigEditBody}
                placeholder={t("settings.signature.placeholderHtml")}
              />
            ) : (
              <Textarea
                value={sigEditBody}
                onChange={(e) => setSigEditBody(e.target.value)}
                placeholder={t("settings.signature.placeholder")}
                rows={4}
              />
            )}
            <div className="flex items-center gap-2">
              <Button onClick={handleSignatureSave} disabled={sigSaving || !sigEditName.trim()}>
                {sigSaving ? t("common.saving") : t("common.save")}
              </Button>
              {sigEditName && (
                <Button variant="ghost" size="sm" onClick={() => { setSigEditName(""); setSigEditBody(""); setSigEditHTML(false); setSigError(null) }}>
                  {t("common.cancel")}
                </Button>
              )}
            </div>
          </div>
        </div>
      </SettingSection>

      {/* Templates / snippets */}
      <SettingSection
        icon={FileText}
        title={t("settings.template.title")}
        description={t("settings.template.description")}
      >
        <div className="space-y-4">
          {/* Existing templates list */}
          {templates.length > 0 && (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t("settings.template.current")}</p>
              {templates.map((tpl) => (
                <div key={tpl.name} className="flex items-center justify-between rounded-md border px-3 py-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs rounded px-1.5 py-0.5 bg-muted font-mono">{tpl.is_html ? "HTML" : "TXT"}</span>
                    <span className="truncate text-sm font-medium">{tpl.name}</span>
                    {tpl.subject && (
                      <span className="text-xs text-muted-foreground truncate hidden sm:inline">
                        {t("settings.template.subject")}: {tpl.subject}
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-1 ml-2">
                    <Button variant="ghost" size="sm" onClick={() => editTemplate(tpl)}>{t("common.edit")}</Button>
                    <Button variant="ghost" size="sm" onClick={() => handleTemplateDelete(tpl.name)}>
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
          {/* Add / edit form */}
          <div className="space-y-2 rounded-md border p-3">
            <p className="text-sm font-medium">{tplEditName ? t("settings.template.edit") : t("settings.template.addNew")}</p>
            <div className="flex items-center gap-2">
              <Input
                value={tplEditName}
                onChange={(e) => setTplEditName(e.target.value)}
                placeholder={t("settings.template.namePlaceholder")}
                className="max-w-xs"
              />
              <Input
                value={tplEditSubject}
                onChange={(e) => setTplEditSubject(e.target.value)}
                placeholder={t("settings.template.subjectPlaceholder")}
                className="max-w-xs"
              />
              <label className="flex items-center gap-1.5 text-sm cursor-pointer">
                <input
                  type="checkbox"
                  checked={tplEditHTML}
                  onChange={(e) => setTplEditHTML(e.target.checked)}
                />
                {t("settings.signature.isHtml")}
              </label>
            </div>
            {tplError && <p className="text-xs text-destructive">{tplError}</p>}
            {tplEditHTML ? (
              <RichTextEditor
                ref={tplBodyRef}
                value={tplEditBody}
                onChange={setTplEditBody}
                placeholder={t("settings.template.bodyPlaceholderHtml")}
              />
            ) : (
              <Textarea
                value={tplEditBody}
                onChange={(e) => setTplEditBody(e.target.value)}
                placeholder={t("settings.template.bodyPlaceholder")}
                rows={4}
              />
            )}
            <div className="flex items-center gap-2">
              <Button onClick={handleTemplateSave} disabled={tplSaving || !tplEditName.trim()}>
                {tplSaving ? t("common.saving") : t("common.save")}
              </Button>
              {tplEditName && (
                <Button variant="ghost" size="sm" onClick={() => { setTplEditName(""); setTplEditSubject(""); setTplEditBody(""); setTplEditHTML(false); setTplError(null) }}>
                  {t("common.cancel")}
                </Button>
              )}
            </div>
          </div>
        </div>
      </SettingSection>

      {/* Categories */}
      <SettingSection
        icon={Tag}
        title={t("settings.categories.title")}
        description={t("settings.categories.description")}
      >
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            {categories.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("settings.categories.empty")}</p>
            ) : (
              categories.map((c) => (
                <span
                  key={c.name}
                  className="inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium text-white"
                  style={{ backgroundColor: c.color || "#64748b" }}
                >
                  {c.name}
                  <button
                    onClick={() => removeCategory(c.name)}
                    aria-label={t("settings.categories.removeAria", { name: c.name })}
                    className="opacity-80 hover:opacity-100"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))
            )}
          </div>
          <div className="flex items-center gap-2">
            <input
              type="color"
              value={catColor}
              onChange={(e) => setCatColor(e.target.value)}
              className="h-9 w-12 cursor-pointer rounded border bg-transparent"
              aria-label={t("settings.categories.colorAria")}
            />
            <Input
              value={catName}
              onChange={(e) => setCatName(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") addCategory() }}
              placeholder={t("settings.categories.namePlaceholder")}
            />
            <Button onClick={addCategory} disabled={catBusy || !catName.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              {t("common.add")}
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Safe senders */}
      <SettingSection
        icon={ShieldCheck}
        title={t("settings.safeSenders.title")}
        description={t("settings.safeSenders.description")}
      >
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            {safeSenders.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("settings.safeSenders.empty")}</p>
            ) : (
              safeSenders.map((addr) => (
                <span
                  key={addr}
                  className="inline-flex items-center gap-1 rounded-full border bg-muted px-2.5 py-0.5 text-xs font-medium"
                >
                  {addr}
                  <button
                    onClick={() => removeSafeSender(addr)}
                    aria-label={t("settings.safeSenders.removeAria", { sender: addr })}
                    className="opacity-60 hover:opacity-100"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))
            )}
          </div>
          <div className="flex items-center gap-2">
            <Input
              value={safeInput}
              onChange={(e) => setSafeInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") addSafeSender() }}
              placeholder={t("settings.safeSenders.placeholder")}
            />
            <Button onClick={addSafeSender} disabled={safeBusy || !safeInput.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              {t("common.add")}
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Delegates */}
      <SettingSection
        icon={UserCog}
        title={t("settings.delegates.title")}
        description={t("settings.delegates.description")}
      >
        <div className="space-y-4">
          {delegations.length > 0 && (
            <div className="space-y-2">
              {delegations.map((d) => (
                <div key={d.id} className="flex items-center justify-between rounded-lg border p-3">
                  <div className="min-w-0">
                    <p className="font-medium truncate">{d.grantee}</p>
                    <p className="text-xs text-muted-foreground">
                      {d.rights || t("settings.delegates.noAccess")}
                      {d.canSendOnBehalf ? ` · ${t("settings.delegates.sendOnBehalfTag")}` : ""}
                      {d.canSendAs ? ` · ${t("settings.delegates.sendAsTag")}` : ""}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-destructive"
                    onClick={() => handleRemoveDelegate(d.id)}
                    disabled={delBusy}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
          <div className="space-y-3 rounded-lg border p-3">
            <div className="space-y-2">
              <Label htmlFor="delegate-email">{t("settings.delegates.add")}</Label>
              <Input
                id="delegate-email"
                type="email"
                value={delEmail}
                onChange={(e) => setDelEmail(e.target.value)}
                placeholder={t("settings.delegates.emailPlaceholder")}
              />
            </div>
            <SettingRow
              title={t("settings.delegates.allowEditing")}
              description={t("settings.delegates.allowEditingDescription")}
              checked={delWrite}
              onChange={() => setDelWrite((v) => !v)}
            />
            <SettingRow
              title={t("settings.delegates.sendOnBehalf")}
              description={t("settings.delegates.sendOnBehalfDescription")}
              checked={delSendOnBehalf}
              onChange={() => setDelSendOnBehalf((v) => !v)}
            />
            <Button onClick={handleAddDelegate} disabled={delBusy || !delEmail.trim()}>
              <Plus className="mr-2 h-4 w-4" />
              {t("settings.delegates.addDelegate")}
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Privacy & Security */}
      <SettingSection
        icon={Shield}
        title={t("settings.privacy.title")}
        description={t("settings.privacy.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.privacy.readReceipts")}
            description={t("settings.privacy.readReceiptsDescription")}
            checked={settings.readReceipts}
            onChange={() => handleToggle("readReceipts")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.deliveryReceipts")}
            description={t("settings.privacy.deliveryReceiptsDescription")}
            checked={settings.deliveryReceipts}
            onChange={() => handleToggle("deliveryReceipts")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.showOnlineStatus")}
            description={t("settings.privacy.showOnlineStatusDescription")}
            checked={settings.showOnlineStatus}
            onChange={() => handleToggle("showOnlineStatus")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.allowReadReceipts")}
            description={t("settings.privacy.allowReadReceiptsDescription")}
            checked={settings.allowReadReceipts}
            onChange={() => handleToggle("allowReadReceipts")}
          />
          <Separator />
          <div className="flex items-center justify-between py-2">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <FileKey className="h-4 w-4 text-muted-foreground shrink-0" />
                <span className="text-sm font-medium">{t("settings.privacy.smimeCert")}</span>
              </div>
              <p className="text-xs text-muted-foreground mt-0.5">{t("settings.privacy.smimeCertDescription")}</p>
              {smimeLoading ? (
                <p className="text-xs text-muted-foreground mt-1">{t("common.loading")}</p>
              ) : smimeCert ? (
                <p className="text-xs text-muted-foreground mt-1 truncate">
                  {smimeCert.subject} · {smimeMode === "server"
                    ? t("settings.privacy.smimeServerBadge")
                    : (smimeUnlocked ? t("settings.privacy.smimeUnlockedBadge") : t("settings.privacy.smimeLockedBadge"))}
                </p>
              ) : (
                <p className="text-xs text-muted-foreground mt-1">{t("settings.privacy.smimeCertNone")}</p>
              )}
            </div>
            <div className="flex items-center gap-2 ml-4">
              {smimeCert && smimeMode === "browser" && !smimeUnlocked && (
                <Button variant="outline" size="sm" onClick={() => setSmimeUnlockOpen(true)}>
                  {t("settings.privacy.smimeUnlock")}
                </Button>
              )}
              {smimeCert && (
                <Button variant="outline" size="sm" onClick={() => setSmimeDeleteOpen(true)}>
                  {t("settings.privacy.smimeCertDelete")}
                </Button>
              )}
              <Button
                variant={smimeCert ? "outline" : "default"}
                size="sm"
                onClick={() => setSmimeImportOpen(true)}
              >
                {smimeCert ? t("settings.privacy.smimeCertView") : t("settings.privacy.smimeCertAdd")}
              </Button>
            </div>
          </div>
        </div>
      </SettingSection>

      {/* Keyboard Shortcuts */}
      <SettingSection
        icon={Keyboard}
        title={t("settings.shortcuts.title")}
        description={t("settings.shortcuts.description")}
      >
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t("settings.shortcuts.help")}
          </p>
          <Button
            variant="outline"
            onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}
          >
            {t("settings.shortcuts.view")}
          </Button>
        </div>
      </SettingSection>

      {/* Push Notifications */}
      <SettingSection
        icon={Bell}
        title={t("settings.push.title")}
        description={t("settings.push.description")}
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={handleEnablePush} disabled={pushBusy || !pushSupported()}>
            {pushBusy ? t("settings.push.working") : t("settings.push.enable")}
          </Button>
          <Button variant="outline" onClick={handleDisablePush} disabled={pushBusy || !pushSupported()}>
            {t("settings.disable")}
          </Button>
          {!pushSupported() && (
            <span className="text-sm text-muted-foreground">
              {t("settings.push.notSupported")}
            </span>
          )}
        </div>
      </SettingSection>

      {/* Active Sessions */}
      <SettingSection
        icon={Monitor}
        title={t("settings.sessions.title")}
        description={t("settings.sessions.description")}
      >
        {sessions.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("settings.sessions.empty")}</p>
        ) : (
          <div className="space-y-2">
            {sessions.map((s) => (
              <div key={s.id} className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium truncate">
                    {s.device_type || t("settings.sessions.unknownDevice")} · {s.client_ip || t("settings.sessions.unknownIp")}
                  </p>
                  <p className="text-xs text-muted-foreground truncate">
                    {s.user_agent || "—"}
                  </p>
                  <p className="text-xs text-muted-foreground">{t("settings.sessions.lastActive", { time: s.last_active })}</p>
                </div>
                <Button variant="outline" size="sm" onClick={() => handleRevokeSession(s.id)}>
                  {t("settings.sessions.revoke")}
                </Button>
              </div>
            ))}
          </div>
        )}
      </SettingSection>

      {/* Account */}
      <div className="rounded-lg border bg-card p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-4">
            <div className="rounded-full bg-destructive/10 p-2">
              <Lock className="h-5 w-5 text-destructive" />
            </div>
            <div>
              <h3 className="font-semibold">{t("settings.account.title")}</h3>
              <p className="text-sm text-muted-foreground">
                {t("settings.account.description")}
              </p>
            </div>
          </div>
          <Button variant="outline" onClick={() => setPwOpen(true)}>{t("settings.account.manage")}</Button>
        </div>
      </div>

      <Dialog open={pwOpen} onOpenChange={setPwOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.password.title")}</DialogTitle>
            <DialogDescription>
              {t("settings.password.description")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="pw-current">{t("settings.password.current")}</Label>
              <Input
                id="pw-current"
                type="password"
                value={pwCurrent}
                onChange={(e) => setPwCurrent(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-new">{t("settings.password.new")}</Label>
              <Input
                id="pw-new"
                type="password"
                value={pwNew}
                onChange={(e) => setPwNew(e.target.value)}
                autoComplete="new-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-confirm">{t("settings.password.confirm")}</Label>
              <Input
                id="pw-confirm"
                type="password"
                value={pwConfirm}
                onChange={(e) => setPwConfirm(e.target.value)}
                autoComplete="new-password"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPwOpen(false)} disabled={pwSaving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleChangePassword} disabled={pwSaving}>
              {pwSaving ? t("common.saving") : t("settings.password.update")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* S/MIME Upload / View Dialog */}
      <Dialog open={smimeImportOpen} onOpenChange={setSmimeImportOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {smimeCert ? t("settings.privacy.smimeCertView") : t("settings.privacy.smimeCertUpload")}
            </DialogTitle>
            <DialogDescription>
              {t("settings.privacy.smimeImportDesc")}
            </DialogDescription>
          </DialogHeader>

          {smimeCert && (
            <div className="rounded-lg border bg-muted/50 p-4 space-y-2">
              <div className="grid grid-cols-[120px_1fr] gap-1 text-sm">
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertSubject")}:</span>
                <span className="break-all">{smimeCert.subject}</span>
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertIssuer")}:</span>
                <span className="break-all">{smimeCert.issuer}</span>
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertValidFrom")}:</span>
                <span>{smimeCert.notBefore ? new Date(smimeCert.notBefore).toLocaleDateString() : "—"}</span>
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertValidUntil")}:</span>
                <span>{smimeCert.notAfter ? new Date(smimeCert.notAfter).toLocaleDateString() : "—"}</span>
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertSerial")}:</span>
                <span className="font-mono text-xs break-all">{smimeCert.serialNumber}</span>
                <span className="text-muted-foreground font-medium">{t("settings.privacy.smimeCertFingerprint")}:</span>
                <span className="font-mono text-xs break-all">{smimeCert.fingerprint}</span>
              </div>
            </div>
          )}

          <div className="space-y-3">
            <div className="space-y-1">
              <Label>{t("settings.privacy.smimeStorageLabel")}</Label>
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => setSmimeImportMode("browser")}
                  className={`rounded-md border p-2 text-left text-xs ${smimeImportMode === "browser" ? "border-primary bg-primary/5" : "border-input"}`}
                >
                  <div className="font-medium">{t("settings.privacy.smimeStoreBrowser")}</div>
                  <div className="text-muted-foreground">{t("settings.privacy.smimeStoreBrowserHint")}</div>
                </button>
                <button
                  type="button"
                  onClick={() => setSmimeImportMode("server")}
                  className={`rounded-md border p-2 text-left text-xs ${smimeImportMode === "server" ? "border-primary bg-primary/5" : "border-input"}`}
                >
                  <div className="font-medium">{t("settings.privacy.smimeStoreServer")}</div>
                  <div className="text-muted-foreground">{t("settings.privacy.smimeStoreServerHint")}</div>
                </button>
              </div>
            </div>
            <div className="space-y-1">
              <Label htmlFor="smime-p12">{t("settings.privacy.smimeP12File")}</Label>
              <Input
                id="smime-p12"
                type="file"
                accept=".p12,.pfx"
                onChange={(e) => setSmimeFile(e.target.files?.[0] ?? null)}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="smime-p12-pass">{t("settings.privacy.smimeP12Password")}</Label>
              <Input
                id="smime-p12-pass"
                type="password"
                value={smimePassword}
                onChange={(e) => setSmimePassword(e.target.value)}
                placeholder={t("settings.privacy.smimeP12PasswordPlaceholder")}
              />
            </div>
            <p className="text-xs text-muted-foreground">{t("settings.privacy.smimeImportHint")}</p>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => {
              setSmimeImportOpen(false)
              setSmimeFile(null)
              setSmimePassword("")
            }} disabled={smimeSaving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleSmimeImport} disabled={smimeSaving || !smimeFile || !smimePassword}>
              {smimeSaving ? t("common.saving") : t("settings.privacy.smimeCertAdd")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* S/MIME Unlock (per session) */}
      <Dialog open={smimeUnlockOpen} onOpenChange={setSmimeUnlockOpen}>
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
              value={smimeUnlockPassword}
              onChange={(e) => setSmimeUnlockPassword(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") handleSmimeUnlock() }}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setSmimeUnlockOpen(false); setSmimeUnlockPassword("") }} disabled={smimeSaving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleSmimeUnlock} disabled={smimeSaving || !smimeUnlockPassword}>
              {t("settings.privacy.smimeUnlock")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* S/MIME Delete Confirmation */}
      <Dialog open={smimeDeleteOpen} onOpenChange={setSmimeDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.privacy.smimeCertDelete")}</DialogTitle>
            <DialogDescription>
              {t("settings.privacy.smimeCertDeleteConfirm")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSmimeDeleteOpen(false)} disabled={smimeDeleting}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleSmimeDelete} disabled={smimeDeleting}>
              {smimeDeleting ? t("common.deleting") : t("settings.privacy.smimeCertDelete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="text-center text-sm text-muted-foreground pb-8">
        <p>hermEX Webmail</p>
        <p className="mt-1">{t("settings.footer.tagline")}</p>
      </div>
    </div>
  )
}
