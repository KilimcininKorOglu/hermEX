import { useRef, useState } from "react"
import { Camera, HardDrive, Trash2, UserCog } from "lucide-react"
import { toast } from "sonner"
import { useAuth } from "@/contexts/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import type { ProfileFields, QuotaInfo } from "@/hooks/useProfile"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Progress } from "@/components/ui/progress"
import api from "@/utils/api"
import { formatStorageBytes, usagePercent } from "@/utils/settingsRecords"
import { SettingSection } from "./setting-layout"

const MAX_AVATAR_BYTES = 1024 * 1024

// readDataURL reads a file as a data: URL.
function readDataURL(file: File): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

// avatarError returns the i18n key of the reason a file cannot be a profile
// photo, or null.
function avatarError(file: File): string | null {
  if (!file.type.startsWith("image/")) return "settings.profilePhoto.invalidType"
  if (file.size > MAX_AVATAR_BYTES) return "settings.profilePhoto.tooLarge"
  return null
}

// useAvatar uploads and removes the self-service profile photo. version
// cache-busts the <img> after a change so the new photo shows at once.
function useAvatar() {
  const { user } = useAuth()
  const { t } = useI18n()
  const [version, setVersion] = useState(1)
  const [busy, setBusy] = useState(false)
  // Only request the avatar endpoint when the user actually has a photo,
  // otherwise the <img> 404s and spams the console.
  const [hasAvatar, setHasAvatar] = useState(!!user?.hasAvatar)

  const run = async (action: () => Promise<unknown>, present: boolean, okKey: string, failKey: string) => {
    setBusy(true)
    try {
      await action()
      setHasAvatar(present)
      setVersion((v) => v + 1)
      toast.success(t(okKey))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(failKey))
    } finally {
      setBusy(false)
    }
  }

  const pick = async (file: File) => {
    const invalid = avatarError(file)
    if (invalid) {
      toast.error(t(invalid))
      return
    }
    const dataURL = await readDataURL(file)
    await run(() => api.updateAvatar(dataURL), true, "settings.profilePhoto.updated", "settings.profilePhoto.updateFailed")
  }

  const remove = () =>
    run(() => api.removeAvatar(), false, "settings.profilePhoto.removed", "settings.profilePhoto.removeFailed")

  return { version, busy, hasAvatar, pick, remove }
}

export function ProfilePhotoSection() {
  const { user } = useAuth()
  const { t } = useI18n()
  const avatar = useAvatar()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const email = user?.email ?? ""
  const initials = (email ? email.slice(0, 2) : "?").toUpperCase()
  const src = avatar.hasAvatar && email ? api.avatarUrl(email, avatar.version) : ""

  return (
    <SettingSection icon={Camera} title={t("settings.profilePhoto.title")} description={t("settings.profilePhoto.description")}>
      <div className="flex items-center gap-4">
        <Avatar className="h-16 w-16 ring-2 ring-primary/20">
          <AvatarImage src={src} alt={email} />
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
              if (file) void avatar.pick(file)
              e.target.value = ""
            }}
          />
          <Button variant="outline" disabled={avatar.busy} onClick={() => fileInputRef.current?.click()}>
            <Camera className="mr-2 h-4 w-4" />
            {avatar.busy ? t("common.saving") : t("settings.profilePhoto.upload")}
          </Button>
          <Button variant="ghost" disabled={avatar.busy} onClick={() => void avatar.remove()}>
            <Trash2 className="mr-2 h-4 w-4" />
            {t("common.remove")}
          </Button>
        </div>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">{t("settings.profilePhoto.hint")}</p>
    </SettingSection>
  )
}

// PROFILE_FIELDS lists the directory profile inputs: the field, its input id,
// and the i18n keys of its label and placeholder.
const PROFILE_FIELDS: { key: keyof ProfileFields; id: string; label: string; placeholder: string }[] = [
  { key: "display_name", id: "profile-display-name", label: "settings.profile.displayName", placeholder: "settings.profile.displayNamePlaceholder" },
  { key: "title", id: "profile-title", label: "settings.profile.jobTitle", placeholder: "settings.profile.titlePlaceholder" },
  { key: "department", id: "profile-department", label: "settings.profile.department", placeholder: "settings.profile.departmentPlaceholder" },
  { key: "phone", id: "profile-phone", label: "settings.profile.phone", placeholder: "settings.profile.phonePlaceholder" },
]

export function ProfileSection({
  profile,
  onChange,
  busy,
  onSave,
}: {
  profile: ProfileFields
  onChange: (next: ProfileFields) => void
  busy: boolean
  onSave: () => void
}) {
  const { t } = useI18n()
  return (
    <SettingSection icon={UserCog} title={t("settings.profile.title")} description={t("settings.profile.description")}>
      <div className="space-y-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {PROFILE_FIELDS.map((f) => (
            <div key={f.key} className="space-y-2">
              <Label htmlFor={f.id}>{t(f.label)}</Label>
              <Input
                id={f.id}
                value={profile[f.key]}
                onChange={(e) => onChange({ ...profile, [f.key]: e.target.value })}
                placeholder={t(f.placeholder)}
              />
            </div>
          ))}
        </div>
        <Button onClick={onSave} disabled={busy}>
          {busy ? t("common.saving") : t("settings.profile.save")}
        </Button>
      </div>
    </SettingSection>
  )
}

// StorageUsage shows the gauge against a quota, or the bare usage without one.
function StorageUsage({ quota }: { quota: QuotaInfo }) {
  const { t } = useI18n()
  if (quota.limit <= 0) {
    return <p className="text-sm text-muted-foreground">{t("settings.storage.unlimited", { used: formatStorageBytes(quota.used) })}</p>
  }
  const pct = usagePercent(quota.used, quota.limit)
  return (
    <>
      <Progress value={pct} />
      <p className="text-sm text-muted-foreground">
        {t("settings.storage.used", {
          used: formatStorageBytes(quota.used),
          limit: formatStorageBytes(quota.limit),
          pct: String(pct),
        })}
      </p>
    </>
  )
}

// QuotaThreshold names one quota threshold, when it is set.
function QuotaThreshold({ bytes, labelKey }: { bytes: number; labelKey: string }) {
  const { t } = useI18n()
  if (bytes <= 0) return null
  return <p className="text-xs text-muted-foreground">{t(labelKey, { size: formatStorageBytes(bytes) })}</p>
}

export function StorageSection({ quota }: { quota: QuotaInfo }) {
  const { t } = useI18n()
  return (
    <SettingSection icon={HardDrive} title={t("settings.storage.title")} description={t("settings.storage.description")}>
      <div className="space-y-3">
        <StorageUsage quota={quota} />
        <QuotaThreshold bytes={quota.warn} labelKey="settings.storage.warnAt" />
        <QuotaThreshold bytes={quota.prohibitSend} labelKey="settings.storage.blockSendAt" />
      </div>
    </SettingSection>
  )
}
